package core

import (
	"os"
	"testing"
	"time"
)

// TestLiveThinking checks that the thinking text arrives (thinking_display
// "summarized" is requested when the process starts).
func TestLiveThinking(t *testing.T) {
	if os.Getenv("COCKPIT_LIVE") == "" {
		t.Skip("set COCKPIT_LIVE=1 to run against the real claude CLI")
	}
	m, _ := NewManagerAt("")
	id, err := m.Create("think", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	m.SetModel(id, "claude-haiku-5-5")
	m.SetEffort(id, "high")
	if err := m.Send(id, "Think carefully: a train leaves at 9:40 and travels 2 h 35 min plus 3 stops of 4 min each. When does it arrive? One sentence."); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := m.Get(id)
		if s.Status == StatusIdle && s.Preview() != "" {
			for _, msg := range s.Messages {
				if msg.Kind == KindThinking {
					t.Logf("thinking %q: %.200q", msg.Text, msg.Detail)
					if msg.Detail == "" || msg.Partial {
						t.Errorf("thinking without text or still partial: %+v", msg)
					}
					return
				}
			}
			t.Fatalf("no thinking block: %+v", s.Messages)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("no answer within timeout")
}

// TestLiveSession talks to the real Claude CLI. Run with COCKPIT_LIVE=1.
func TestLiveSession(t *testing.T) {
	if os.Getenv("COCKPIT_LIVE") == "" {
		t.Skip("set COCKPIT_LIVE=1 to run against the real claude CLI")
	}
	m, _ := NewManagerAt("")
	id, err := m.Create("live", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	// Changing settings restarts the waiting session with the new options.
	if msg := m.SetModel(id, "claude-haiku-5-5"); msg != "session restarted with the new setting" {
		t.Fatalf("SetModel: %s", msg)
	}
	if msg := m.SetEffort(id, "low"); msg != "session restarted with the new setting" {
		t.Fatalf("SetEffort: %s", msg)
	}
	if err := m.Send(id, "Reply with the single word: Pong"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := m.Get(id)
		if s.Status == StatusIdle && s.Preview() != "" {
			t.Logf("preview=%q claude_id=%s cost=$%.4f model=%s context=%d/%d limits=%+v",
				s.Preview(), s.ClaudeID, s.CostUSD, s.ActiveModel, s.ContextTokens, s.ContextWindow, m.Limits())
			if s.ActiveModel != "claude-haiku-5-5" {
				t.Errorf("model %q", s.ActiveModel)
			}
			if s.ContextTokens == 0 || s.ContextWindow == 0 {
				t.Error("context not captured")
			}
			if m.Limits().UpdatedAt.IsZero() {
				t.Error("no rate limit info received")
			}
			if s.ClaudeID == "" {
				t.Error("no claude session id captured")
			}
			for _, msg := range s.Messages {
				if msg.Partial || (msg.Kind == KindAssistant && msg.UUID == "") {
					t.Errorf("unfinished streamed message left: %+v", msg)
				}
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
