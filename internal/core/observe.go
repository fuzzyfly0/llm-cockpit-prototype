package core

// Observer mode: a session that runs in a real Claude Code terminal is
// followed through its transcript file (~/.claude/projects/*/<id>.jsonl).
// The cockpit shows its history and status live but does not run a process
// for it; messages are written in the Claude window.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// observeInterval is how often the transcript file is checked for new lines.
const observeInterval = 700 * time.Millisecond

type observer struct{ stop chan struct{} }

// ErrObserved is returned by Send for sessions that run in a Claude terminal.
var ErrObserved = errors.New("this session runs in a Claude terminal – write there, or /takeover to continue it here")

func transcriptPath(claudeID string) string {
	home, err := os.UserHomeDir()
	if err != nil || claudeID == "" {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", claudeID+".jsonl"))
	if len(files) == 0 {
		return ""
	}
	return files[0]
}

// Observe stops the cockpit's own process of a session and follows its
// transcript instead (the session continues in a Claude terminal).
func (m *Manager) Observe(id string) error {
	m.mu.Lock()
	defer m.changed()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil {
		return errors.New("session not found")
	}
	if s.ClaudeID == "" {
		return errors.New("the session has no Claude ID yet (send a message first)")
	}
	stopProc(s)
	s.proc = nil
	s.Observe = true
	m.save()
	m.startObserver(s)
	return nil
}

// TakeOver ends observing; the next message resumes the session in the
// cockpit. The Claude terminal should be closed first.
func (m *Manager) TakeOver(id string) {
	m.mu.Lock()
	defer m.changed()
	defer m.mu.Unlock()
	if s := m.find(id); s != nil {
		m.stopObserver(s)
		s.Observe = false
		s.Status = StatusStopped
		s.Activity = ""
		m.save()
	}
}

// startObserver (lock held) starts following the transcript; the history
// is rebuilt from the file.
func (m *Manager) startObserver(s *Session) {
	if s.obs != nil {
		return
	}
	o := &observer{stop: make(chan struct{})}
	s.obs = o
	s.Messages = nil
	s.historyLoaded = true
	s.Status = StatusIdle
	go m.observeLoop(s, o)
}

func (m *Manager) stopObserver(s *Session) {
	if s.obs != nil {
		close(s.obs.stop)
		s.obs = nil
	}
}

func (m *Manager) observeLoop(s *Session, o *observer) {
	m.mu.Lock()
	id := s.ClaudeID
	m.mu.Unlock()
	var path string
	var offset int64
	var rest []byte // an incomplete last line
	t := time.NewTicker(observeInterval)
	defer t.Stop()
	for {
		if path == "" {
			path = transcriptPath(id)
		}
		if path != "" {
			if data, next, err := readFrom(path, offset); err == nil && len(data) > 0 {
				offset = next
				data = append(rest, data...)
				cut := strings.LastIndexByte(string(data), '\n') + 1
				lines, tail := data[:cut], data[cut:]
				rest = append([]byte(nil), tail...)
				m.applyTranscript(s, o, lines)
			}
		}
		select {
		case <-o.stop:
			return
		case <-t.C:
		}
	}
}

func readFrom(path string, offset int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(f)
	return data, offset + int64(len(data)), err
}

// applyTranscript adds complete transcript lines to an observed session.
func (m *Manager) applyTranscript(s *Session, o *observer, lines []byte) {
	m.mu.Lock()
	if s.obs != o { // stopped meanwhile
		m.mu.Unlock()
		return
	}
	sc := bufio.NewScanner(strings.NewReader(string(lines)))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var ev streamEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			m.observeEvent(s, ev)
		}
	}
	m.mu.Unlock()
	m.changed()
}

// observeEvent updates history and status from one transcript entry.
func (m *Manager) observeEvent(s *Session, ev streamEvent) {
	switch ev.Type {
	case "user":
		for _, msg := range messagesFrom(ev) {
			s.Messages = append(s.Messages, msg)
			if msg.Kind == KindUser && !msg.Sub {
				s.Status, s.Activity = StatusWorking, "thinking…"
			}
		}
	case "assistant":
		msgs := messagesFrom(ev)
		s.Messages = append(s.Messages, msgs...)
		if ev.isSubagent() {
			return
		}
		if c := contextFrom(ev); c > 0 {
			s.ContextTokens = c
		}
		var meta struct {
			Model      string `json:"model"`
			StopReason string `json:"stop_reason"`
		}
		_ = json.Unmarshal(ev.Message, &meta)
		if meta.Model != "" {
			s.ActiveModel = meta.Model
		}
		switch meta.StopReason {
		case "end_turn", "stop_sequence", "max_tokens":
			s.Status, s.Activity, s.Unread = StatusIdle, "", true
		default:
			s.Status = StatusWorking
			for _, msg := range msgs {
				if msg.Kind == KindTool {
					s.Activity = "⚙ " + msg.Text
				}
			}
		}
	}
}

// ExternalSession is a Claude Code session found on disk that the cockpit
// does not manage yet.
type ExternalSession struct {
	ClaudeID   string
	Dir        string
	LastPrompt string
	Modified   time.Time
}

// ExternalSessions lists Claude Code transcripts changed within maxAge that
// do not belong to a cockpit session, newest first.
func (m *Manager) ExternalSessions(maxAge time.Duration) []ExternalSession {
	m.mu.Lock()
	known := map[string]bool{}
	for _, s := range m.st.Sessions {
		known[s.ClaudeID] = true
	}
	m.mu.Unlock()
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", "*.jsonl"))
	var out []ExternalSession
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		fi, err := os.Stat(f)
		if err != nil || known[id] || time.Since(fi.ModTime()) > maxAge {
			continue
		}
		dir, prompt := transcriptInfo(f)
		if dir == "" {
			continue
		}
		out = append(out, ExternalSession{ClaudeID: id, Dir: dir, LastPrompt: prompt, Modified: fi.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// transcriptInfo reads the working folder and the last prompt of a transcript.
func transcriptInfo(path string) (dir, lastPrompt string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var e struct {
			Type string `json:"type"`
			Cwd  string `json:"cwd"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Cwd != "" {
			dir = e.Cwd
		}
		if e.Type == "user" {
			var ev streamEvent
			if json.Unmarshal(sc.Bytes(), &ev) == nil {
				for _, msg := range messagesFrom(ev) {
					if msg.Kind == KindUser && !msg.Sub {
						lastPrompt = excerpt(msg.Text, 80)
					}
				}
			}
		}
	}
	return dir, lastPrompt
}

// AddObserved adds an external Claude Code session and follows it.
func (m *Manager) AddObserved(e ExternalSession) string {
	m.mu.Lock()
	s := &Session{
		ID:       fmt.Sprintf("%x", time.Now().UnixNano()),
		Name:     filepath.Base(e.Dir),
		Dir:      e.Dir,
		ClaudeID: e.ClaudeID,
		Observe:  true,
	}
	m.st.Sessions = append(m.st.Sessions, s)
	m.save()
	m.startObserver(s)
	m.mu.Unlock()
	m.changed()
	return s.ID
}
