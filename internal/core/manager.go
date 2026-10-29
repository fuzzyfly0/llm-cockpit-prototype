package core

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

type process struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stderr   *tailBuffer
	stopping bool
	// initShown is set once the start line of this process was added
	// (Claude repeats system/init for every turn).
	initShown bool
}

// Manager owns all sessions and their Claude processes. All methods are safe
// for concurrent use; subscribers registered with Subscribe are notified
// asynchronously whenever session state changed.
type Manager struct {
	mu     sync.Mutex
	st     *state
	path   string        // state file; empty disables persistence (tests)
	notify chan struct{} // coalesced change notifications
}

func NewManager() (*Manager, error) {
	p, err := defaultStatePath()
	if err != nil {
		m, _ := NewManagerAt("")
		return m, err
	}
	return NewManagerAt(p)
}

// NewManagerAt uses the given state file; an empty path keeps everything in
// memory (used by tests).
func NewManagerAt(path string) (*Manager, error) {
	m := &Manager{notify: make(chan struct{}, 1), path: path, st: &state{}}
	if path == "" {
		applyDefaults(&m.st.Config)
		return m, nil
	}
	var err error
	m.st, err = loadState(path)
	return m, err
}

// Subscribe calls fn on its own goroutine after changes. Notifications never
// block the caller (which may be the UI loop itself) and bursts are coalesced.
func (m *Manager) Subscribe(fn func()) {
	go func() {
		for range m.notify {
			fn()
		}
	}()
}

func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.Config
}

func (m *Manager) changed() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

func (m *Manager) save() {
	_ = saveState(m.path, m.st)
}

func (m *Manager) find(id string) *Session {
	for _, s := range m.st.Sessions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// List returns shallow snapshots for the session list (messages excluded,
// but Preview is still answered from the snapshot's last messages).
func (m *Manager) List() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.st.Sessions))
	for _, s := range m.st.Sessions {
		c := *s
		c.Messages = append([]Message(nil), s.Messages...)
		out = append(out, c)
	}
	return out
}

// Get returns a full snapshot and lazily loads the history from the transcript.
func (m *Manager) Get(id string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil {
		return Session{}, false
	}
	m.ensureHistory(s)
	c := *s
	c.Messages = append([]Message(nil), s.Messages...)
	c.Pins = append([]Pin(nil), s.Pins...)
	return c, true
}

func (m *Manager) ensureHistory(s *Session) {
	if s.historyLoaded {
		return
	}
	s.historyLoaded = true
	if s.ClaudeID != "" {
		msgs, context := loadTranscript(s.ClaudeID)
		s.Messages = append(msgs, s.Messages...)
		if s.ContextTokens == 0 {
			s.ContextTokens = context
		}
	}
}

func (m *Manager) Create(name, dir string) (string, error) {
	m.mu.Lock()
	s := &Session{
		ID:            fmt.Sprintf("%x", time.Now().UnixNano()),
		Name:          name,
		Dir:           dir,
		historyLoaded: true,
	}
	m.st.Sessions = append(m.st.Sessions, s)
	m.save()
	err := m.start(s)
	m.mu.Unlock()
	m.changed()
	return s.ID, err
}

func (m *Manager) Start(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil {
		return errors.New("session not found")
	}
	return m.start(s)
}

func (m *Manager) start(s *Session) error {
	if s.proc != nil {
		return nil
	}
	m.ensureHistory(s)
	cfg := m.st.Config
	cmd := exec.Command(cfg.ClaudeBin, startArgs(cfg, s)...)
	cmd.Dir = s.Dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	p := &process{cmd: cmd, stdin: stdin, stderr: &tailBuffer{}}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		s.Status = StatusError
		s.Messages = append(s.Messages, Message{Kind: KindInfo, Text: "Start failed: " + err.Error()})
		return err
	}
	s.proc = p
	s.partial, s.partialThink = -1, -1
	s.costBase = s.CostUSD
	// Like Claude Code, stream the (summarized) thinking text.
	if d := cfg.ThinkingDisplay; d != "" && d != "off" {
		req, _ := json.Marshal(map[string]any{
			"type":       "control_request",
			"request_id": "cockpit-thinking",
			"request":    map[string]any{"subtype": "set_max_thinking_tokens", "thinking_display": d},
		})
		_, _ = stdin.Write(append(req, '\n'))
	}
	s.Status = StatusIdle
	s.Activity = ""
	go m.readLoop(s, p, stdout)
	return nil
}

// startArgs builds the claude command line for a session. Session settings
// come last so they override model/effort from config.extra_args.
func startArgs(cfg Config, s *Session) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--include-partial-messages", "--forward-subagent-text",
		"--permission-mode", cfg.PermissionMode}
	if s.ClaudeID != "" {
		args = append(args, "--resume", s.ClaudeID)
	}
	args = append(args, cfg.ExtraArgs...)
	if s.Model != "" {
		args = append(args, "--model", s.Model)
	}
	if s.Effort != "" {
		args = append(args, "--effort", s.Effort)
	}
	return args
}

func (m *Manager) readLoop(s *Session, p *process, stdout io.Reader) {
	r := bufio.NewReader(stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var ev streamEvent
			if json.Unmarshal(line, &ev) == nil {
				m.mu.Lock()
				m.handle(s, ev)
				m.mu.Unlock()
				m.changed()
			}
		}
		if err != nil {
			break
		}
	}
	waitErr := p.cmd.Wait()

	m.mu.Lock()
	if s.proc == p {
		s.proc = nil
		s.Activity = ""
		if p.stopping || waitErr == nil {
			s.Status = StatusStopped
		} else {
			s.Status = StatusError
			msg := "Process exited: " + waitErr.Error()
			if tail := strings.TrimSpace(p.stderr.String()); tail != "" {
				msg += "\n" + tail
			}
			s.Messages = append(s.Messages, Message{Kind: KindInfo, Text: msg})
		}
	}
	m.mu.Unlock()
	m.changed()
}

func (m *Manager) handle(s *Session, ev streamEvent) {
	switch ev.Type {
	case "system":
		switch ev.Subtype {
		case "init":
			if ev.SessionID != "" && ev.SessionID != s.ClaudeID {
				s.ClaudeID = ev.SessionID
				m.save()
			}
			s.ActiveModel = ev.Model
			if len(ev.SlashCommands) > 0 && !slices.Equal(ev.SlashCommands, m.st.ClaudeCommands) {
				m.st.ClaudeCommands = ev.SlashCommands
				m.save()
			}
			if s.proc != nil && !s.proc.initShown {
				s.proc.initShown = true
				s.Messages = append(s.Messages, Message{Kind: KindInfo, Text: initSummary(ev)})
			}
		case "thinking_tokens":
			s.Activity = fmt.Sprintf("💭 thinking… (~%d tokens)", ev.EstimatedTokens)
		case "status":
			if ev.Status != nil && *ev.Status != "" {
				s.Activity = "⏳ " + *ev.Status
			}
		default:
			s.Messages = append(s.Messages, Message{Kind: KindInfo, Text: "System: " + ev.Subtype})
		}
	case "stream_event":
		if !ev.isSubagent() {
			m.handleStream(s, ev.Event)
		}
	case "control_response":
		if ev.Response != nil && ev.Response.Subtype != "success" {
			s.Messages = append(s.Messages, Message{Kind: KindInfo,
				Text: "Control request rejected: " + ev.Response.Error})
		}
	case "rate_limit_event":
		if l, ok := parseLimits(ev.RateLimitInfo, time.Now()); ok {
			m.st.Limits = l
			m.save()
		}
	case "assistant":
		if !ev.isSubagent() {
			if c := contextFrom(ev); c > 0 {
				s.ContextTokens = c
			}
		}
		for _, msg := range messagesFrom(ev) {
			// A complete block replaces its streamed preview.
			if msg.Kind == KindAssistant && !msg.Sub && s.partial >= 0 && s.partial < len(s.Messages) {
				s.Messages[s.partial] = msg
				s.partial = -1
			} else if msg.Kind == KindThinking && !msg.Sub && s.partialThink >= 0 && s.partialThink < len(s.Messages) {
				if msg.Detail == "" {
					msg.Detail = s.Messages[s.partialThink].Detail
				}
				s.Messages[s.partialThink] = msg
				s.partialThink = -1
			} else {
				s.Messages = append(s.Messages, msg)
			}
			if msg.Kind == KindTool {
				s.Activity = "⚙ " + msg.Text
			}
		}
		s.Status = StatusWorking
	case "user":
		// Prompts are recorded by Send; only tool results come from the stream.
		for _, msg := range messagesFrom(ev) {
			if msg.Kind == KindResult {
				s.Messages = append(s.Messages, msg)
			}
		}
	case "result":
		for _, i := range []int{s.partial, s.partialThink} {
			if i >= 0 && i < len(s.Messages) {
				s.Messages[i].Partial = false
			}
		}
		s.partial, s.partialThink = -1, -1
		s.Status = StatusIdle
		s.Activity = ""
		s.Unread = true
		// total_cost_usd is reported per process, so add it to the cost
		// accumulated before this process was started.
		s.CostUSD = s.costBase + ev.TotalCost
		if mu, ok := ev.ModelUsage[s.ActiveModel]; ok && mu.ContextWindow > 0 {
			s.ContextWindow = mu.ContextWindow
		} else {
			for _, mu := range ev.ModelUsage {
				s.ContextWindow = max(s.ContextWindow, mu.ContextWindow)
			}
		}
		for _, d := range ev.PermissionDenials {
			s.Messages = append(s.Messages, Message{Kind: KindDenied, Text: denialText(d)})
		}
		if ev.IsError && ev.Result != "" {
			s.Messages = append(s.Messages, Message{Kind: KindInfo, Text: "Error: " + ev.Result})
		}
		s.Messages = append(s.Messages, Message{Kind: KindTurn, Text: turnSummary(ev)})
		m.save()
	}
}

func (m *Manager) handleStream(s *Session, e *apiEvent) {
	if e == nil {
		return
	}
	s.Status = StatusWorking
	switch e.Type {
	case "content_block_start":
		switch e.ContentBlock.Type {
		case "text":
			s.Messages = append(s.Messages, Message{Kind: KindAssistant, Partial: true})
			s.partial = len(s.Messages) - 1
			s.Activity = "writing…"
		case "thinking":
			s.Messages = append(s.Messages, Message{Kind: KindThinking, Text: "thinking…", Partial: true})
			s.partialThink = len(s.Messages) - 1
			s.Activity = "💭 thinking…"
		case "tool_use":
			s.Activity = "⚙ " + e.ContentBlock.Name + "…"
		}
	case "content_block_delta":
		switch {
		case e.Delta.Type == "text_delta" && s.partial >= 0 && s.partial < len(s.Messages):
			s.Messages[s.partial].Text += e.Delta.Text
		case e.Delta.Type == "thinking_delta" && s.partialThink >= 0 && s.partialThink < len(s.Messages):
			s.Messages[s.partialThink].Detail += e.Delta.Thinking
		}
	}
}

// Send writes a user message into the session, starting/resuming it if needed.
func (m *Manager) Send(id, text string) error {
	m.mu.Lock()
	defer m.changed()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil {
		return errors.New("session not found")
	}
	if err := m.start(s); err != nil {
		return err
	}
	line, _ := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	if _, err := s.proc.stdin.Write(append(line, '\n')); err != nil {
		return err
	}
	s.Messages = append(s.Messages, Message{Kind: KindUser, Text: text})
	s.Status = StatusWorking
	s.Activity = "thinking…"
	return nil
}

// Interrupt asks a working session to stop its current turn (like esc in
// Claude Code). The process keeps running and waits for the next message.
func (m *Manager) Interrupt(id string) bool {
	m.mu.Lock()
	defer m.changed()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil || s.proc == nil || s.Status != StatusWorking {
		return false
	}
	req, _ := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": fmt.Sprintf("cockpit-interrupt-%d", time.Now().UnixNano()),
		"request":    map[string]any{"subtype": "interrupt"},
	})
	if _, err := s.proc.stdin.Write(append(req, '\n')); err != nil {
		return false
	}
	s.Messages = append(s.Messages, Message{Kind: KindInfo, Text: "⏹ interrupted"})
	s.Activity = "interrupting…"
	return true
}

func (m *Manager) Stop(id string) {
	m.mu.Lock()
	if s := m.find(id); s != nil {
		stopProc(s)
	}
	m.mu.Unlock()
}

func stopProc(s *Session) {
	if s.proc == nil {
		return
	}
	s.proc.stopping = true
	_ = s.proc.stdin.Close()
	if s.proc.cmd.Process != nil {
		_ = s.proc.cmd.Process.Kill()
	}
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	for _, s := range m.st.Sessions {
		stopProc(s)
	}
	m.mu.Unlock()
}

func (m *Manager) Remove(id string) {
	m.mu.Lock()
	for i, s := range m.st.Sessions {
		if s.ID == id {
			stopProc(s)
			m.st.Sessions = append(m.st.Sessions[:i], m.st.Sessions[i+1:]...)
			m.save()
			break
		}
	}
	m.mu.Unlock()
	m.changed()
}

// ClaudeCommands returns the slash commands Claude Code reported last
// (built-in ones, skills and the project's own commands).
func (m *Manager) ClaudeCommands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.st.ClaudeCommands)
}

// UpdateConfig changes the configuration and saves it. New settings apply
// to sessions started afterwards.
func (m *Manager) UpdateConfig(change func(c *Config)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(&m.st.Config)
	applyDefaults(&m.st.Config)
	m.save()
}

// StatePath is the file that holds configuration and sessions.
func (m *Manager) StatePath() string { return m.path }

// Limits returns the last known account usage (shared by all sessions).
func (m *Manager) Limits() Limits {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.Limits
}

// SetModel changes the model of a session; see applySettings.
func (m *Manager) SetModel(id, model string) string {
	m.mu.Lock()
	defer m.changed()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil {
		return "session not found"
	}
	s.Model = model
	return m.applySettings(s)
}

// SetEffort changes the effort level of a session; see applySettings.
func (m *Manager) SetEffort(id, effort string) string {
	m.mu.Lock()
	defer m.changed()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil {
		return "session not found"
	}
	s.Effort = effort
	return m.applySettings(s)
}

// applySettings saves changed start options. A waiting session is restarted
// right away (resuming its conversation); a working one keeps running and
// picks them up on its next start. It returns a status text for the UI.
func (m *Manager) applySettings(s *Session) string {
	m.save()
	if s.proc == nil {
		return "applies on the next start"
	}
	if s.Status == StatusWorking {
		return "applies on the next start (the session is working; /stop, then /start)"
	}
	stopProc(s)
	s.proc = nil
	if err := m.start(s); err != nil {
		return "Restart failed: " + err.Error()
	}
	return "session restarted with the new setting"
}

func (m *Manager) SetNotes(id, notes string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.find(id); s != nil {
		s.Notes = notes
		m.save()
	}
}

func (m *Manager) MarkRead(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.find(id); s != nil {
		s.Unread = false
	}
}

// TogglePin pins or unpins the entry with the given key (see MessageKeys);
// it reports whether the entry is pinned afterwards.
func (m *Manager) TogglePin(id, key, label string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil || key == "" {
		return false
	}
	defer m.save()
	for i, p := range s.Pins {
		if p.UUID == key {
			s.Pins = append(s.Pins[:i], s.Pins[i+1:]...)
			return false
		}
	}
	s.Pins = append(s.Pins, Pin{UUID: key, Excerpt: excerpt(label, 80)})
	return true
}

// tailBuffer keeps the last few KB written to it (used for stderr).
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
