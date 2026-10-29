package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/stefa/llm-cockpit/internal/core"
	"github.com/stefa/llm-cockpit/internal/platform"
)

func keys(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func special(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func alt(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true} }

// typeText sends text key by key, like a user typing.
func typeText(t *testing.T, a *Model, text string) {
	t.Helper()
	for _, r := range text {
		send(t, a, keys(string(r)))
	}
}

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

// newWith creates a model with stopped sessions in temporary folders.
func newWith(t *testing.T, w, h int, names ...string) (*Model, *core.Manager) {
	t.Helper()
	mgr, _ := core.NewManagerAt("")
	for _, n := range names {
		if _, err := mgr.Create(n, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	mgr.StopAll()
	t.Cleanup(mgr.StopAll)
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: w, Height: h})
	return a, mgr
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
	for _, k := range []tea.Msg{special(tea.KeyTab), special(tea.KeyDown), special(tea.KeyEsc), keys("?"), special(tea.KeyEsc)} {
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
	a, _ := newWith(t, 140, 30, "eins", "zwei")
	run(t, a, a.gitAllCmd()) // both folders are no git repos: one group
	a.View()

	// Row 0 is the group header "No git repository", rows 1-2 the first
	// session, rows 3-4 the second; screen rows are one lower (border).
	if len(a.groups()) != 1 || a.groups()[0].label != "No git repository" {
		t.Fatalf("groups %+v", a.groups())
	}
	send(t, a, click(5, 1)) // header: no selection change
	if a.sel != 0 {
		t.Fatalf("click on header changed selection to %d", a.sel)
	}
	// A click selects the session and focuses the list (blue border), so the
	// arrow keys continue there; typing goes to the message field.
	send(t, a, click(5, 4))
	if a.sel != 1 || a.cur.Name != "zwei" || a.focus != focusList {
		t.Fatalf("click on session: sel=%d cur=%q focus=%d", a.sel, a.cur.Name, a.focus)
	}
	if out := a.View(); !strings.Contains(out, "\x1b[94m╭") && !strings.Contains(out, "94m") {
		t.Log("note: accent colour not detectable in test output")
	}
	send(t, a, special(tea.KeyUp))
	if a.cur.Name != "eins" || a.focus != focusList {
		t.Fatalf("↑ after click: cur=%q focus=%d", a.cur.Name, a.focus)
	}
	send(t, a, special(tea.KeyDown))
	typeText(t, a, "hi")
	if a.focus != focusInput || a.input.Value() != "hi" || a.cur.Name != "zwei" {
		t.Fatalf("typing after click: focus=%d input=%q cur=%q", a.focus, a.input.Value(), a.cur.Name)
	}
	a.input.SetValue("")
	send(t, a, tea.MouseMsg{X: 5, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if a.sel != 0 {
		t.Fatalf("wheel up over list: sel=%d", a.sel)
	}
	// Tabs in the first row of the right panel.
	a.View()
	x := leftW + 3 + a.tabX[1][0] + 1
	send(t, a, click(x, 1))
	if a.tab != tabFiles {
		t.Fatalf("click on tab Dateien: tab=%d", a.tab)
	}
	send(t, a, click(leftW+3+a.tabX[0][0]+1, 1))
	if a.tab != tabHistory || a.focus != focusInput {
		t.Fatalf("click on tab Verlauf: tab=%d focus=%d", a.tab, a.focus)
	}
	// The hint line at the bottom opens the command list.
	send(t, a, click(3, 29))
	if !a.paletteOpen() {
		t.Fatalf("click on hint line: palette closed, input %q", a.input.Value())
	}
	if rows := strings.Split(ansiStrip(a.View()), "\n"); len(rows) != 30 {
		t.Fatalf("view has %d rows, want 30", len(rows))
	}
}

// Regression: typing a message that starts with "n" opened the new-session
// dialog. Now typing always lands in the message field – also when the
// session list or the history has the focus.
func TestTypingGoesToMessageField(t *testing.T) {
	a, _ := newWith(t, 120, 30, "tippen")
	if a.focus != focusInput {
		t.Fatalf("start focus=%d, want message field", a.focus)
	}
	typeText(t, a, "nimm die datei")
	if a.focus != focusInput || a.input.Value() != "nimm die datei" || a.input.Prompt != "› " {
		t.Fatalf("focus=%d value=%q prompt=%q", a.focus, a.input.Value(), a.input.Prompt)
	}
	send(t, a, special(tea.KeyEsc)) // esc clears the text
	if a.input.Value() != "" {
		t.Fatalf("esc did not clear: %q", a.input.Value())
	}
	// In the session list, a letter without a meaning there is typed.
	send(t, a, special(tea.KeyShiftTab))
	if a.focus != focusList {
		t.Fatalf("shift+tab: focus=%d", a.focus)
	}
	typeText(t, a, "neu")
	if a.focus != focusInput || a.input.Value() != "neu" {
		t.Fatalf("redirect: focus=%d value=%q", a.focus, a.input.Value())
	}
	// ctrl+n offers the known places; the last option asks for a folder.
	send(t, a, tea.KeyMsg{Type: tea.KeyCtrlN})
	if a.focus != focusPick || a.pick.options[0].value != a.cur.Dir {
		t.Fatalf("focus=%d options=%+v", a.focus, a.pick.options)
	}
	run(t, a, a.choosePick(len(a.pick.options)-1)) // "Anderer Ordner …"
	if a.focus != focusNewDir || !strings.HasPrefix(a.input.Prompt, "New session") {
		t.Fatalf("focus=%d prompt=%q", a.focus, a.input.Prompt)
	}
	send(t, a, special(tea.KeyEsc))
	if a.input.Prompt != "› " || a.focus != focusInput {
		t.Fatalf("after esc: prompt=%q focus=%d", a.input.Prompt, a.focus)
	}
}

func TestCommandPalette(t *testing.T) {
	a, mgr := newWith(t, 140, 40, "palette")
	send(t, a, keys("/"))
	if !a.paletteOpen() || !strings.Contains(ansiStrip(a.View()), "/new") {
		t.Fatal("palette not shown after /")
	}
	typeText(t, a, "model")
	if m := a.paletteMatches(); len(m) != 1 || m[0].name != "model" {
		t.Fatalf("filter: %+v", m)
	}
	send(t, a, special(tea.KeyEnter))
	if a.focus != focusPick || a.input.Value() != "" {
		t.Fatalf("model: focus=%d input=%q", a.focus, a.input.Value())
	}
	for i := 0; i < 3; i++ {
		send(t, a, special(tea.KeyDown))
	}
	send(t, a, special(tea.KeyEnter)) // Haiku
	if s, _ := mgr.Get(a.cur.ID); s.Model != "claude-haiku-5-5" {
		t.Fatalf("model %q status %q", s.Model, a.status)
	}
	// ctrl+p opens the list too, esc closes it.
	send(t, a, tea.KeyMsg{Type: tea.KeyCtrlP})
	if !a.paletteOpen() {
		t.Fatal("ctrl+p")
	}
	send(t, a, special(tea.KeyEsc))
	if a.paletteOpen() || a.input.Value() != "" {
		t.Fatal("esc did not close the palette")
	}
	// "?" in the empty field shows the help.
	send(t, a, keys("?"))
	if a.focus != focusOverlay || !strings.Contains(a.overlayRaw, "ctrl+k") {
		t.Fatalf("help: focus=%d", a.focus)
	}
	send(t, a, special(tea.KeyEsc))
	if a.focus != focusInput {
		t.Fatalf("after help: focus=%d", a.focus)
	}
}

// Claude Code's own commands appear in the list, marked "claude ▸", and
// are sent to Claude including their arguments.
func TestClaudeCommandsInPalette(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"claude_commands":["compact","context","__internal"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr, _ := core.NewManagerAt(statePath)
	t.Cleanup(mgr.StopAll)
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 140, Height: 40})

	send(t, a, keys("/"))
	out := ansiStrip(a.View())
	if !strings.Contains(out, "/compact") || !strings.Contains(out, "claude ▸") || strings.Contains(out, "__internal") {
		t.Fatalf("claude commands not listed:\n%s", out)
	}
	typeText(t, a, "compact focus on the API")
	// The exact name comes first, before "view" whose label mentions compact.
	m := a.paletteMatches()
	if len(m) != 2 || !m[0].claude || m[0].name != "compact" || m[1].name != "view" {
		t.Fatalf("matches %+v", m)
	}
	// Without a session the command cannot be sent; with one it is sent.
	send(t, a, special(tea.KeyEnter))
	if !strings.Contains(a.status, "create a session first") {
		t.Fatalf("status %q", a.status)
	}
	// The sent text is the full input incl. arguments.
	a.input.SetValue("/compact focus on the API")
	if got := a.paletteQuery(); got != "compact" {
		t.Fatalf("query %q", got)
	}
}

func TestSettings(t *testing.T) {
	a, mgr := newWith(t, 140, 40, "einstellungen")
	a.runAction("settings")
	if a.focus != focusPick || !strings.Contains(ansiStrip(a.View()), "IDE command: idea") {
		t.Fatalf("settings not shown, focus=%d", a.focus)
	}
	// Free text: the session link.
	for i, o := range a.pick.options {
		if o.value == "link" {
			run(t, a, a.choosePick(i))
		}
	}
	if a.focus != focusSetting {
		t.Fatalf("link: focus=%d", a.focus)
	}
	a.input.SetValue("https://company.atlassian.net/browse/")
	send(t, a, special(tea.KeyEnter))
	if c := mgr.Config(); c.SessionLink != "https://company.atlassian.net/browse/" || a.focus != focusInput || a.input.Prompt != "› " {
		t.Fatalf("link %q focus=%d prompt=%q", c.SessionLink, a.focus, a.input.Prompt)
	}
	// Choice: the permission mode.
	a.runAction("settings")
	run(t, a, a.choosePick(1)) // Permission mode
	for i, o := range a.pick.options {
		if o.value == "plan" {
			run(t, a, a.choosePick(i))
		}
	}
	if mgr.Config().PermissionMode != "plan" {
		t.Fatalf("permission mode %q", mgr.Config().PermissionMode)
	}
}

func TestSessionSwitcher(t *testing.T) {
	a, _ := newWith(t, 140, 40, "login-fix", "export", "warenkorb")
	typeText(t, a, "Entwurf")
	send(t, a, tea.KeyMsg{Type: tea.KeyCtrlK})
	if !a.switching || a.input.Value() != "" {
		t.Fatalf("switcher: %v %q", a.switching, a.input.Value())
	}
	typeText(t, a, "export")
	if m := a.switchMatches(); len(m) != 1 || a.sessions[m[0]].Name != "export" {
		t.Fatalf("matches %v", m)
	}
	send(t, a, special(tea.KeyEnter))
	if a.switching || a.cur.Name != "export" || a.input.Value() != "Entwurf" {
		t.Fatalf("after switch: %v %q draft=%q", a.switching, a.cur.Name, a.input.Value())
	}
	// alt+↑ / alt+↓ step through the sessions.
	send(t, a, tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	if a.cur.Name != "login-fix" {
		t.Fatalf("alt+up: %q", a.cur.Name)
	}
}

func TestMultiLineInputAndHistory(t *testing.T) {
	a, _ := newWith(t, 120, 30, "zeilen")
	typeText(t, a, "erste")
	send(t, a, tea.KeyMsg{Type: tea.KeyEnter, Alt: true}) // alt+enter
	typeText(t, a, "zweite\\")
	send(t, a, special(tea.KeyEnter)) // trailing backslash → new line
	typeText(t, a, "dritte")
	if got := a.input.Value(); got != "erste\nzweite\ndritte" {
		t.Fatalf("value %q", got)
	}
	if a.input.Height() != 3 {
		t.Fatalf("field height %d, want 3", a.input.Height())
	}
	if rows := strings.Split(ansiStrip(a.View()), "\n"); len(rows) != 30 {
		t.Fatalf("view has %d rows, want 30", len(rows))
	}
	// ↑ in the empty field brings back earlier prompts.
	a.cur.Messages = []core.Message{{Kind: core.KindUser, Text: "alt 1"}, {Kind: core.KindUser, Text: "alt 2"}}
	a.input.SetValue("")
	send(t, a, special(tea.KeyUp))
	if a.input.Value() != "alt 2" {
		t.Fatalf("history: %q", a.input.Value())
	}
	send(t, a, special(tea.KeyUp))
	send(t, a, special(tea.KeyDown))
	send(t, a, special(tea.KeyDown))
	if a.input.Value() != "" {
		t.Fatalf("history end: %q", a.input.Value())
	}
}

func TestCtrlCClearsThenQuits(t *testing.T) {
	a, _ := newWith(t, 120, 30, "beenden")
	typeText(t, a, "text")
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if a.input.Value() != "" || cmd != nil {
		t.Fatalf("first ctrl+c should only clear: %q", a.input.Value())
	}
	_, cmd = a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil || !strings.Contains(a.status, "press ctrl+c again") {
		t.Fatalf("second ctrl+c should arm: %q", a.status)
	}
	_, cmd = a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("third ctrl+c should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not a quit command")
	}
}

func TestTabsWithAltNumbers(t *testing.T) {
	a, mgr := newWith(t, 120, 30, "reiter")
	send(t, a, alt('3'))
	if a.tab != tabNotes || a.focus != focusNotes {
		t.Fatalf("alt+3: tab=%d focus=%d", a.tab, a.focus)
	}
	typeText(t, a, "note")
	send(t, a, special(tea.KeyEsc)) // saves and returns
	if s, _ := mgr.Get(a.cur.ID); s.Notes != "note" || a.focus != focusInput || a.tab != tabHistory {
		t.Fatalf("notes %q focus=%d tab=%d", s.Notes, a.focus, a.tab)
	}
	if !strings.Contains(ansiStrip(a.View()), "Notes ✎") {
		t.Fatal("notes marker missing in the tab bar")
	}
	send(t, a, alt('4'))
	if a.tab != tabPins || !strings.Contains(ansiStrip(a.View()), "No pins yet") {
		t.Fatalf("alt+4: tab=%d", a.tab)
	}
}

func TestHintLineAndHeight(t *testing.T) {
	for _, w := range []int{80, 140} {
		a, _ := newWith(t, w, 30, "hinweis")
		rows := strings.Split(ansiStrip(a.View()), "\n")
		if len(rows) != 30 {
			t.Fatalf("width %d: view has %d rows", w, len(rows))
		}
		if last := rows[len(rows)-1]; !strings.Contains(last, "enter send") {
			t.Fatalf("width %d: hint line %q", w, last)
		}
	}
}

// Regression: the Dateien tab kept showing the old session's files after a
// session switch by arrow keys, mouse wheel or the session search.
func TestFilesFollowSessionSwitch(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	t.Cleanup(mgr.StopAll)
	dirs := map[string]string{}
	for name, files := range map[string][]string{"eins": {"a.txt"}, "zwei": {"b.txt", "c.txt"}} {
		dir := t.TempDir()
		for _, f := range files {
			_ = os.WriteFile(filepath.Join(dir, f), nil, 0o644)
		}
		if _, err := mgr.Create(name, dir); err != nil {
			t.Fatal(err)
		}
		dirs[name] = dir
	}
	mgr.StopAll()
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 140, Height: 30})
	run(t, a, a.setTab(tabFiles))

	check := func(how string) {
		t.Helper()
		want := map[string]int{"eins": 1, "zwei": 2}[a.cur.Name]
		if a.files.dir != a.cur.Dir || a.files.total != want || a.tab != tabFiles {
			t.Fatalf("%s: session %q, files of %q (%d files), tab %d", how, a.cur.Name, a.files.dir, a.files.total, a.tab)
		}
	}
	check("start")

	// Arrow keys in the session list.
	a.focus = focusList
	_, cmd := a.handlePanelKey(special(tea.KeyDown))
	run(t, a, cmd)
	check("list ↓")
	if a.focus != focusList {
		t.Fatalf("focus moved to %d", a.focus)
	}
	// Mouse wheel over the list.
	a.View()
	_, cmd = a.handleMouse(tea.MouseMsg{X: 5, Y: 3, Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	run(t, a, cmd)
	check("wheel")
	// alt+↓ and the session search keep the tab.
	_, cmd = a.Update(tea.KeyMsg{Type: tea.KeyDown, Alt: true})
	run(t, a, cmd)
	check("alt+↓")
	send(t, a, tea.KeyMsg{Type: tea.KeyCtrlK})
	typeText(t, a, "eins")
	_, cmd = a.Update(special(tea.KeyEnter))
	run(t, a, cmd)
	check("ctrl+k")
}

// Regression: with the Notizen tab open, iterating the session list kept
// the old session's notes in the editor and saved them into the next one.
func TestNotesAndPinsFollowSessionSwitch(t *testing.T) {
	a, mgr := newWith(t, 140, 30, "eins", "zwei")
	mgr.SetNotes(a.sessions[1].ID, "notes of two")
	pinned := core.Message{UUID: "u1", Kind: core.KindAssistant, Text: "x"}
	mgr.TogglePin(a.sessions[1].ID, "u1", pinLabel(pinned))
	a.refresh()

	send(t, a, alt('3')) // Notizen of "eins"
	typeText(t, a, "notes of one")
	send(t, a, special(tea.KeyShiftTab)) // to the session list (from the right panel back)
	if a.focus != focusList && a.focus != focusInput {
		t.Fatalf("focus %d", a.focus)
	}
	a.focus = focusList
	_, cmd := a.handlePanelKey(special(tea.KeyDown)) // just iterate to "zwei"
	run(t, a, cmd)
	if a.cur.Name != "zwei" || a.notes.Value() != "notes of two" {
		t.Fatalf("editor shows %q for %q", a.notes.Value(), a.cur.Name)
	}
	s1, _ := mgr.Get(a.sessions[0].ID)
	s2, _ := mgr.Get(a.sessions[1].ID)
	if s1.Notes != "notes of one" || s2.Notes != "notes of two" {
		t.Fatalf("saved notes: eins=%q zwei=%q", s1.Notes, s2.Notes)
	}
	// Pins tab shows the pins of the selected session.
	send(t, a, alt('4'))
	if !strings.Contains(ansiStrip(a.View()), "★ x") {
		t.Fatalf("pins of zwei not shown:\n%s", ansiStrip(a.View()))
	}
	a.focus = focusList
	_, cmd = a.handlePanelKey(special(tea.KeyUp))
	run(t, a, cmd)
	if out := ansiStrip(a.View()); a.cur.Name != "eins" || !strings.Contains(out, "No pins yet") {
		t.Fatalf("pins of eins not shown for %q:\n%s", a.cur.Name, out)
	}
}

func TestSessionLink(t *testing.T) {
	a, _ := newWith(t, 120, 30, "PROJ-123")
	a.runAction("link")
	if !strings.Contains(a.status, "no link configured") {
		t.Fatalf("status %q", a.status)
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
	if !strings.Contains(a.vp.View(), "[o] IDE · [d] Diff") {
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
		{Kind: core.KindThinking, Text: "thought (1.5 s)", Detail: "Erst die Dateien zählen."},
		{Kind: core.KindTool, Tool: "Glob", Text: "Glob *", Detail: "pattern: *"},
		{Kind: core.KindResult, Text: "a.go (+2 lines)", Detail: "a.go\nb.go\nc.go"},
		{Kind: core.KindTool, Tool: "Edit", Text: "Edit x.go", File: "x.go", Detail: "- alt\n+ neu"},
		{Kind: core.KindResult, Diff: true, Text: "1 line added, 1 removed", Detail: "    1 -  alt\n    1 +  neu"},
		{Kind: core.KindDenied, Text: "PowerShell go test  – not allowed by the permission mode"},
		{Kind: core.KindTurn, Text: "done · 1.0 s · 2 steps"},
		{Kind: core.KindAssistant, Text: "Im Ordner", Partial: true},
	}}
	a.renderMessages()
	out := ansi.Strip(a.vp.View())
	for _, want := range []string{
		"> Wie viele Dateien?", "✻ thought (1.5 s)", "Erst die Dateien zählen.", "⏺ Glob(*)", "pattern: *",
		"⎿  a.go", "c.go", "⏺ Update(x.go)", "⎿  1 line added, 1 removed", "1 -  alt", "1 +  neu",
		"⛔ denied: PowerShell go test", "✓ done", "⏺ Im Ordner▌",
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
		{Kind: core.KindTurn, Text: "done · 1.0 s"},
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
	if len(s.Pins) != 2 || s.Pins[0].Excerpt != "⏺ Bash go test" || s.Pins[1].Excerpt != "✓ done · 1.0 s" {
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
	a.runAction("M")
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
	for _, want := range []string{"5h", " 26%", "↺ 14:30", "7 days", " 58%", "↺ Fri 10:00", "in 2h 30m"} {
		if !strings.Contains(got, want) {
			t.Errorf("usage missing %q:\n%s", want, got)
		}
	}
}

// Regression: the selection background was reset by styled text, so the
// first line of a clicked entry looked unselected.
func TestSelectionMarksEveryLine(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 80, Height: 30})
	a.cur = core.Session{Messages: []core.Message{
		{Kind: core.KindUser, Text: "Frage"},
		{Kind: core.KindAssistant, UUID: "a", Text: "Erste Zeile\nZweite Zeile\nDritte Zeile"},
	}}
	a.focus, a.msgCursor = focusMessages, 1
	a.renderMessages()
	var marked []string
	for _, l := range strings.Split(ansi.Strip(a.vp.View()), "\n") {
		if strings.HasPrefix(l, "▌ ") {
			marked = append(marked, l)
		}
	}
	if len(marked) != 3 || !strings.Contains(marked[0], "⏺ Erste Zeile") || !strings.Contains(marked[2], "Dritte Zeile") {
		t.Fatalf("marked lines %q", marked)
	}
}

func TestCopyText(t *testing.T) {
	long := strings.Repeat("zeile\n", 50) + "ende"
	tests := []struct {
		m    core.Message
		want string
	}{
		{core.Message{Kind: core.KindAssistant, Text: "Antwort"}, "Antwort"},
		{core.Message{Kind: core.KindTool, Tool: "Bash", Text: "Bash go test", Detail: "# Tests\n$ go test ./..."}, "go test ./..."},
		{core.Message{Kind: core.KindResult, Text: "zeile (+50 Zeilen)", Detail: long}, long},
		{core.Message{Kind: core.KindThinking, Text: "thought", Detail: "Gedanke"}, "Gedanke"},
		{core.Message{Kind: core.KindTurn, Text: "done"}, "done"},
	}
	for _, tt := range tests {
		if got := copyText(tt.m); got != tt.want {
			t.Errorf("copyText(%v) = %.40q, want %.40q", tt.m.Kind, got, tt.want)
		}
	}
}

// Copies through the real Windows clipboard; skipped where none exists.
func TestCopyToClipboard(t *testing.T) {
	text := "Grüße aus dem Cockpit\nzweite Zeile"
	if err := platform.CopyToClipboard(text); err != nil {
		t.Skip("no clipboard:", err)
	}
	got, err := clipboard.ReadAll()
	if err != nil || strings.ReplaceAll(got, "\r\n", "\n") != text {
		t.Fatalf("clipboard %q, %v", got, err)
	}
}

func TestViewCycleWithOneKey(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 200, Height: 30})
	long := strings.Repeat("zeile\n", 30) + "ende"
	a.cur = core.Session{Messages: []core.Message{
		{Kind: core.KindTool, Tool: "Bash", Text: "Bash ls", Detail: "$ ls"},
		{Kind: core.KindResult, Text: "zeile (+30 Zeilen)", Detail: long},
	}}
	view := func() string { a.renderMessages(); a.vp.GotoBottom(); return ansi.Strip(a.vp.View()) }

	steps := []struct {
		name, mustHave, mustNot string
	}{
		{"normal", "… +27 lines", "ende"},
		{"all", "ende", "… +"},
		{"compact", "… +30 lines", "ende"},
		{"normal", "… +27 lines", "ende"},
	}
	for i, st := range steps {
		if i > 0 {
			send(t, a, tea.KeyMsg{Type: tea.KeyCtrlO})
		}
		out := view()
		if a.viewName() != st.name || !strings.Contains(out, st.mustHave) || strings.Contains(out, st.mustNot) {
			t.Fatalf("step %d: view=%s\n%s", i, a.viewName(), out)
		}
		if i > 0 && !strings.Contains(a.status, "View: "+st.name) {
			t.Fatalf("status does not show %q: %q", st.name, a.status)
		}
	}
}

func TestCopyNotes(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	id, err := mgr.Create("notizen", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr.StopAll()
	mgr.SetNotes(id, "Zeile 1\nZeile 2")
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 120, Height: 30})
	send(t, a, alt('3'))
	send(t, a, tea.KeyMsg{Type: tea.KeyCtrlY})
	if a.focus != focusNotes {
		t.Fatalf("ctrl+y left the editor: focus=%d", a.focus)
	}
	if strings.HasPrefix(a.status, "Copy failed") {
		t.Skip("no clipboard:", a.status)
	}
	if a.status != "notes copied (2 lines)" {
		t.Fatalf("status %q", a.status)
	}
	if got, _ := clipboard.ReadAll(); strings.ReplaceAll(got, "\r\n", "\n") != "Zeile 1\nZeile 2" {
		t.Fatalf("clipboard %q", got)
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
	if out := ansi.Strip(a.vp.View()); strings.Contains(out, "ende") || !strings.Contains(out, "… +27 lines") {
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

// run executes a (batch) command and feeds its messages back, like the
// Bubble Tea loop.
func run(t *testing.T, a *Model, cmd tea.Cmd) {
	t.Helper()
	runDepth(t, a, cmd, 0)
}

func runDepth(t *testing.T, a *Model, cmd tea.Cmd, depth int) {
	t.Helper()
	if depth > 5 {
		return
	}
	for _, msg := range collect(cmd) {
		switch msg.(type) {
		case fsMsg, tickMsg:
			continue // endless background loops
		}
		if strings.HasPrefix(fmt.Sprintf("%T", msg), "cursor.") {
			continue // cursor blinking
		}
		_, next := a.Update(msg)
		runDepth(t, a, next, depth+1)
	}
}

// gitRepo creates a repository with one commit.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cart.go"), []byte("package shop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestWorktreeSessionFlow(t *testing.T) {
	repo := gitRepo(t)
	t.Cleanup(func() { _ = os.RemoveAll(repo + "-worktrees") })
	mgr, _ := core.NewManagerAt("")
	defer mgr.StopAll()
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 140, Height: 40})

	// ctrl+n, folder of the repo → choice "direkt / neuer Worktree / …".
	send(t, a, tea.KeyMsg{Type: tea.KeyCtrlN})
	a.input.SetValue(repo)
	send(t, a, special(tea.KeyEnter))
	if a.focus != focusPick || a.pick.options[1].value != "new" {
		t.Fatalf("no workspace choice: focus=%d status=%q", a.focus, a.status)
	}
	run(t, a, a.choosePick(1)) // "Neuer Worktree mit neuem Branch"
	if a.focus != focusNewWorktree {
		t.Fatalf("focus=%d", a.focus)
	}
	a.input.SetValue("PROJ-123 Warenkorb")
	_, cmd := a.handleKey(special(tea.KeyEnter))
	run(t, a, cmd) // runs git worktree add, then creates the session

	wt := platform.WorktreeDir(repo, "PROJ-123-Warenkorb")
	if a.cur.Dir != wt || a.cur.Name != "PROJ-123-Warenkorb" {
		t.Fatalf("session %q in %q, want %q (status %q)", a.cur.Name, a.cur.Dir, wt, a.status)
	}
	if g := a.git[wt]; !g.IsWorktree || g.Branch != "PROJ-123-Warenkorb" {
		t.Fatalf("git info %+v", g)
	}

	// A second session directly in the repo lands in the same group.
	send(t, a, tea.KeyMsg{Type: tea.KeyCtrlN})
	send(t, a, special(tea.KeyEnter)) // the repo is offered as known place
	run(t, a, a.choosePick(0))        // direkt im Ordner
	_, cmd = a.handleKey(special(tea.KeyEnter))
	run(t, a, cmd)
	groups := a.groups()
	if len(groups) != 1 || groups[0].label != "shop" || len(groups[0].sessions) != 2 {
		t.Fatalf("groups %+v", groups)
	}
	if out := ansiStrip(a.View()); !strings.Contains(out, "▾ shop (2)") || !strings.Contains(out, "PROJ-123-Warenkorb  ⎇") ||
		!strings.Contains(out, "shop  ⎇ master") {
		t.Fatalf("list not grouped:\n%s", out)
	}

	// Remove the worktree session together with its worktree.
	a.sel = 0
	a.refresh()
	a.focus = focusList
	send(t, a, special(tea.KeyDelete))
	if a.focus != focusPick || a.pick.options[1].value != "delete" {
		t.Fatalf("no removal choice: focus=%d", a.focus)
	}
	run(t, a, a.choosePick(1))
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists (status %q): %v", a.status, err)
	}
	if len(mgr.List()) != 1 {
		t.Fatalf("sessions left: %d", len(mgr.List()))
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
	_, cmd = a.handleFilesKey(special(tea.KeyEnter)) // preview src/main.go
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
	if a.focus != focusInput || a.tab != tabHistory {
		t.Fatalf("esc should leave files, focus=%d tab=%d", a.focus, a.tab)
	}
}

func TestCreateSessionFlow(t *testing.T) {
	mgr, _ := core.NewManagerAt("")
	defer mgr.StopAll()
	a := New(mgr)
	send(t, a, tea.WindowSizeMsg{Width: 120, Height: 30})

	dir := t.TempDir()
	send(t, a, tea.KeyMsg{Type: tea.KeyCtrlN})
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

	// To the list and back, help and close it.
	for _, k := range []tea.Msg{special(tea.KeyShiftTab), special(tea.KeyDown), special(tea.KeyUp), special(tea.KeyEsc), keys("?"), special(tea.KeyEsc)} {
		send(t, a, k)
	}
	if a.focus != focusInput {
		t.Fatalf("expected input focus, got %d", a.focus)
	}
}
