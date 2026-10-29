package core

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type process struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stderr   *tailBuffer
	stopping bool
}

// Manager owns all sessions and their Claude processes. All methods are safe
// for concurrent use; OnChange is called (without the lock held) whenever
// session state changed in the background.
type Manager struct {
	mu       sync.Mutex
	st       *state
	OnChange func()
}

func NewManager() (*Manager, error) {
	st, err := loadState()
	return &Manager{st: st}, err
}

func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.Config
}

func (m *Manager) changed() {
	if m.OnChange != nil {
		m.OnChange()
	}
}

func (m *Manager) save() {
	_ = saveState(m.st)
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
		s.Messages = append(loadTranscript(s.ClaudeID), s.Messages...)
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
		return errors.New("session nicht gefunden")
	}
	return m.start(s)
}

func (m *Manager) start(s *Session) error {
	if s.proc != nil {
		return nil
	}
	m.ensureHistory(s)
	cfg := m.st.Config
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--permission-mode", cfg.PermissionMode}
	if s.ClaudeID != "" {
		args = append(args, "--resume", s.ClaudeID)
	}
	cmd := exec.Command(cfg.ClaudeBin, args...)
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
		s.Messages = append(s.Messages, Message{Kind: KindInfo, Text: "Start fehlgeschlagen: " + err.Error()})
		return err
	}
	s.proc = p
	s.costBase = s.CostUSD
	s.Status = StatusIdle
	s.Activity = ""
	go m.readLoop(s, p, stdout)
	return nil
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
			msg := "Prozess beendet: " + waitErr.Error()
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
		if ev.Subtype == "init" && ev.SessionID != "" && ev.SessionID != s.ClaudeID {
			s.ClaudeID = ev.SessionID
			m.save()
		}
	case "assistant":
		for _, msg := range messagesFrom(ev) {
			s.Messages = append(s.Messages, msg)
			if msg.Kind == KindTool {
				s.Activity = "⚙ " + msg.Text
			} else {
				s.Activity = "schreibt…"
			}
		}
		s.Status = StatusWorking
	case "result":
		s.Status = StatusIdle
		s.Activity = ""
		s.Unread = true
		// total_cost_usd is reported per process, so add it to the cost
		// accumulated before this process was started.
		s.CostUSD = s.costBase + ev.TotalCost
		if ev.IsError {
			s.Messages = append(s.Messages, Message{Kind: KindInfo, Text: "Fehler: " + ev.Result})
		}
		m.save()
	}
}

// Send writes a user message into the session, starting/resuming it if needed.
func (m *Manager) Send(id, text string) error {
	m.mu.Lock()
	defer m.changed()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil {
		return errors.New("session nicht gefunden")
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
	s.Activity = "denkt…"
	return nil
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

// TogglePin pins or unpins the message with the given UUID; it reports
// whether the message is pinned afterwards.
func (m *Manager) TogglePin(id string, msg Message) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.find(id)
	if s == nil || msg.UUID == "" {
		return false
	}
	defer m.save()
	for i, p := range s.Pins {
		if p.UUID == msg.UUID {
			s.Pins = append(s.Pins[:i], s.Pins[i+1:]...)
			return false
		}
	}
	s.Pins = append(s.Pins, Pin{UUID: msg.UUID, Excerpt: excerpt(msg.Text, 60)})
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
