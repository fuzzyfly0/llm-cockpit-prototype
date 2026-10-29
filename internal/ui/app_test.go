package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
