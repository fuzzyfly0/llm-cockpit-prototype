package core

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeHome points the user home (and thus ~/.claude) to a temp folder.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	return home
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestObserveFollowsTranscript(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".claude", "projects", "C--work-shop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "abc-123.jsonl")
	appendLines(t, transcript,
		`{"type":"user","uuid":"u1","cwd":"C:/work/shop","message":{"role":"user","content":"Fix the cart"}}`)

	// The session is found as an external one and followed.
	m, _ := NewManagerAt("")
	ext := m.ExternalSessions(time.Hour)
	if len(ext) != 1 || ext[0].ClaudeID != "abc-123" || ext[0].Dir != "C:/work/shop" || ext[0].LastPrompt != "Fix the cart" {
		t.Fatalf("external sessions %+v", ext)
	}
	id := m.AddObserved(ext[0])
	defer m.StopAll()
	if len(m.ExternalSessions(time.Hour)) != 0 {
		t.Fatal("a followed session is still listed as external")
	}
	get := func() Session { s, _ := m.Get(id); return s }
	waitFor(t, "the prompt", func() bool { s := get(); return len(s.Messages) == 1 && s.Status == StatusWorking })

	// A tool call, its result and the final answer arrive live.
	appendLines(t, transcript,
		`{"type":"assistant","uuid":"a1","message":{"model":"claude-opus-5-5","stop_reason":"tool_use","usage":{"input_tokens":4,"cache_read_input_tokens":20000},"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","uuid":"u2","message":{"content":[{"type":"tool_result","content":"ok"}]}}`)
	waitFor(t, "the tool call", func() bool { s := get(); return len(s.Messages) == 3 && s.Activity == "⚙ Bash go test ./..." })
	if s := get(); s.ContextTokens != 20004 || s.ActiveModel != "claude-opus-5-5" {
		t.Fatalf("context %d model %q", s.ContextTokens, s.ActiveModel)
	}
	// A half-written line is only read once it is complete.
	f, _ := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`{"type":"assistant","uuid":"a2","message":{"stop_reason":"end_turn",`)
	f.Close()
	time.Sleep(2 * observeInterval)
	if len(get().Messages) != 3 {
		t.Fatal("incomplete line was parsed")
	}
	appendLines(t, transcript, `"content":[{"type":"text","text":"Fixed, tests pass."}]}}`)
	waitFor(t, "the answer", func() bool { s := get(); return len(s.Messages) == 4 && s.Status == StatusIdle })

	// Messages are written in the Claude window, not from the cockpit.
	if err := m.Send(id, "more"); !errors.Is(err, ErrObserved) {
		t.Fatalf("send to an observed session: %v", err)
	}
	// Take over: the session is a normal cockpit session again.
	m.TakeOver(id)
	if s := get(); s.Observe || s.Status != StatusStopped {
		t.Fatalf("after takeover: %+v", s)
	}
}

func TestObservedSessionsAreFollowedAfterRestart(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(home, ".claude", "projects", "p")
	_ = os.MkdirAll(dir, 0o755)
	appendLines(t, filepath.Join(dir, "x1.jsonl"),
		`{"type":"user","uuid":"u1","cwd":"C:/p","message":{"role":"user","content":"Hello"}}`,
		`{"type":"assistant","uuid":"a1","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"Hi"}]}}`)
	state := filepath.Join(t.TempDir(), "state.json")
	first, _ := NewManagerAt(state)
	first.AddObserved(ExternalSession{ClaudeID: "x1", Dir: "C:/p"})
	first.StopAll()

	m, _ := NewManagerAt(state)
	defer m.StopAll()
	id := m.List()[0].ID
	waitFor(t, "the history after restart", func() bool {
		s, _ := m.Get(id)
		return s.Observe && len(s.Messages) == 2 && s.Status == StatusIdle
	})
}
