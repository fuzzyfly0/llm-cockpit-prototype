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
		{"thinking shows its duration",
			`{"type":"assistant","uuid":"u2","thinking_duration_ms":3200,"message":{"content":[{"type":"thinking","thinking":""}]}}`,
			[]Message{{UUID: "u2", Kind: KindThinking, Text: "thought (3.2 s)"}}},
		{"tool use",
			`{"type":"assistant","uuid":"u3","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`,
			[]Message{{UUID: "u3", Kind: KindTool, Text: "Read main.go", Tool: "Read", File: "main.go", Detail: "file_path: main.go"}}},
		{"edit becomes a diff",
			`{"type":"assistant","uuid":"u7","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"C:/p/main.go","old_string":"a\nb","new_string":"c"}}]}}`,
			[]Message{{UUID: "u7", Kind: KindTool, Text: "Edit C:/p/main.go", Tool: "Edit", File: "C:/p/main.go", Detail: "- a\n- b\n+ c"}}},
		{"bash shows the command",
			`{"type":"assistant","uuid":"u8","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls","description":"Liste"}}]}}`,
			[]Message{{UUID: "u8", Kind: KindTool, Text: "Bash ls", Tool: "Bash", Detail: "# Liste\n$ ls"}}},
		{"todo list",
			`{"type":"assistant","uuid":"u9","message":{"content":[{"type":"tool_use","name":"TodoWrite","input":{"todos":[{"content":"A","status":"completed"},{"content":"B","status":"in_progress"},{"content":"C","status":"pending"}]}}]}}`,
			[]Message{{UUID: "u9", Kind: KindTool, Text: "TodoWrite", Tool: "TodoWrite", Detail: "[x] A\n[~] B\n[ ] C"}}},
		{"subagent output is marked",
			`{"type":"assistant","uuid":"u10","parent_tool_use_id":"toolu_1","message":{"content":[{"type":"text","text":"sub"}]}}`,
			[]Message{{UUID: "u10", Kind: KindAssistant, Text: "sub", Sub: true}}},
		{"user string",
			`{"type":"user","uuid":"u4","message":{"role":"user","content":"Hallo"}}`,
			[]Message{{UUID: "u4", Kind: KindUser, Text: "Hallo"}}},
		{"tool result keeps the full output",
			`{"type":"user","uuid":"u5","message":{"content":[{"type":"tool_result","content":"a.go\nb.go\nc.go"}]}}`,
			[]Message{{UUID: "u5", Kind: KindResult, Text: "a.go (+2 lines)", Detail: "a.go\nb.go\nc.go"}}},
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

// Edit results carry a structuredPatch that becomes a numbered diff.
func TestEditResultBecomesNumberedDiff(t *testing.T) {
	ev := parse(t, `{"type":"user","uuid":"r1","message":{"content":[{"type":"tool_result","content":"The file x.go has been updated."}]},
		"tool_use_result":{"structuredPatch":[{"oldStart":21,"newStart":21,"lines":[" func Total() {","-\treturn 0","+\tsum := 0","+\treturn sum"," }"]}]}}`)
	msgs := messagesFrom(ev)
	if len(msgs) != 1 || !msgs[0].Diff {
		t.Fatalf("got %+v", msgs)
	}
	if msgs[0].Text != "2 lines added, 1 removed" {
		t.Errorf("summary %q", msgs[0].Text)
	}
	want := "   21    func Total() {\n   22 -  \treturn 0\n   22 +  \tsum := 0\n   23 +  \treturn sum\n   24    }"
	if msgs[0].Detail != want {
		t.Errorf("diff:\n%s\nwant:\n%s", msgs[0].Detail, want)
	}
}

// Recorded sequence with thinking_display "summarized".
func TestThinkingIsStreamed(t *testing.T) {
	m := &Manager{st: &state{}}
	s := &Session{partial: -1, partialThink: -1}
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"thinking","thinking":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"Working through "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"the arithmetic"}}}`,
	} {
		m.handle(s, parse(t, line))
	}
	if len(s.Messages) != 1 || s.Messages[0].Detail != "Working through the arithmetic" || !s.Messages[0].Partial {
		t.Fatalf("while thinking: %+v", s.Messages)
	}
	m.handle(s, parse(t, `{"type":"assistant","uuid":"t1","thinking_duration_ms":2500,"message":{"content":[{"type":"thinking","thinking":"Working through the arithmetic: 11:27."}]}}`))
	got := s.Messages[0]
	if len(s.Messages) != 1 || got.Partial || got.Text != "thought (2.5 s)" || got.Detail != "Working through the arithmetic: 11:27." {
		t.Fatalf("after block: %+v", s.Messages)
	}
}

// The result event of a turn adds denials and a summary line.
func TestResultAddsSummaryAndDenials(t *testing.T) {
	m := &Manager{st: &state{}}
	s := &Session{partial: -1}
	m.handle(s, parse(t, `{"type":"result","duration_ms":12300,"num_turns":3,"total_cost_usd":0.01,
		"usage":{"input_tokens":4,"cache_creation_input_tokens":11438,"cache_read_input_tokens":37817,"output_tokens":923,"output_tokens_details":{"thinking_tokens":751}},
		"permission_denials":[{"tool_name":"PowerShell","tool_input":{"command":"go test ./..."}}]}`))
	if len(s.Messages) != 2 {
		t.Fatalf("messages: %+v", s.Messages)
	}
	if d := s.Messages[0]; d.Kind != KindDenied || d.Text != "PowerShell go test ./...  – not allowed by the permission mode" {
		t.Errorf("denial: %+v", d)
	}
	want := "done · 12.3 s · 3 steps · 49k tokens in · 923 out (751 thinking)"
	if sum := s.Messages[1]; sum.Kind != KindTurn || sum.Text != want {
		t.Errorf("summary: %q, want %q", sum.Text, want)
	}
}

func TestInitShownOncePerProcess(t *testing.T) {
	m := &Manager{st: &state{}}
	s := &Session{partial: -1, proc: &process{}}
	line := `{"type":"system","subtype":"init","session_id":"x","model":"claude-haiku-5-5","permissionMode":"acceptEdits","claude_code_version":"2.1.302","mcp_servers":[{"name":"docs","status":"pending"}]}`
	m.handle(s, parse(t, line))
	m.handle(s, parse(t, line))
	if len(s.Messages) != 1 {
		t.Fatalf("init lines: %+v", s.Messages)
	}
	want := "Session ready · model claude-haiku-5-5 · mode acceptEdits · Claude Code 2.1.302 · MCP: docs (pending)"
	if s.Messages[0].Text != want {
		t.Errorf("got %q", s.Messages[0].Text)
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
	want := Config{ClaudeBin: "claude", PermissionMode: "acceptEdits", IDE: "code", ThinkingDisplay: "summarized"}
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
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"In the "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"folder"}}}`,
	} {
		m.handle(s, parse(t, line))
	}
	if len(s.Messages) != 1 || s.Messages[0].Text != "In the folder" || !s.Messages[0].Partial {
		t.Fatalf("while streaming: %+v", s.Messages)
	}
	m.handle(s, parse(t, `{"type":"assistant","uuid":"u9","message":{"content":[{"type":"text","text":"In the folder are 12 files."}]}}`))
	want := Message{UUID: "u9", Kind: KindAssistant, Text: "In the folder are 12 files."}
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
