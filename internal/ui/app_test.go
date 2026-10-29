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
	barTop := a.bodyHeight() + 5
	send(t, a, click(10, barTop-2))
	if a.focus != focusInput {
		t.Fatalf("click on input: focus=%d", a.focus)
	}
	send(t, a, special(tea.KeyEsc))
	// "[+ Neu]" is the first button of the bar.
	send(t, a, click(2, barTop))
	if a.focus != focusNewDir {
		t.Fatalf("click on [+ Neu]: focus=%d", a.focus)
	}
	send(t, a, special(tea.KeyEsc))
	// Every button must be visible and clickable, also "[?]" at the end.
	view := strings.Split(ansiStrip(a.View()), "\n")
	if len(view) != 30 {
		t.Fatalf("view has %d rows, want 30", len(view))
	}
	found := false
	for r := barTop; r < len(view); r++ {
		if x := strings.Index(view[r], "[?]"); x >= 0 {
			send(t, a, click(x+1, r))
			found = true
		}
	}
	if !found || a.focus != focusOverlay {
		t.Fatalf("[?] not found or not clickable (found=%v focus=%d)", found, a.focus)
	}
}

func TestButtonBarWrapsOnNarrowScreens(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 80, Height: 30})
	out := ansiStrip(a.View())
	if rows := strings.Split(out, "\n"); len(rows) != 30 {
		t.Fatalf("view has %d rows, want 30", len(rows))
	}
	for _, b := range buttons {
		if !strings.Contains(out, "["+b.label+"]") {
			t.Errorf("button %q not visible at width 80", b.label)
		}
	}
}

func TestCursorFile(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 100, Height: 20})
	dir := t.TempDir()
	a.cur = core.Session{Dir: dir, Messages: []core.Message{
		{Kind: core.KindTool, Tool: "Edit", File: "src/main.go", Text: "Edit src/main.go"},
		{Kind: core.KindTool, Tool: "Bash", Text: "Bash ls"},
	}}
	if _, ok := a.cursorFile(); ok {
		t.Fatal("cursorFile outside the message view")
	}
	a.focus, a.msgCursor = focusMessages, 0
	f, ok := a.cursorFile()
	if want := filepath.Join(dir, "src", "main.go"); !ok || f != want {
		t.Fatalf("got %q %v, want %q", f, ok, want)
	}
	a.renderMessages()
	if !strings.Contains(a.vp.View(), "[c] IDE · [f] Diff") {
		t.Errorf("hint missing:\n%s", a.vp.View())
	}
	a.msgCursor = 1
	if _, ok := a.cursorFile(); ok {
		t.Fatal("Bash line has no file")
	}
}

func TestStreamingRender(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 100, Height: 60})
	a.cur = core.Session{Messages: []core.Message{
		{Kind: core.KindUser, Text: "Wie viele Dateien?"},
		{Kind: core.KindThinking, Text: "nachgedacht (1.5 s)"},
		{Kind: core.KindTool, Tool: "Glob", Text: "Glob *", Detail: "pattern: *"},
		{Kind: core.KindResult, Text: "a.go (+2 Zeilen)", Detail: "a.go\nb.go\nc.go"},
		{Kind: core.KindTool, Tool: "Edit", Text: "Edit x.go", File: "x.go", Detail: "- alt\n+ neu"},
		{Kind: core.KindDenied, Text: "PowerShell go test  – vom Rechte-Modus nicht erlaubt"},
		{Kind: core.KindTurn, Text: "fertig · 1.0 s · 2 Schritte"},
		{Kind: core.KindAssistant, Text: "Im Ordner", Partial: true},
	}}
	a.renderMessages()
	out := ansi.Strip(a.vp.View())
	for _, want := range []string{
		"> Wie viele Dateien?", "✻ nachgedacht (1.5 s)", "⏺ Glob(*)", "pattern: *",
		"⎿  a.go", "c.go", "⏺ Update(x.go)", "- alt", "+ neu",
		"⛔ verweigert: PowerShell go test", "✓ fertig", "⏺ Im Ordner▌",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPinAnyEntryAndJump(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	id, err := mgr.Create("pins", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.StopAll()
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 120, Height: 40})
	// Pin a tool call and a turn summary (neither is an assistant answer).
	a.cur.Messages = []core.Message{
		{UUID: "u1", Kind: core.KindTool, Tool: "Bash", Text: "Bash go test", Detail: "$ go test"},
		{Kind: core.KindTurn, Text: "fertig · 1.0 s"},
	}
	a.focus = focusMessages
	for _, i := range []int{0, 1} {
		a.msgCursor = i
		key := core.MessageKeys(a.cur.Messages)[i]
		if !mgr.TogglePin(id, key, pinLabel(a.cur.Messages[i])) {
			t.Fatalf("entry %d not pinned", i)
		}
	}
	s, _ := mgr.Get(id)
	if len(s.Pins) != 2 || s.Pins[0].Excerpt != "⏺ Bash go test" || s.Pins[1].Excerpt != "✓ fertig · 1.0 s" {
		t.Fatalf("pins %+v", s.Pins)
	}
	a.cur.Pins = s.Pins
	a.renderMessages()
	if out := ansi.Strip(a.vp.View()); strings.Count(out, "★") != 2 {
		t.Fatalf("stars missing:\n%s", out)
	}
	a.jumpTo(s.Pins[1].UUID)
	if a.msgCursor != 1 || a.focus != focusMessages {
		t.Fatalf("jump: cursor=%d focus=%d", a.msgCursor, a.focus)
	}
}

func TestModelPickerAndUsage(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	id, err := mgr.Create("modell", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr.Stop(id)
	defer mgr.StopAll()
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 140, Height: 40})
	send(t, a, keys("M"))
	if a.focus != focusPick || !strings.Contains(ansi.Strip(a.View()), "Haiku 5.5") {
		t.Fatalf("picker not shown, focus=%d", a.focus)
	}
	send(t, a, keys("j"))
	send(t, a, keys("j"))
	send(t, a, keys("j")) // Haiku
	send(t, a, special(tea.KeyEnter))
	s, _ := mgr.Get(id)
	if s.Model != "claude-haiku-5-5" {
		t.Fatalf("model %q, status %q", s.Model, a.status)
	}
	if !strings.Contains(ansi.Strip(a.View()), "Haiku 5.5") {
		t.Fatal("header does not show the model")
	}

	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	lines := usageLines(core.Limits{
		FiveHour:  core.Window{Utilization: 0.26, ResetsAt: now.Add(2*time.Hour + 30*time.Minute)},
		SevenDay:  core.Window{Utilization: 0.58, ResetsAt: time.Date(2026, 10, 30, 10, 0, 0, 0, time.Local)},
		Status:    "allowed",
		UpdatedAt: now,
	}, now, leftW)
	got := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"5 Std", " 26%", "↺ 14:30", "7 Tage", " 58%", "↺ Fr 10:00", "in 2 Std 30 Min"} {
		if !strings.Contains(got, want) {
			t.Errorf("usage missing %q:\n%s", want, got)
		}
	}
}

func TestLongResultIsClippedAndExpands(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 100, Height: 40})
	long := strings.Repeat("zeile\n", 30) + "ende"
	a.cur = core.Session{Messages: []core.Message{
		{Kind: core.KindTool, Tool: "Bash", Text: "Bash ls", Detail: "$ ls"},
		{Kind: core.KindResult, Text: "zeile (+30 Zeilen)", Detail: long},
	}}
	a.renderMessages()
	if out := ansi.Strip(a.vp.View()); strings.Contains(out, "ende") || !strings.Contains(out, "… +27 Zeilen") {
		t.Fatalf("not clipped:\n%s", out)
	}
	a.focus, a.msgCursor = focusMessages, 1
	a.toggleExpanded()
	a.vp.GotoBottom()
	if out := ansi.Strip(a.vp.View()); !strings.Contains(out, "ende") {
		t.Fatalf("not expanded:\n%s", out)
	}
}

func ansiStrip(s string) string { return ansi.Strip(s) }

// collect runs a (batch) command and returns the resulting messages, skipping
// commands that block (like waitFS) after a short timeout.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(time.Second):
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collect(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

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
	// A save from the IDE reaches the open preview via the file watcher.
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n// aus der IDE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-a.fsEvents:
		cmd := a.onFilesChanged(d)
		for _, m := range collect(cmd) {
			if _, ok := m.(fsMsg); !ok {
				send(t, a, m)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not report the change")
	}
	if !strings.Contains(a.overlay.View(), "aus der IDE") {
		t.Fatalf("preview not updated:\n%s", a.overlay.View())
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
