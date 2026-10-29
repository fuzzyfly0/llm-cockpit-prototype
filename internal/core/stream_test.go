package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func parse(t *testing.T, line string) streamEvent {
	t.Helper()
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestMessagesFrom(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []Message
	}{
		{"assistant text",
			`{"type":"assistant","uuid":"u1","message":{"content":[{"type":"text","text":"Eins"}]}}`,
			[]Message{{UUID: "u1", Kind: KindAssistant, Text: "Eins"}}},
		{"thinking only is skipped",
			`{"type":"assistant","uuid":"u2","message":{"content":[{"type":"thinking","thinking":""}]}}`,
			nil},
		{"tool use",
			`{"type":"assistant","uuid":"u3","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`,
			[]Message{{UUID: "u3", Kind: KindTool, Text: "Read main.go"}}},
		{"user string",
			`{"type":"user","uuid":"u4","message":{"role":"user","content":"Hallo"}}`,
			[]Message{{UUID: "u4", Kind: KindUser, Text: "Hallo"}}},
		{"tool result is summarized",
			`{"type":"user","uuid":"u5","message":{"content":[{"type":"tool_result","content":"a.go\nb.go\nc.go"}]}}`,
			[]Message{{UUID: "u5", Kind: KindResult, Text: "a.go (+2 Zeilen)"}}},
		{"system reminder is skipped",
			`{"type":"user","uuid":"u6","message":{"content":"<system-reminder>x</system-reminder>"}}`,
			nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messagesFrom(parse(t, tt.line))
			if len(got) != len(tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got %+v, want %+v", got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLoadStateFillsEmptyConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(p, []byte(`{"config":{"claude_bin":"","ide":"code"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(p)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{ClaudeBin: "claude", PermissionMode: "acceptEdits", IDE: "code"}
	if !reflect.DeepEqual(st.Config, want) {
		t.Fatalf("got %+v, want %+v", st.Config, want)
	}
}

func TestLoadStateMissingFileIsNotAnError(t *testing.T) {
	st, err := loadState(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || st.Config.ClaudeBin != "claude" {
		t.Fatalf("got %+v, %v", st.Config, err)
	}
}

// Regression: change notifications used to call into the UI synchronously,
// deadlocking when triggered from the UI loop (e.g. creating a session).
func TestChangedDoesNotBlockWithoutReader(t *testing.T) {
	m := &Manager{st: &state{}, notify: make(chan struct{}, 1)}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			m.changed()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("changed() blocked")
	}
}

// Replays the real event sequence recorded with --include-partial-messages.
func TestStreamingReplacesPartialText(t *testing.T) {
	m := &Manager{st: &state{}}
	s := &Session{partial: -1}
	for _, line := range []string{
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":50}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Im "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Ordner"}}}`,
	} {
		m.handle(s, parse(t, line))
	}
	if len(s.Messages) != 1 || s.Messages[0].Text != "Im Ordner" || !s.Messages[0].Partial {
		t.Fatalf("while streaming: %+v", s.Messages)
	}
	m.handle(s, parse(t, `{"type":"assistant","uuid":"u9","message":{"content":[{"type":"text","text":"Im Ordner liegen 12 Dateien."}]}}`))
	want := Message{UUID: "u9", Kind: KindAssistant, Text: "Im Ordner liegen 12 Dateien."}
	if len(s.Messages) != 1 || s.Messages[0] != want {
		t.Fatalf("after final block: %+v", s.Messages)
	}
	if s.Status != StatusWorking {
		t.Fatalf("status %v", s.Status)
	}
}

func TestHandleResultAccumulatesCostPerProcess(t *testing.T) {
	m := &Manager{st: &state{}}
	s := &Session{CostUSD: 1, costBase: 1}
	m.handle(s, streamEvent{Type: "result", TotalCost: 0.25})
	if s.CostUSD != 1.25 || s.Status != StatusIdle || !s.Unread {
		t.Fatalf("unexpected session state: %+v", s)
	}
}
