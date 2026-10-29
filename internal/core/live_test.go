package core

import (
	"os"
	"testing"
	"time"
)

// TestLiveSession talks to the real Claude CLI. Run with COCKPIT_LIVE=1.
func TestLiveSession(t *testing.T) {
	if os.Getenv("COCKPIT_LIVE") == "" {
		t.Skip("set COCKPIT_LIVE=1 to run against the real claude CLI")
	}
	m := &Manager{st: &state{}, notify: make(chan struct{}, 1)}
	applyDefaults(&m.st.Config)
	m.st.Config.ExtraArgs = []string{"--model", "haiku"}

	id, err := m.Create("live", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	if err := m.Send(id, "Antworte nur mit dem Wort: Pong"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := m.Get(id)
		if s.Status == StatusIdle && s.Preview() != "" {
			t.Logf("preview=%q claude_id=%s cost=$%.4f", s.Preview(), s.ClaudeID, s.CostUSD)
			if s.ClaudeID == "" {
				t.Error("no claude session id captured")
			}
			return
		}
		if s.Status == StatusError {
			t.Fatalf("session error: %+v", s.Messages)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no answer within timeout")
}
