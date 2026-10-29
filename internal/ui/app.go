// Package ui is the Bubble Tea terminal frontend on top of core.Manager.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stefa/llm-cockpit/internal/core"
	"github.com/stefa/llm-cockpit/internal/platform"
)

// ChangedMsg is sent by the manager whenever session state changed.
type ChangedMsg struct{}

type gitMsg struct {
	dir  string
	info platform.GitInfo
}

// overlayMsg opens the overlay; path is set for file previews so they can
// follow changes on disk.
type overlayMsg struct{ title, body, path string }

// previewMsg refreshes an open file preview without moving its scroll position.
type previewMsg struct{ path, body string }

// fsMsg reports that files below the given directory changed.
type fsMsg string

type statusMsg string

type tickMsg time.Time

type focus int

const (
	focusList focus = iota
	focusMessages
	focusInput
	focusNewDir
	focusNewName
	focusNotes
	focusPins
	focusOverlay
	focusConfirm
	focusFiles
)

const leftW = 34

var (
	accent    = lipgloss.Color("12")
	dim       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	bold      = lipgloss.NewStyle().Bold(true)
	userStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	llmStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	infoStyle = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("11"))
	pinStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	selStyle  = lipgloss.NewStyle().Reverse(true)
)

func statusStyle(s core.Status) lipgloss.Style {
	c := map[core.Status]string{
		core.StatusIdle: "10", core.StatusWorking: "11", core.StatusError: "9", core.StatusStopped: "8",
	}[s]
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
}

func statusIcon(s core.Status) string {
	return map[core.Status]string{
		core.StatusIdle: "○", core.StatusWorking: "●", core.StatusError: "✗", core.StatusStopped: "■",
	}[s]
}

type Model struct {
	mgr      *core.Manager
	w, h     int
	sessions []core.Session
	sel      int
	cur      core.Session
	offsets  []int
	// listStart is the first session shown in the list (for mouse hits).
	listStart int
	// stickBottom forces the next render to scroll to the newest message.
	stickBottom bool
	// details shows tool inputs and several result lines (v toggles);
	// expanded holds entries shown completely (enter toggles).
	details  bool
	expanded map[int]bool

	focus     focus
	msgCursor int
	pinSel    int
	newDir    string
	confirm   func()
	status    string
	git       map[string]platform.GitInfo
	overlayT  string
	showHelp  bool
	// overlayRaw is the unclipped overlay text, re-fitted on resize.
	overlayRaw string
	// overlayBack is the view to return to when the overlay is closed.
	overlayBack focus
	files       fileView
	previewPath string

	// The directory of the selected session is watched for changes.
	fsEvents chan string
	watcher  *platform.Watcher
	watchDir string

	vp      viewport.Model
	overlay viewport.Model
	input   textinput.Model
	notes   textarea.Model
}

func New(mgr *core.Manager) *Model {
	in := textinput.New()
	in.Prompt = "› "
	in.Placeholder = "Nachricht an die Session (enter senden, esc zurück)"
	ta := textarea.New()
	ta.Placeholder = "Notizen zu dieser Session…"
	ta.ShowLineNumbers = false
	a := &Model{
		mgr:      mgr,
		git:      map[string]platform.GitInfo{},
		vp:       viewport.New(0, 0),
		overlay:  viewport.New(0, 0),
		input:    in,
		notes:    ta,
		fsEvents: make(chan string, 1),
		details:  true,
		expanded: map[int]bool{},
	}
	a.refresh()
	return a
}

func (a *Model) Init() tea.Cmd {
	return tea.Batch(a.gitCmd(), tick(), a.waitFS())
}

func (a *Model) waitFS() tea.Cmd {
	ch := a.fsEvents
	return func() tea.Msg { return fsMsg(<-ch) }
}

// watch moves the file watcher to dir (the selected session's folder).
func (a *Model) watch(dir string) {
	if dir == a.watchDir {
		return
	}
	a.watcher.Close()
	a.watcher, a.watchDir = nil, dir
	ch := a.fsEvents
	w, err := platform.Watch(dir, func() {
		select {
		case ch <- dir:
		default:
		}
	})
	if err == nil {
		a.watcher = w
	}
}

func (a *Model) onFilesChanged(dir string) tea.Cmd {
	cmds := []tea.Cmd{a.waitFS()}
	if dir != a.cur.Dir {
		return tea.Batch(cmds...)
	}
	cmds = append(cmds, a.gitCmd())
	if a.focus == focusFiles {
		cmds = append(cmds, filesCmd(a.files.dir))
	}
	if a.focus == focusOverlay && a.previewPath != "" {
		p := a.previewPath
		cmds = append(cmds, func() tea.Msg {
			out, err := platform.ReadPreview(p)
			return previewMsg{p, withErr(out, err)}
		})
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (a *Model) selected() (core.Session, bool) {
	if a.sel < 0 || a.sel >= len(a.sessions) {
		return core.Session{}, false
	}
	return a.cur, true
}

// refresh pulls fresh snapshots from the manager and re-renders the messages.
func (a *Model) refresh() {
	a.sessions = a.mgr.List()
	if a.sel >= len(a.sessions) {
		a.sel = len(a.sessions) - 1
	}
	if a.sel < 0 && len(a.sessions) > 0 {
		a.sel = 0
	}
	prevID := a.cur.ID
	a.cur = core.Session{}
	if a.sel >= 0 {
		id := a.sessions[a.sel].ID
		if id != prevID {
			a.stickBottom = true
			a.expanded = map[int]bool{}
			a.watch(a.sessions[a.sel].Dir)
		}
		a.mgr.MarkRead(id)
		a.sessions[a.sel].Unread = false
		a.cur, _ = a.mgr.Get(id)
	}
	if a.msgCursor >= len(a.cur.Messages) {
		a.msgCursor = len(a.cur.Messages) - 1
	}
	a.renderMessages()
}

func (a *Model) gitCmd() tea.Cmd {
	s, ok := a.selected()
	if !ok {
		return nil
	}
	dir := s.Dir
	return func() tea.Msg { return gitMsg{dir, platform.GitStatus(dir)} }
}

func (a *Model) layout() {
	rightW := a.w - leftW - 4
	bodyH := a.h - 2 - 3 - 1
	a.vp.Width, a.vp.Height = rightW, max(bodyH-2, 1)
	a.overlay.Width, a.overlay.Height = rightW, max(bodyH-1, 1)
	a.notes.SetWidth(rightW)
	a.notes.SetHeight(max(bodyH-1, 1))
	a.input.Width = a.w - 6
	a.overlay.SetContent(fitLines(a.overlayRaw, a.overlay.Width))
	a.renderMessages()
}

func (a *Model) setOverlay(title, body string) {
	a.overlayT = title
	a.overlayRaw = body
	a.overlay.SetContent(fitLines(body, a.overlay.Width))
	a.overlay.GotoTop()
	a.focus = focusOverlay
}

func (a *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.w, a.h = msg.Width, msg.Height
		a.layout()
		return a, nil
	case ChangedMsg:
		a.refresh()
		return a, nil
	case gitMsg:
		a.git[msg.dir] = msg.info
		return a, nil
	case tickMsg:
		cmds := []tea.Cmd{a.gitCmd(), tick()}
		if a.focus == focusFiles {
			cmds = append(cmds, filesCmd(a.files.dir))
		}
		return a, tea.Batch(cmds...)
	case filesMsg:
		a.applyFiles(msg)
		return a, nil
	case fsMsg:
		return a, a.onFilesChanged(string(msg))
	case previewMsg:
		if a.focus == focusOverlay && msg.path == a.previewPath {
			a.overlayRaw = msg.body
			a.overlay.SetContent(fitLines(msg.body, a.overlay.Width))
		}
		return a, nil
	case statusMsg:
		a.status = string(msg)
		return a, nil
	case overlayMsg:
		a.setOverlay(msg.title, msg.body)
		a.previewPath = msg.path
		return a, a.gitCmd()
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			a.mgr.StopAll()
			return a, tea.Quit
		}
		return a.handleKey(msg)
	case tea.MouseMsg:
		return a.handleMouse(msg)
	}
	return a, nil
}

type button struct{ label, key string }

var buttons = []button{
	{"+ Neu", "n"}, {"Schreiben", "enter"}, {"Start", "r"}, {"Stop", "x"},
	{"Dateien", "d"}, {"Pin", "p"}, {"Pins", "P"}, {"Notizen", "o"}, {"Diff", "f"}, {"Pull", "u"},
	{"Explorer", "e"}, {"IDE", "c"}, {"GitHub", "g"}, {"Terminal", "t"},
	{"Entfernen", "ctrl+d"}, {"?", "?"}, {"Beenden", "q"},
}

func (a *Model) renderButtons() string {
	var parts []string
	for _, b := range buttons {
		parts = append(parts, lipgloss.NewStyle().Foreground(accent).Render("["+b.label+"]"))
	}
	return ansi.Truncate(strings.Join(parts, " "), a.w, "")
}

// buttonAt returns the key of the button at column x of the button bar.
func buttonAt(x int) (string, bool) {
	pos := 0
	for _, b := range buttons {
		w := lipgloss.Width("[" + b.label + "]")
		if x >= pos && x < pos+w {
			return b.key, true
		}
		pos += w + 1
	}
	return "", false
}

func (a *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if a.w == 0 {
		return a, nil
	}
	bodyH := a.h - 2 - 3 - 1
	inLeft := msg.X <= leftW+1 && msg.Y <= bodyH+1
	inRight := msg.X > leftW+1 && msg.Y <= bodyH+1
	row := msg.Y - 1 // content row inside the panel borders

	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		delta := 3
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -3
		}
		switch {
		case inRight && a.focus == focusFiles:
			v := &a.files
			v.sel = max(min(v.sel+delta, len(v.rows)-1), 0)
		case inLeft && (a.focus == focusList || a.focus == focusMessages):
			a.focus = focusList
			if delta < 0 {
				return a.handleNav("up")
			}
			return a.handleNav("down")
		case a.focus == focusOverlay:
			a.overlay.SetYOffset(a.overlay.YOffset + delta)
		default:
			a.vp.SetYOffset(a.vp.YOffset + delta)
		}
		return a, nil
	}
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return a, nil
	}

	// Modal views: only their own area reacts to clicks.
	switch a.focus {
	case focusConfirm, focusNewDir, focusNewName:
		return a, nil
	case focusPins:
		if inRight && row >= 1 && row-1 < len(a.cur.Pins) {
			a.pinSel = row - 1
			a.jumpTo(a.cur.Pins[a.pinSel].UUID)
		}
		return a, nil
	case focusNotes, focusOverlay:
		if !inRight {
			return a.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		}
		return a, nil
	case focusFiles:
		if inRight && row >= 1 {
			idx := a.files.top + row - 1
			if idx < len(a.files.rows) {
				if idx == a.files.sel {
					return a, a.activateFile()
				}
				a.files.sel = idx
			}
			return a, nil
		}
		if msg.Y == a.h-1 || msg.Y >= bodyH+2 {
			return a, nil
		}
		a.focus = focusList
	case focusInput:
		if msg.Y <= bodyH+1 {
			a.input.Blur()
			a.focus = focusList
		}
	}

	switch {
	case inLeft && row >= 0:
		idx := a.listStart + row/2
		if idx < len(a.sessions) {
			a.focus = focusList
			if idx != a.sel {
				a.sel = idx
				a.refresh()
				return a, a.gitCmd()
			}
			a.renderMessages()
		}
	case inRight && row >= 2:
		line := a.vp.YOffset + row - 2
		for i := len(a.offsets) - 1; i >= 0; i-- {
			if a.offsets[i] <= line {
				// A second click shows the entry completely (or clips it again).
				if a.focus == focusMessages && i == a.msgCursor {
					a.toggleExpanded()
					break
				}
				a.focus = focusMessages
				a.msgCursor = i
				a.renderMessages()
				break
			}
		}
	case msg.Y >= bodyH+2 && msg.Y <= bodyH+4:
		return a.handleNav("i")
	case msg.Y == a.h-1:
		if key, ok := buttonAt(msg.X); ok {
			return a.handleNav(key)
		}
	}
	return a, nil
}

func (a *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	var cmd tea.Cmd
	switch a.focus {
	case focusInput:
		switch key {
		case "esc":
			a.input.Blur()
			a.focus = focusList
		case "enter":
			text := strings.TrimSpace(a.input.Value())
			if s, ok := a.selected(); ok && text != "" {
				if err := a.mgr.Send(s.ID, text); err != nil {
					a.status = "Senden fehlgeschlagen: " + err.Error()
				}
				a.input.SetValue("")
			}
		default:
			a.input, cmd = a.input.Update(msg)
		}
		return a, cmd

	case focusNewDir, focusNewName:
		switch key {
		case "esc":
			a.input.Blur()
			a.input.SetValue("")
			a.input.Prompt = "› "
			a.focus = focusList
		case "enter":
			val := strings.TrimSpace(a.input.Value())
			if a.focus == focusNewDir {
				dir, err := filepath.Abs(expandHome(val))
				if info, statErr := os.Stat(dir); err != nil || statErr != nil || !info.IsDir() {
					a.status = "Ordner existiert nicht: " + val
					return a, nil
				}
				a.newDir = dir
				a.focus = focusNewName
				a.input.Prompt = "Name: "
				a.input.SetValue(filepath.Base(dir))
				a.input.CursorEnd()
				return a, nil
			}
			if val == "" {
				val = filepath.Base(a.newDir)
			}
			if _, err := a.mgr.Create(val, a.newDir); err != nil {
				a.status = "Start fehlgeschlagen: " + err.Error()
			}
			a.sel = len(a.mgr.List()) - 1
			a.input.SetValue("")
			a.input.Prompt = "› "
			a.focus = focusInput
			a.refresh()
			return a, a.gitCmd()
		default:
			a.input, cmd = a.input.Update(msg)
		}
		return a, cmd

	case focusNotes:
		if key == "esc" {
			if s, ok := a.selected(); ok {
				a.mgr.SetNotes(s.ID, a.notes.Value())
				a.status = "Notizen gespeichert"
			}
			a.notes.Blur()
			a.focus = focusList
			a.refresh()
			return a, nil
		}
		a.notes, cmd = a.notes.Update(msg)
		return a, cmd

	case focusPins:
		pins := a.cur.Pins
		switch key {
		case "esc", "q", "P":
			a.focus = focusList
		case "up", "k":
			a.pinSel = max(a.pinSel-1, 0)
		case "down", "j":
			a.pinSel = min(a.pinSel+1, len(pins)-1)
		case "x", "delete":
			if a.pinSel < len(pins) {
				a.mgr.TogglePin(a.cur.ID, core.Message{UUID: pins[a.pinSel].UUID})
				a.refresh()
				a.pinSel = max(min(a.pinSel, len(a.cur.Pins)-1), 0)
			}
		case "enter":
			if a.pinSel < len(pins) {
				a.jumpTo(pins[a.pinSel].UUID)
			}
		}
		return a, nil

	case focusFiles:
		return a.handleFilesKey(key)

	case focusOverlay:
		switch key {
		case "esc", "q":
			a.focus = a.overlayBack
			a.overlayBack = focusList
			a.showHelp = false
			if a.focus == focusFiles {
				return a, filesCmd(a.files.dir)
			}
		default:
			a.overlay, cmd = a.overlay.Update(msg)
		}
		return a, cmd

	case focusConfirm:
		if key == "y" || key == "j" {
			a.confirm()
			a.status = ""
		} else {
			a.status = "abgebrochen"
		}
		a.confirm = nil
		a.focus = focusList
		return a, nil
	}
	return a.handleNav(key)
}

func (a *Model) handleNav(key string) (tea.Model, tea.Cmd) {
	s, has := a.selected()
	a.status = ""
	switch key {
	case "q":
		a.mgr.StopAll()
		return a, tea.Quit
	case "up", "k":
		if a.focus == focusMessages {
			a.moveCursor(-1)
		} else if a.sel > 0 {
			a.sel--
			a.refresh()
			return a, a.gitCmd()
		}
	case "down", "j":
		if a.focus == focusMessages {
			a.moveCursor(1)
		} else if a.sel < len(a.sessions)-1 {
			a.sel++
			a.refresh()
			return a, a.gitCmd()
		}
	case "pgup":
		a.vp.HalfPageUp()
	case "pgdown":
		a.vp.HalfPageDown()
	case "tab":
		if a.focus == focusMessages {
			a.focus = focusList
		} else if has && len(s.Messages) > 0 {
			a.focus = focusMessages
			a.msgCursor = len(s.Messages) - 1
		}
		a.renderMessages()
	case "n":
		wd, _ := os.Getwd()
		a.focus = focusNewDir
		a.input.Prompt = "Ordner: "
		a.input.SetValue(wd)
		a.input.CursorEnd()
		return a, a.input.Focus()
	case "?":
		a.showHelp = true
		a.overlayBack = focusList
		a.previewPath = ""
		a.setOverlay("Tastenkürzel", helpText)
	}
	if !has {
		return a, nil
	}

	switch key {
	case "enter", "i":
		if key == "enter" && a.focus == focusMessages {
			a.toggleExpanded()
			break
		}
		a.focus = focusInput
		return a, a.input.Focus()
	case "v":
		a.details = !a.details
		if a.details {
			a.status = "Details an: Tool-Eingaben und mehrere Ergebniszeilen"
		} else {
			a.status = "Details aus: kompakte Ansicht"
		}
		a.renderMessages()
	case "r":
		if err := a.mgr.Start(s.ID); err != nil {
			a.status = "Start fehlgeschlagen: " + err.Error()
		}
		a.refresh()
	case "x":
		a.mgr.Stop(s.ID)
		a.status = "Session gestoppt (mit r oder einer Nachricht fortsetzen)"
	case "ctrl+d":
		a.focus = focusConfirm
		a.status = fmt.Sprintf("Session %q aus dem Cockpit entfernen? (y/n)", s.Name)
		a.confirm = func() { a.mgr.Remove(s.ID); a.refresh() }
	case "t":
		if s.ClaudeID == "" {
			a.status = "Session hat noch keine Claude-ID (erst eine Nachricht senden)"
			break
		}
		a.mgr.Stop(s.ID)
		if err := platform.OpenTerminal(s.Dir, a.mgr.Config().ClaudeBin, "--resume", s.ClaudeID); err != nil {
			a.status = "Terminal: " + err.Error()
		} else {
			a.status = "Session im Terminal geöffnet und hier gestoppt"
		}
	case "o":
		a.focus = focusNotes
		a.notes.SetValue(s.Notes)
		return a, a.notes.Focus()
	case "p":
		msg, ok := a.pinTarget()
		if !ok {
			a.status = "keine Antwort zum Pinnen"
			break
		}
		if a.mgr.TogglePin(s.ID, msg) {
			a.status = "gepinnt"
		} else {
			a.status = "Pin entfernt"
		}
		a.refresh()
	case "P":
		if len(s.Pins) == 0 {
			a.status = "keine Pins (p pinnt eine Antwort)"
			break
		}
		a.pinSel = 0
		a.focus = focusPins
	case "e":
		a.report(platform.OpenFolder(s.Dir), "Explorer geöffnet")
	case "c":
		if f, ok := a.cursorFile(); ok {
			a.report(platform.OpenIDE(a.mgr.Config().IDE, s.Dir, f), "In der IDE geöffnet: "+f)
			break
		}
		a.report(platform.OpenIDE(a.mgr.Config().IDE, s.Dir, s.Dir), "IDE gestartet")
	case "d":
		return a, a.openFiles()
	case "g":
		u := a.git[s.Dir].WebURL
		if u == "" {
			u = platform.GitHubSearchURL(s.Name)
		}
		a.report(platform.OpenURL(u), "Browser: "+u)
	case "f", "u":
		if !a.git[s.Dir].IsRepo {
			a.status = "kein git-Repository"
			break
		}
		if f, ok := a.cursorFile(); ok && key == "f" {
			a.report(platform.OpenIDEDiff(a.mgr.Config().IDE, s.Dir, f), "Diff in der IDE: "+f)
			break
		}
		dir, pull := s.Dir, key == "u"
		a.status = "git läuft…"
		a.overlayBack = focusList
		return a, func() tea.Msg {
			if pull {
				out, err := platform.GitPull(dir)
				return overlayMsg{"git pull --ff-only", withErr(out, err), ""}
			}
			out, err := platform.GitDiff(dir)
			return overlayMsg{"git diff", withErr(out, err), ""}
		}
	}
	return a, nil
}

func withErr(out string, err error) string {
	if err != nil {
		return out + "\n\nFehler: " + err.Error()
	}
	return out
}

func (a *Model) report(err error, ok string) {
	if err != nil {
		a.status = err.Error()
	} else {
		a.status = ok
	}
}

// toggleExpanded shows the entry under the cursor completely or clipped.
func (a *Model) toggleExpanded() {
	if a.msgCursor < 0 || a.msgCursor >= len(a.cur.Messages) {
		return
	}
	if a.expanded[a.msgCursor] {
		delete(a.expanded, a.msgCursor)
	} else {
		a.expanded[a.msgCursor] = true
	}
	a.renderMessages()
}

// cursorFile is the absolute file of the tool call under the message cursor.
func (a *Model) cursorFile() (string, bool) {
	if a.focus != focusMessages || a.msgCursor < 0 || a.msgCursor >= len(a.cur.Messages) {
		return "", false
	}
	m := a.cur.Messages[a.msgCursor]
	if m.Kind != core.KindTool || m.File == "" {
		return "", false
	}
	f := filepath.FromSlash(m.File)
	if !filepath.IsAbs(f) {
		f = filepath.Join(a.cur.Dir, f)
	}
	return f, true
}

// pinTarget is the message under the cursor, or the last assistant answer.
func (a *Model) pinTarget() (core.Message, bool) {
	msgs := a.cur.Messages
	if a.focus == focusMessages && a.msgCursor >= 0 && a.msgCursor < len(msgs) {
		m := msgs[a.msgCursor]
		return m, m.UUID != "" && m.Kind == core.KindAssistant
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Kind == core.KindAssistant && msgs[i].UUID != "" {
			return msgs[i], true
		}
	}
	return core.Message{}, false
}

func (a *Model) jumpTo(uuid string) {
	for i, m := range a.cur.Messages {
		if m.UUID == uuid && m.Kind == core.KindAssistant {
			a.focus = focusMessages
			a.msgCursor = i
			a.renderMessages()
			return
		}
	}
	a.status = "Stelle nicht im Verlauf gefunden"
	a.focus = focusList
}

func (a *Model) moveCursor(d int) {
	a.msgCursor = max(min(a.msgCursor+d, len(a.cur.Messages)-1), 0)
	a.renderMessages()
}

func (a *Model) renderMessages() {
	width := a.vp.Width
	if width <= 4 {
		return
	}
	// Follow new output only if the user has not scrolled up.
	follow := a.stickBottom || a.vp.AtBottom() || a.vp.TotalLineCount() <= a.vp.Height
	a.stickBottom = false
	var b strings.Builder
	a.offsets = a.offsets[:0]
	line := 0
	prevTool := ""
	msgs := a.cur.Messages
	for i, m := range msgs {
		a.offsets = append(a.offsets, line)
		selected := a.focus == focusMessages && i == a.msgCursor
		block := a.renderMessage(m, prevTool, selected, a.expanded[i], width)
		if m.Kind == core.KindTool {
			prevTool = m.Tool
		}
		if selected {
			block = lipgloss.NewStyle().Background(lipgloss.Color("236")).Width(width).Render(block)
		}
		// Like Claude Code: a blank line between entries, results stick to
		// their tool call.
		if i+1 < len(msgs) && msgs[i+1].Kind != core.KindResult {
			block += "\n"
		}
		b.WriteString(block)
		b.WriteString("\n")
		line += lipgloss.Height(block)
	}
	a.vp.SetContent(b.String())
	if a.focus == focusMessages && a.msgCursor >= 0 && a.msgCursor < len(a.offsets) {
		off := a.offsets[a.msgCursor]
		if off < a.vp.YOffset || off >= a.vp.YOffset+a.vp.Height {
			a.vp.SetYOffset(off)
		}
	} else if follow {
		a.vp.GotoBottom()
	}
}

func (a *Model) View() string {
	if a.w == 0 {
		return "Lade…"
	}
	rightW := a.w - leftW - 4
	bodyH := a.h - 2 - 3 - 1

	box := func(focused bool) lipgloss.Style {
		c := lipgloss.Color("8")
		if focused {
			c = accent
		}
		return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c)
	}

	left := box(a.focus == focusList).Width(leftW).Height(bodyH).MaxHeight(bodyH + 2).
		Render(a.renderList(bodyH))
	right := box(a.focus != focusList && a.focus != focusInput).Width(rightW).Height(bodyH).MaxHeight(bodyH + 2).
		Render(a.renderRight(rightW))
	inputFocused := a.focus == focusInput || a.focus == focusNewDir || a.focus == focusNewName
	input := box(inputFocused).Width(a.w - 2).Render(a.input.View())

	var help string
	if a.focus == focusList || a.focus == focusMessages {
		help = a.renderButtons()
	} else {
		help = dim.Render(ansi.Truncate(helpLine[a.focus], a.w, "…"))
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top, left, right), input, help)
}

func (a *Model) renderList(h int) string {
	if len(a.sessions) == 0 {
		return dim.Render("Keine Sessions.\n\nn oder [+ Neu] = neue Session")
	}
	perItem := 2
	visible := max(h/perItem, 1)
	start := 0
	if a.sel >= visible {
		start = a.sel - visible + 1
	}
	a.listStart = start
	var lines []string
	for i := start; i < len(a.sessions) && i < start+visible; i++ {
		s := a.sessions[i]
		mark := " "
		if s.Unread {
			mark = pinStyle.Render("•")
		}
		title := fmt.Sprintf("%s %s %s", statusStyle(s.Status).Render(statusIcon(s.Status)), mark, s.Name)
		title = ansi.Truncate(title, leftW, "…")
		sub := s.Preview()
		if sub == "" {
			sub = s.Status.String()
		}
		sub = dim.Render(ansi.Truncate("    "+strings.ReplaceAll(sub, "\n", " "), leftW, "…"))
		if i == a.sel {
			title = selStyle.Render(ansi.Truncate(fmt.Sprintf("%s %s %s", statusIcon(s.Status), " ", s.Name)+strings.Repeat(" ", leftW), leftW, ""))
		}
		lines = append(lines, title, sub)
	}
	return strings.Join(lines, "\n")
}

func (a *Model) renderRight(w int) string {
	s, ok := a.selected()
	if !ok {
		return infoStyle.Render(a.status) + "\n" +
			dim.Render("n oder [+ Neu] startet eine Session in einem Ordner.\n? zeigt alle Tastenkürzel.")
	}
	switch a.focus {
	case focusFiles:
		return a.renderFiles(w)
	case focusOverlay:
		return bold.Render(a.overlayT) + "\n" + a.overlay.View()
	case focusNotes:
		return bold.Render("Notizen – "+s.Name) + dim.Render("  (esc speichert)") + "\n" + a.notes.View()
	case focusPins:
		var b strings.Builder
		b.WriteString(bold.Render("Pins – "+s.Name) + dim.Render("  (enter springen, x entfernen)") + "\n")
		for i, p := range s.Pins {
			line := ansi.Truncate(fmt.Sprintf("★ %s", p.Excerpt), w, "…")
			if i == a.pinSel {
				line = selStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
		return b.String()
	}

	head := bold.Render(s.Name) + "  " + statusStyle(s.Status).Render(statusIcon(s.Status)+" "+s.Status.String())
	if s.CostUSD > 0 {
		head += dim.Render(fmt.Sprintf("  $%.2f", s.CostUSD))
	}
	if len(s.Pins) > 0 {
		head += pinStyle.Render(fmt.Sprintf("  ★%d", len(s.Pins)))
	}
	if s.Notes != "" {
		head += dim.Render("  ✎")
	}
	if g := a.git[s.Dir]; g.IsRepo {
		gs := "  ⎇ " + g.Branch
		if g.Dirty > 0 {
			gs += fmt.Sprintf(" ±%d", g.Dirty)
		}
		head += dim.Render(gs)
	}
	sub := dim.Render(ansi.Truncate(s.Dir, w, "…"))
	switch {
	case a.status != "":
		sub = infoStyle.Render(ansi.Truncate(a.status, w, "…"))
	case s.Status == core.StatusWorking && s.Activity != "":
		sub = dim.Render(ansi.Truncate(s.Activity+"  ·  "+s.Dir, w, "…"))
	}
	head = ansi.Truncate(head, w, "…") + "\n" + sub
	return head + "\n" + a.vp.View()
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

var helpLine = map[focus]string{
	focusList:     "↑↓ wählen · enter schreiben · tab Verlauf · n neu · x stop · p pin · P pins · o notizen · f diff · u pull · e explorer · c IDE · g github · ? hilfe · q beenden",
	focusMessages: "↑↓ Eintrag wählen · p pin/unpin · c Datei in IDE · f Diff in IDE · pgup/pgdn scrollen · tab zurück zur Liste",
	focusInput:    "enter senden · esc zurück",
	focusNewDir:   "Ordner für die neue Session · enter weiter · esc abbrechen",
	focusNewName:  "Name der Session · enter starten · esc abbrechen",
	focusNotes:    "esc speichert die Notizen",
	focusPins:     "↑↓ wählen · enter zur Stelle springen · x entfernen · esc zurück",
	focusOverlay:  "↑↓/pgup/pgdn scrollen · esc schließen",
	focusConfirm:  "y bestätigen · andere Taste abbrechen",
	focusFiles:    "↑↓ wählen · enter/Klick öffnen · →← auf/zu · m nur Änderungen · f Diff in IDE · F Diff hier · c IDE · e explorer · esc zurück",
}

const helpText = `Sessions
  ↑/↓ j/k    Session wählen
  n          neue Session (Ordner, dann Name)
  enter / i  Nachricht an die Session schreiben
  r          Session starten / fortsetzen
  x          Session stoppen (Verlauf bleibt, fortsetzen mit r)
  ctrl+d     Session aus dem Cockpit entfernen
  t          Session in externem Terminal öffnen (claude --resume)

Verlauf, Pins, Notizen
  tab        zwischen Liste und Verlauf wechseln
  ↑/↓        im Verlauf: Antwort oder Tool-Zeile wählen
  c / f      auf ⚙-Zeile mit Datei: in der IDE öffnen / Diff in der IDE
  p          Antwort pinnen / entpinnen (in der Liste: letzte Antwort)
  P          Pins anzeigen, enter springt zur Stelle
  o          Notizen bearbeiten (esc speichert)
  pgup/pgdn  Verlauf scrollen

Dateien (d)
  ↑/↓        Datei wählen
  enter      Ordner auf/zu bzw. Datei-Vorschau
  →/←        Ordner aufklappen / zuklappen
  m          nur geänderte Dateien zeigen (M A ? D, ✎ = von Claude bearbeitet)
  f          Diff dieser Datei im Vergleichsfenster der IDE (HEAD ↔ aktuell)
  F          Diff dieser Datei als Text im Cockpit
  c          Datei in der IDE öffnen
  e          Ordner im Explorer öffnen
  r          neu laden

Git & System
  f          git diff anzeigen
  u          git pull --ff-only
  g          GitHub öffnen (origin) bzw. GitHub-Suche nach dem Namen
  e          Ordner im Explorer öffnen
  c          Ordner in der IDE öffnen (config.ide, Standard: idea)

  q / ctrl+c beenden (laufende Sessions werden gestoppt)

Maus
  Klick auf Session      auswählen
  Klick auf Antwort      Antwort wählen (dann [Pin])
  2. Klick auf ⚙-Zeile   Datei in der IDE öffnen
  Klick auf Eingabe      Nachricht schreiben
  Mausrad                Verlauf / Liste scrollen
  Buttons unten          wie die Tasten oben
  Shift + Ziehen         Text markieren und kopieren`
