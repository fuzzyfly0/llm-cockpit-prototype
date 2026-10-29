// Package core contains the UI-independent session logic. It knows nothing
// about Bubble Tea so that other frontends (e.g. an IDE bridge) can reuse it.
package core

import "strings"

type Status int

const (
	StatusStopped Status = iota
	StatusIdle
	StatusWorking
	StatusError
)

func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "wartet"
	case StatusWorking:
		return "arbeitet"
	case StatusError:
		return "fehler"
	default:
		return "gestoppt"
	}
}

type Kind int

const (
	KindUser Kind = iota
	KindAssistant
	KindTool
	KindResult
	KindInfo
)

type Message struct {
	UUID string
	Kind Kind
	Text string
	// Partial marks an assistant text that is still being streamed.
	Partial bool
	// Tool and File describe a tool call (KindTool only); File is set for
	// tools that read or modify a file.
	Tool string
	File string
}

// Modifies reports whether the tool call changed File.
func (m Message) Modifies() bool {
	switch m.Tool {
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		return m.File != ""
	}
	return false
}

// TouchedFiles returns the files Claude modified in this session.
func (s *Session) TouchedFiles() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range s.Messages {
		if m.Modifies() && !seen[m.File] {
			seen[m.File] = true
			out = append(out, m.File)
		}
	}
	return out
}

type Pin struct {
	UUID    string `json:"uuid"`
	Excerpt string `json:"excerpt"`
}

// Session combines persisted fields (exported JSON tags) with runtime state.
type Session struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Dir      string  `json:"dir"`
	ClaudeID string  `json:"claude_id,omitempty"`
	Notes    string  `json:"notes,omitempty"`
	Pins     []Pin   `json:"pins,omitempty"`
	CostUSD  float64 `json:"cost_usd,omitempty"`

	Status   Status    `json:"-"`
	Activity string    `json:"-"`
	Unread   bool      `json:"-"`
	Messages []Message `json:"-"`

	historyLoaded bool
	costBase      float64
	proc          *process
	partial       int // index of the streaming message in Messages, -1 if none
}

func (s *Session) IsPinned(uuid string) bool {
	for _, p := range s.Pins {
		if p.UUID == uuid {
			return true
		}
	}
	return false
}

// Preview returns what the session is doing right now or what it said last.
func (s *Session) Preview() string {
	if s.Status == StatusWorking && s.Activity != "" {
		return s.Activity
	}
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Kind == KindAssistant {
			return firstLine(s.Messages[i].Text)
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
