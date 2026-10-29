package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/stefa/llm-cockpit/internal/core"
)

func keys(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func special(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// send runs one Update with a timeout so a deadlock fails the test instead
// of hanging it.
func send(t *testing.T, a *Model, msg tea.Msg) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		a.Update(msg)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Update(%v) hung", msg)
	}
}

// Restart with a persisted session: the UI must stay responsive.
func TestRestartWithSavedSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, _ := core.NewManagerAt(path)
	if _, err := first.Create("alt", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	first.StopAll()

	mgr, err := core.NewManagerAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.StopAll()
	a := New(mgr)
	send(t, a, a.Init()) // returns a batch cmd; executing it is not needed
	send(t, a, tea.WindowSizeMsg{Width: 100, Height: 25})
	for _, k := range []tea.Msg{keys("j"), keys("k"), special(tea.KeyTab), keys("?"), special(tea.KeyEsc), special(tea.KeyEnter)} {
		send(t, a, k)
	}
	if len(a.sessions) != 1 || a.focus != focusInput {
		t.Fatalf("sessions=%d focus=%d status=%q", len(a.sessions), a.focus, a.status)
	}
	if !strings.Contains(a.View(), "alt") {
		t.Fatal("saved session not shown")
	}
}

func click(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}

func TestMouse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	setup, _ := core.NewManagerAt(path)
	for _, name := range []string{"eins", "zwei"} {
		if _, err := setup.Create(name, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	setup.StopAll()

	mgr, _ := core.NewManagerAt(path)
	defer mgr.StopAll()
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 140, Height: 30})
	a.View()

	// Second session occupies content rows 2-3, i.e. screen rows 3-4.
	send(t, a, click(5, 3))
	if a.sel != 1 || a.cur.Name != "zwei" {
		t.Fatalf("click on session: sel=%d cur=%q", a.sel, a.cur.Name)
	}
	send(t, a, tea.MouseMsg{X: 5, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if a.sel != 0 {
		t.Fatalf("wheel up over list: sel=%d", a.sel)
	}
	// Click into the input box.
	send(t, a, click(10, 30-3))
	if a.focus != focusInput {
		t.Fatalf("click on input: focus=%d", a.focus)
	}
	send(t, a, special(tea.KeyEsc))
	// "[+ Neu]" is the first button in the bottom row.
	send(t, a, click(2, 29))
	if a.focus != focusNewDir {
		t.Fatalf("click on [+ Neu]: focus=%d", a.focus)
	}
	send(t, a, special(tea.KeyEsc))
	// "[?]" opens the help.
	x := strings.Index(ansiStrip(a.renderButtons()), "[?]")
	send(t, a, click(x+1, 29))
	if a.focus != focusOverlay {
		t.Fatalf("click on [?] at x=%d: focus=%d", x, a.focus)
	}
}

func TestStreamingRender(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 100, Height: 20})
	a.cur = core.Session{Messages: []core.Message{
		{Kind: core.KindTool, Text: "Glob *"},
		{Kind: core.KindResult, Text: "a.go (+2 Zeilen)"},
		{Kind: core.KindAssistant, Text: "Im Ordner", Partial: true},
	}}
	a.renderMessages()
	out := a.vp.View()
	for _, want := range []string{"⚙ Glob *", "↳ a.go (+2 Zeilen)", "Im Ordner▌"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func ansiStrip(s string) string { return ansi.Strip(s) }

// run executes a command and feeds its message back, like the Bubble Tea loop.
func run(t *testing.T, a *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd != nil {
		if msg := cmd(); msg != nil {
			send(t, a, msg)
		}
	}
}

func TestFileView(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hi\n"), 0o644)

	mgr, _ := core.NewManagerAt("")
	defer mgr.StopAll()
	if _, err := mgr.Create("files", dir); err != nil {
		t.Fatal(err)
	}
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 120, Height: 30})

	_, cmd := a.handleNav("d")
	run(t, a, cmd)
	if a.focus != focusFiles || a.files.total != 2 {
		t.Fatalf("focus=%d total=%d", a.focus, a.files.total)
	}
	// Folders first, collapsed: "src/", then "readme.md".
	if len(a.files.rows) != 2 || a.files.rows[0].node.name != "src" {
		t.Fatalf("rows=%+v", a.files.rows)
	}
	send(t, a, special(tea.KeyEnter)) // expand src
	if len(a.files.rows) != 3 {
		t.Fatalf("after expand: %d rows", len(a.files.rows))
	}
	send(t, a, keys("j"))
	_, cmd = a.handleFilesKey("enter") // preview src/main.go
	run(t, a, cmd)
	if a.focus != focusOverlay || !strings.Contains(a.overlay.View(), "package main") {
		t.Fatalf("preview not shown: focus=%d\n%s", a.focus, a.overlay.View())
	}
	send(t, a, special(tea.KeyEsc))
	if a.focus != focusFiles {
		t.Fatalf("esc from preview should return to files, focus=%d", a.focus)
	}
	if !strings.Contains(a.View(), "readme.md") {
		t.Fatal("file list not rendered")
	}
	send(t, a, keys("m")) // only changed: nothing in a plain dir
	if len(a.files.rows) != 0 {
		t.Fatalf("only changed: %d rows", len(a.files.rows))
	}
	send(t, a, special(tea.KeyEsc))
	if a.focus != focusList {
		t.Fatalf("esc should leave files, focus=%d", a.focus)
	}
}

func TestCreateSessionFlow(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	defer mgr.StopAll()
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 120, Height: 30})

	dir := t.TempDir()
	send(t, a, keys("n"))
	a.input.SetValue(dir)
	send(t, a, special(tea.KeyEnter))
	if a.focus != focusNewName {
		t.Fatalf("after dir: focus=%d status=%q", a.focus, a.status)
	}
	a.input.SetValue("demo")
	send(t, a, special(tea.KeyEnter))

	if len(a.sessions) != 1 || a.sessions[0].Name != "demo" {
		t.Fatalf("sessions=%+v status=%q", a.sessions, a.status)
	}
	if a.focus != focusInput {
		t.Fatalf("expected input focus, got %d", a.focus)
	}
	view := a.View()
	if !strings.Contains(view, "demo") {
		t.Fatalf("view does not show session:\n%s", view)
	}
	if h := strings.Count(view, "\n") + 1; h != 30 {
		t.Errorf("view height %d, want 30", h)
	}

	// esc back to the list, navigate, open help, close it.
	for _, k := range []tea.Msg{special(tea.KeyEsc), keys("j"), keys("k"), keys("?"), special(tea.KeyEsc), keys("o"), special(tea.KeyEsc)} {
		send(t, a, k)
	}
	if a.focus != focusList {
		t.Fatalf("expected list focus, got %d", a.focus)
	}
}
