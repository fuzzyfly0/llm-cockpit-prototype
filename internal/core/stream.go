package core

import (
	"encoding/json"
	"fmt"
	"sort"
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

	// Subagent output (stream: parent_tool_use_id, transcript: isSidechain).
	ParentToolUseID *string `json:"parent_tool_use_id"`
	IsSidechain     bool    `json:"isSidechain"`
	// Thinking duration (stream / transcript spelling).
	ThinkingMS     int `json:"thinking_duration_ms"`
	ThinkingMSTran int `json:"thinkingDurationMs"`

	// result events
	DurationMS        int      `json:"duration_ms"`
	NumTurns          int      `json:"num_turns"`
	Usage             usage    `json:"usage"`
	PermissionDenials []denial `json:"permission_denials"`
	APIErrorStatus    *int     `json:"api_error_status"`
	// system/init and system/status events
	Model          string      `json:"model"`
	PermissionMode string      `json:"permissionMode"`
	Version        string      `json:"claude_code_version"`
	MCPServers     []mcpServer `json:"mcp_servers"`
	Status         *string     `json:"status"`
	// Structured tool result (stream / transcript spelling), e.g. the
	// structuredPatch of an Edit.
	ToolUseResult     json.RawMessage `json:"tool_use_result"`
	ToolUseResultTran json.RawMessage `json:"toolUseResult"`
	// control_response to our control requests
	Response *struct {
		Subtype string `json:"subtype"`
		Error   string `json:"error"`
	} `json:"response"`
	// rate_limit_event
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
	// result: per-model usage incl. the context window size
	ModelUsage map[string]struct {
		ContextWindow int `json:"contextWindow"`
	} `json:"modelUsage"`
}

type usage struct {
	InputTokens   int `json:"input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	OutputDetails struct {
		ThinkingTokens int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type denial struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

type mcpServer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// apiEvent is the raw Messages API stream event inside a "stream_event".
type apiEvent struct {
	Type         string       `json:"type"`
	ContentBlock contentBlock `json:"content_block"`
	Delta        struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

type contentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
	IsError  bool            `json:"is_error"`
}

func (ev streamEvent) isSubagent() bool {
	return ev.IsSidechain || (ev.ParentToolUseID != nil && *ev.ParentToolUseID != "")
}

// maxDetail caps stored tool inputs/results so huge outputs stay manageable.
const maxDetail = 100_000

// messagesFrom converts a user/assistant event into displayable messages.
func messagesFrom(ev streamEvent) []Message {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(ev.Message, &m) != nil || len(m.Content) == 0 {
		return nil
	}
	sub := ev.isSubagent()
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		if ev.Type == "user" && !strings.HasPrefix(strings.TrimSpace(text), "<") {
			return []Message{{UUID: ev.UUID, Kind: KindUser, Text: text, Sub: sub}}
		}
		return nil
	}
	var blocks []contentBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []Message
	for _, b := range blocks {
		msg := Message{UUID: ev.UUID, Sub: sub}
		switch {
		case b.Type == "text" && ev.Type == "assistant":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			msg.Kind, msg.Text = KindAssistant, b.Text
		case b.Type == "text" && ev.Type == "user":
			if t := strings.TrimSpace(b.Text); t == "" || strings.HasPrefix(t, "<") {
				continue
			}
			msg.Kind, msg.Text = KindUser, b.Text
		case b.Type == "thinking":
			msg.Kind, msg.Text, msg.Detail = KindThinking, "nachgedacht", strings.TrimSpace(b.Thinking)
			if ms := max(ev.ThinkingMS, ev.ThinkingMSTran); ms > 0 {
				msg.Text += fmt.Sprintf(" (%.1f s)", float64(ms)/1000)
			}
		case b.Type == "tool_use":
			msg.Kind, msg.Text, msg.Tool, msg.File = KindTool, toolSummary(b), b.Name, toolFile(b)
			msg.Detail = toolDetail(b)
		case b.Type == "tool_result":
			full := resultText(b)
			msg.Kind, msg.Text, msg.Detail = KindResult, resultSummary(full, b.IsError), full
			if !b.IsError {
				if summary, diff, ok := patchFrom(ev); ok {
					msg.Text, msg.Detail, msg.Diff = summary, diff, true
				}
			}
		default:
			continue
		}
		out = append(out, msg)
	}
	return out
}

// patchFrom renders the structuredPatch of an Edit/Write result like Claude
// Code: line number, marker (+/-/space) and text, e.g. "   22 +  return x".
func patchFrom(ev streamEvent) (summary, diff string, ok bool) {
	raw := ev.ToolUseResult
	if len(raw) == 0 {
		raw = ev.ToolUseResultTran
	}
	var r struct {
		StructuredPatch []struct {
			OldStart int      `json:"oldStart"`
			NewStart int      `json:"newStart"`
			Lines    []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	if json.Unmarshal(raw, &r) != nil || len(r.StructuredPatch) == 0 {
		return "", "", false
	}
	var lines []string
	added, removed := 0, 0
	for h, hunk := range r.StructuredPatch {
		if h > 0 {
			lines = append(lines, "  ...")
		}
		oldN, newN := hunk.OldStart, hunk.NewStart
		for _, l := range hunk.Lines {
			marker, text := " ", l
			if l != "" {
				marker, text = l[:1], l[1:]
			}
			switch marker {
			case "+":
				lines = append(lines, fmt.Sprintf("%5d +  %s", newN, text))
				newN++
				added++
			case "-":
				lines = append(lines, fmt.Sprintf("%5d -  %s", oldN, text))
				oldN++
				removed++
			default:
				lines = append(lines, fmt.Sprintf("%5d    %s", newN, text))
				oldN++
				newN++
			}
		}
	}
	summary = fmt.Sprintf("%d %s hinzugefügt, %d entfernt", added, plural(added, "Zeile", "Zeilen"), removed)
	return summary, capText(strings.Join(lines, "\n")), true
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func resultText(b contentBlock) string {
	var text string
	if json.Unmarshal(b.Content, &text) != nil {
		var parts []contentBlock
		_ = json.Unmarshal(b.Content, &parts)
		var texts []string
		for _, p := range parts {
			if p.Type == "text" {
				texts = append(texts, p.Text)
			} else if p.Type != "" {
				texts = append(texts, "["+p.Type+"]")
			}
		}
		text = strings.Join(texts, "\n")
	}
	return capText(strings.TrimRight(text, "\n"))
}

func capText(s string) string {
	if len(s) > maxDetail {
		return s[:maxDetail] + "\n… (gekürzt)"
	}
	return s
}

// resultSummary condenses a tool result to its first line plus a line count.
func resultSummary(text string, isError bool) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	s := excerpt(lines[0], 100)
	if len(lines) > 1 {
		s += fmt.Sprintf(" (+%d Zeilen)", len(lines)-1)
	}
	if isError {
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
	for _, key := range []string{"file_path", "command", "pattern", "path", "url", "description", "query"} {
		if v, ok := in[key].(string); ok && v != "" {
			return fmt.Sprintf("%s %s", b.Name, excerpt(v, 80))
		}
	}
	return b.Name
}

// toolDetail renders the full input of a tool call. Lines starting with
// "- " / "+ " are shown as removed/added text by the UI.
func toolDetail(b contentBlock) string {
	var in map[string]any
	_ = json.Unmarshal(b.Input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	prefixed := func(prefix, s string) string {
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			lines[i] = prefix + l
		}
		return strings.Join(lines, "\n")
	}
	var d string
	switch b.Name {
	case "Bash", "PowerShell":
		d = "$ " + str("command")
		if desc := str("description"); desc != "" {
			d = "# " + desc + "\n" + d
		}
	case "Edit":
		d = prefixed("- ", str("old_string")) + "\n" + prefixed("+ ", str("new_string"))
		if all, _ := in["replace_all"].(bool); all {
			d += "\n(alle Vorkommen)"
		}
	case "MultiEdit":
		edits, _ := in["edits"].([]any)
		var parts []string
		for i, e := range edits {
			em, _ := e.(map[string]any)
			o, _ := em["old_string"].(string)
			n, _ := em["new_string"].(string)
			parts = append(parts, fmt.Sprintf("Änderung %d:\n%s\n%s", i+1, prefixed("- ", o), prefixed("+ ", n)))
		}
		d = strings.Join(parts, "\n")
	case "Write":
		d = prefixed("+ ", str("content"))
	case "TodoWrite":
		todos, _ := in["todos"].([]any)
		var lines []string
		for _, t := range todos {
			tm, _ := t.(map[string]any)
			box := map[string]string{"completed": "[x]", "in_progress": "[~]"}[fmt.Sprint(tm["status"])]
			if box == "" {
				box = "[ ]"
			}
			lines = append(lines, box+" "+fmt.Sprint(tm["content"]))
		}
		d = strings.Join(lines, "\n")
	case "Task", "Agent":
		d = str("description") + "\n" + str("prompt")
	default:
		d = formatInput(in)
	}
	return capText(strings.TrimRight(d, "\n"))
}

// formatInput lists tool parameters as "key: value" lines in a stable order.
func formatInput(in map[string]any) string {
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		v := in[k]
		s, ok := v.(string)
		if !ok {
			data, _ := json.Marshal(v)
			s = string(data)
		}
		lines = append(lines, k+": "+s)
	}
	return strings.Join(lines, "\n")
}

// turnSummary describes a finished turn from its result event.
func turnSummary(ev streamEvent) string {
	u := ev.Usage
	in := u.InputTokens + u.CacheCreation + u.CacheRead
	s := fmt.Sprintf("fertig · %.1f s · %d Schritte · %s Tokens rein · %s raus",
		float64(ev.DurationMS)/1000, ev.NumTurns, shortNum(in), shortNum(u.OutputTokens))
	if t := u.OutputDetails.ThinkingTokens; t > 0 {
		s += fmt.Sprintf(" (davon %s Denken)", shortNum(t))
	}
	if ev.IsError {
		s = "abgebrochen · " + s
		if ev.APIErrorStatus != nil {
			s += fmt.Sprintf(" · API-Fehler %d", *ev.APIErrorStatus)
		}
	}
	return s
}

func shortNum(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// initSummary describes the process start from the system/init event.
func initSummary(ev streamEvent) string {
	s := fmt.Sprintf("Session bereit · Modell %s · Modus %s", ev.Model, ev.PermissionMode)
	if ev.Version != "" {
		s += " · Claude Code " + ev.Version
	}
	if len(ev.MCPServers) > 0 {
		var names []string
		for _, m := range ev.MCPServers {
			names = append(names, m.Name+" ("+m.Status+")")
		}
		s += " · MCP: " + strings.Join(names, ", ")
	}
	return s
}

func denialText(d denial) string {
	return toolSummary(contentBlock{Name: d.ToolName, Input: d.ToolInput}) +
		"  – vom Rechte-Modus nicht erlaubt"
}
