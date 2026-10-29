package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// streamEvent is one line of `claude --output-format stream-json`; transcript
// lines in ~/.claude/projects share the same shape for user/assistant entries.
type streamEvent struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	UUID      string          `json:"uuid"`
	Message   json.RawMessage `json:"message"`
	TotalCost float64         `json:"total_cost_usd"`
	IsError   bool            `json:"is_error"`
	Result    string          `json:"result"`
	// Live streaming (--include-partial-messages).
	Event           *apiEvent `json:"event"`
	EstimatedTokens int       `json:"estimated_tokens"`
}

// apiEvent is the raw Messages API stream event inside a "stream_event".
type apiEvent struct {
	Type         string       `json:"type"`
	ContentBlock contentBlock `json:"content_block"`
	Delta        struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

// messagesFrom converts a user/assistant event into displayable messages.
func messagesFrom(ev streamEvent) []Message {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(ev.Message, &m) != nil || len(m.Content) == 0 {
		return nil
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		if ev.Type == "user" && !strings.HasPrefix(strings.TrimSpace(text), "<") {
			return []Message{{UUID: ev.UUID, Kind: KindUser, Text: text}}
		}
		return nil
	}
	var blocks []contentBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []Message
	for _, b := range blocks {
		switch {
		case b.Type == "text" && ev.Type == "assistant":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, Message{UUID: ev.UUID, Kind: KindAssistant, Text: b.Text})
			}
		case b.Type == "text" && ev.Type == "user":
			if t := strings.TrimSpace(b.Text); t != "" && !strings.HasPrefix(t, "<") {
				out = append(out, Message{UUID: ev.UUID, Kind: KindUser, Text: b.Text})
			}
		case b.Type == "tool_use":
			out = append(out, Message{UUID: ev.UUID, Kind: KindTool, Text: toolSummary(b), Tool: b.Name, File: toolFile(b)})
		case b.Type == "tool_result":
			out = append(out, Message{UUID: ev.UUID, Kind: KindResult, Text: resultSummary(b)})
		}
	}
	return out
}

// resultSummary condenses a tool result to its first line plus a line count.
func resultSummary(b contentBlock) string {
	var text string
	if json.Unmarshal(b.Content, &text) != nil {
		var parts []contentBlock
		_ = json.Unmarshal(b.Content, &parts)
		for _, p := range parts {
			text += p.Text
		}
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	s := excerpt(lines[0], 100)
	if len(lines) > 1 {
		s += fmt.Sprintf(" (+%d Zeilen)", len(lines)-1)
	}
	if b.IsError {
		s = "Fehler: " + s
	}
	if s == "" {
		s = "(leer)"
	}
	return s
}

// toolFile returns the file a file tool (Read, Edit, Write, …) worked on.
func toolFile(b contentBlock) string {
	switch b.Name {
	case "Read", "Edit", "MultiEdit", "Write", "NotebookEdit":
	default:
		return ""
	}
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	_ = json.Unmarshal(b.Input, &in)
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.NotebookPath
}

func toolSummary(b contentBlock) string {
	var in map[string]any
	_ = json.Unmarshal(b.Input, &in)
	for _, key := range []string{"file_path", "command", "pattern", "path", "url", "description"} {
		if v, ok := in[key].(string); ok && v != "" {
			return fmt.Sprintf("%s %s", b.Name, excerpt(v, 80))
		}
	}
	return b.Name
}
