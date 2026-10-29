// Package core contains the UI-independent session logic. It knows nothing
// about Bubble Tea so that other frontends (e.g. an IDE bridge) can reuse it.
package core

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
)

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
	KindThinking
	KindTurn   // summary of a finished turn
	KindDenied // tool call blocked by the permission mode
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
	// Detail is the full tool input (KindTool) or tool output (KindResult).
	Detail string
	// Sub marks output of a subagent.
	Sub bool
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

// Pin marks a history entry. UUID holds the entry's key (see MessageKeys);
// for the first entry of a Claude message that is simply its UUID, which
// keeps pins from older versions valid.
type Pin struct {
	UUID    string `json:"uuid"`
	Excerpt string `json:"excerpt"`
}

// MessageKeys returns a stable key per message: the UUID (with "#n" for
// further entries sharing it) or, for entries without UUID, a hash of kind
// and text so they can be found again after reloading the transcript.
func MessageKeys(msgs []Message) []string {
	keys := make([]string, len(msgs))
	seen := map[string]int{}
	for i, m := range msgs {
		if m.UUID == "" {
			h := sha1.Sum([]byte(fmt.Sprintf("%d|%s", m.Kind, m.Text)))
			keys[i] = "h:" + hex.EncodeToString(h[:8])
			continue
		}
		n := seen[m.UUID]
		seen[m.UUID] = n + 1
		if n == 0 {
			keys[i] = m.UUID
		} else {
			keys[i] = fmt.Sprintf("%s#%d", m.UUID, n)
		}
	}
	return keys
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
	// Model and Effort are passed as --model/--effort; empty = Claude default.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`

	// ActiveModel is the model reported by the running process.
	ActiveModel string `json:"-"`
	// ContextTokens is the size of the conversation sent with the last
	// request; ContextWindow the model's maximum (0 = unknown).
	ContextTokens int `json:"-"`
	ContextWindow int `json:"-"`

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
