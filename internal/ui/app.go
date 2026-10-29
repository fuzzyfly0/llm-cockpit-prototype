// Package ui is the Bubble Tea terminal frontend on top of core.Manager.
package ui

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stefa/llm-cockpit/internal/core"
	"github.com/stefa/llm-cockpit/internal/platform"
)

// ChangedMsg is sent by the manager whenever session state changed.
type ChangedMsg struct{}

// debugLog records keys and mouse events when COCKPIT_DEBUG is set (see
// SetDebugLog); nil otherwise.
var debugLog *log.Logger

// SetDebugLog enables the input log.
func SetDebugLog(l *log.Logger) { debugLog = l }

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
	focusPick
	focusNewWorktree
	focusSetting
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
	// listStart is the first session shown in the list and listRows the
	// number of screen rows it uses (for mouse hits).
	listStart int
	listRows  int
	// rowMap maps each visible list row to a session index (-1: header).
	rowMap  []int
	newRepo string
	// stickBottom forces the next render to scroll to the newest message.
	stickBottom bool
	// details shows tool inputs and several result lines (v toggles);
	// expanded holds entries shown completely (enter toggles).
	details  bool
	expanded map[int]bool
	// transcript shows every entry completely (ctrl+o, like Claude Code).
	transcript bool
	pick       picker

	// Keyboard concept (keys.go): tabs on the right, command list,
	// prompt history and the double ctrl+c to quit.
	tab        tab
	tabX       [][2]int
	paletteSel int
	paletteTop int
	histPos    int
	quitArmed  time.Time
	// notesFor is the session whose notes are in the notes editor.
	notesFor string
	// settingKey is the text setting being edited (see configui.go).
	settingKey string
	// Session search (ctrl+k): the query is typed into the message field,
	// draft keeps the message that was being written.
	switching bool
	switchSel int
	draft     string

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
	input   textarea.Model
	notes   textarea.Model
}

func New(mgr *core.Manager) *Model {
	in := newMessageField()
	ta := textarea.New()
	ta.Placeholder = "Notes for this session…"
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
		histPos:  -1,
	}
	a.refresh()
	// Like Claude Code, typing goes straight into the message field;
	// single-key shortcuts work after esc.
	if len(a.sessions) > 0 {
		a.focus = focusInput
		a.input.Focus()
	}
	return a
}

func (a *Model) Init() tea.Cmd {
	return tea.Batch(a.gitAllCmd(), tick(), a.waitFS())
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
	if a.tab == tabFiles {
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
			a.pinSel = 0
			a.watch(a.sessions[a.sel].Dir)
			// The notes editor belongs to one session: save it before
			// showing another one.
			a.saveNotes()
		}
		a.mgr.MarkRead(id)
		a.sessions[a.sel].Unread = false
		a.cur, _ = a.mgr.Get(id)
		if id != prevID && a.tab == tabNotes {
			a.notes.SetValue(a.cur.Notes)
			a.notesFor = id
		}
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
	bodyH := a.bodyHeight()
	a.vp.Width, a.vp.Height = rightW, max(bodyH-2, 1)
	a.overlay.Width, a.overlay.Height = rightW, max(bodyH-1, 1)
	a.notes.SetWidth(rightW)
	a.notes.SetHeight(max(bodyH-2, 1))
	a.input.SetWidth(max(a.w-4, 10))
	a.input.SetHeight(a.inputHeight())
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
	case worktreeMsg:
		return a, a.onWorktreeAdded(msg)
	case tickMsg:
		cmds := []tea.Cmd{a.gitCmd(), tick()}
		if a.tab == tabFiles && a.files.dir != "" {
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
		if debugLog != nil {
			debugLog.Printf("key %q type=%d alt=%v focus=%d", msg.String(), msg.Type, msg.Alt, a.focus)
		}
		return a.routeKey(msg)
	case tea.MouseMsg:
		ox, oy := platform.ConsoleWindowOrigin()
		if debugLog != nil {
			debugLog.Printf("mouse %s x=%d y=%d (console origin %d,%d) screen %dx%d", msg.String(), msg.X, msg.Y, ox, oy, a.w, a.h)
		}
		msg.X -= ox
		msg.Y -= oy
		return a.handleMouse(msg)
	}
	return a, nil
}

// routeKey dispatches a key: popups and dialogs first, then global keys,
// then the focused area.
func (a *Model) routeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch a.focus {
	case focusPick, focusOverlay, focusConfirm, focusNewDir, focusNewName, focusNewWorktree, focusSetting:
		if msg.String() == "ctrl+c" {
			// ctrl+c cancels a dialog like esc does.
			return a.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		}
		return a.handleKey(msg)
	}
	if m, cmd, ok := a.handleGlobalKey(msg); ok {
		return m, cmd
	}
	switch a.focus {
	case focusInput:
		if !a.input.Focused() {
			a.input.Focus()
		}
		return a.handleInputKey(msg)
	case focusNotes:
		switch msg.String() {
		case "esc":
			return a, a.backToInput()
		case "tab", "shift+tab":
			d := 1
			if msg.String() == "shift+tab" {
				d = -1
			}
			return a, a.cycleFocus(d)
		}
		return a.handleKey(msg)
	case focusFiles:
		switch msg.String() {
		case "esc":
			return a, a.backToInput()
		case "tab":
			return a, a.cycleFocus(1)
		case "shift+tab":
			return a, a.cycleFocus(-1)
		}
		return a.handleFilesKey(msg)
	}
	return a.handlePanelKey(msg)
}

// viewName names the current view level (see cycleView).
func (a *Model) viewName() string {
	switch {
	case a.transcript:
		return "all"
	case a.details:
		return "normal"
	}
	return "compact"
}

// cycleView switches the history view: normal → alles → kompakt → normal.
// "alles" shows every entry completely (like ctrl+o in Claude Code),
// "kompakt" only headers and one result line.
func (a *Model) cycleView() {
	switch a.viewName() {
	case "normal":
		a.details, a.transcript = true, true
	case "all":
		a.details, a.transcript = false, false
	default:
		a.details, a.transcript = true, false
	}
	a.status = "View: " + a.viewName() + " (ctrl+o cycles normal → all → compact)"
	a.renderMessages()
}

// newMessageField creates the multi-line message field. Enter sends (see
// handleInputKey); new lines come from shift+enter, alt+enter, ctrl+j or a
// trailing backslash.
func newMessageField() textarea.Model {
	in := textarea.New()
	in.Prompt = "› "
	in.Placeholder = "Message to the session · enter send · shift+enter new line · / commands · ? help"
	in.ShowLineNumbers = false
	in.CharLimit = 0
	in.MaxHeight = 0
	in.SetHeight(1)
	in.KeyMap.InsertNewline.SetKeys("alt+enter", "ctrl+j")
	in.FocusedStyle.CursorLine = lipgloss.NewStyle()
	in.FocusedStyle.Base = lipgloss.NewStyle()
	in.BlurredStyle.Base = lipgloss.NewStyle()
	return in
}

const maxInputRows = 6

// inputHeight is the number of text rows of the message field: it grows
// with the (wrapped) text, up to maxInputRows.
func (a *Model) inputHeight() int {
	w := max(a.w-4-lipgloss.Width(a.input.Prompt), 10)
	rows := 0
	for _, line := range strings.Split(a.input.Value(), "\n") {
		rows += max((lipgloss.Width(line)+w)/w, 1)
	}
	return min(max(rows, 1), maxInputRows)
}

// fitInput adjusts the layout when the message field grew or shrank.
func (a *Model) fitInput() {
	if h := a.inputHeight(); h != a.input.Height() {
		a.layout()
	}
}

// bodyHeight is the inner height of the two main panels: everything above
// the message field (with its border) and the hint line.
func (a *Model) bodyHeight() int { return max(a.h-2-(a.inputHeight()+2)-1, 1) }

func (a *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if a.w == 0 {
		return a, nil
	}
	bodyH := a.bodyHeight()
	inLeft := msg.X <= leftW+1 && msg.Y <= bodyH+1
	inRight := msg.X > leftW+1 && msg.Y <= bodyH+1
	inInput := msg.Y >= bodyH+2 && msg.Y < a.h-1
	row := msg.Y - 1         // content row inside the panel borders
	col := msg.X - leftW - 3 // content column of the right panel

	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		delta := 3
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -3
		}
		switch {
		case inLeft:
			// Switch sessions; the focus stays where it is.
			if a.moveSelection(sign(delta)) {
				a.refresh()
				return a, a.sessionChanged()
			}
		case a.focus == focusOverlay:
			a.overlay.SetYOffset(a.overlay.YOffset + delta)
		case a.tab == tabFiles:
			v := &a.files
			v.sel = max(min(v.sel+delta, len(v.rows)-1), 0)
		default:
			a.vp.SetYOffset(a.vp.YOffset + delta)
		}
		return a, nil
	}
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return a, nil
	}

	// Dialogs and popups: only their own area reacts to clicks.
	switch a.focus {
	case focusConfirm, focusNewDir, focusNewName, focusNewWorktree, focusSetting:
		return a, nil
	case focusPick:
		if inRight && row >= 1 {
			return a, a.choosePick(row - 1)
		}
		if !inRight {
			a.focus = focusInput
			return a, a.input.Focus()
		}
		return a, nil
	case focusOverlay:
		if !inRight {
			return a.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
		}
		return a, nil
	}
	if a.switching && inRight && row >= 1 {
		if m := a.switchMatches(); row-1 < len(m) {
			return a, a.chooseSwitch(m[row-1])
		}
		return a, nil
	}
	if a.paletteOpen() && inRight && row >= 1 {
		if m, i := a.paletteMatches(), a.paletteTop+row-1; i < len(m) {
			return a.runCommand(m[i])
		}
		return a, nil
	}

	switch {
	case inLeft && row >= 0:
		// A click into the list selects the session and focuses the list,
		// so ↑↓ continue from there; typing still goes to the message field.
		a.focusList()
		if row < a.listRows && a.rowMap[row] >= 0 && a.rowMap[row] != a.sel {
			a.sel = a.rowMap[row]
			a.refresh()
			return a, a.sessionChanged()
		}
		a.renderMessages()
		return a, nil
	case inRight && row == 0:
		if t, ok := a.tabAt(col); ok {
			return a, a.setTab(t)
		}
	case inRight && row >= 2:
		return a, a.clickRightPanel(row - 2)
	case inInput:
		return a, a.leaveForInput()
	case msg.Y == a.h-1:
		return a, a.openPalette()
	}
	return a, nil
}

// clickRightPanel handles a click on content row r below the panel header.
func (a *Model) clickRightPanel(r int) tea.Cmd {
	switch a.tab {
	case tabFiles:
		a.focus = focusFiles
		idx := a.files.top + r - 1 // first row is the file view's title
		if r >= 1 && idx < len(a.files.rows) {
			if idx == a.files.sel {
				return a.activateFile()
			}
			a.files.sel = idx
		}
		return nil
	case tabNotes:
		if a.focus != focusNotes {
			return a.setTab(tabNotes)
		}
		return nil
	case tabPins:
		a.focus = focusPins
		if r < len(a.cur.Pins) {
			a.pinSel = r
			a.jumpTo(a.cur.Pins[r].UUID)
		}
		return nil
	}
	line := a.vp.YOffset + r
	for i := len(a.offsets) - 1; i >= 0; i-- {
		if a.offsets[i] <= line {
			// A second click shows the entry completely (or clips it again).
			if a.focus == focusMessages && i == a.msgCursor {
				a.toggleExpanded()
				return nil
			}
			a.input.Blur()
			a.focus = focusMessages
			a.msgCursor = i
			a.renderMessages()
			return nil
		}
	}
	return nil
}

// focusList gives the session list the keyboard focus (saving open notes).
func (a *Model) focusList() {
	if a.focus == focusNotes {
		a.saveNotes()
		a.notes.Blur()
	}
	a.input.Blur()
	a.focus = focusList
}

// leaveForInput gives the message field the focus (saving open notes).
func (a *Model) leaveForInput() tea.Cmd {
	if a.focus == focusNotes {
		a.saveNotes()
		a.notesFor = ""
		a.notes.Blur()
		a.tab = tabHistory
	}
	a.focus = focusInput
	return a.input.Focus()
}

func sign(n int) int {
	if n < 0 {
		return -1
	}
	return 1
}

// handleKey handles dialogs, popups and the notes editor (see routeKey).
func (a *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	var cmd tea.Cmd
	switch a.focus {
	case focusNewDir, focusNewName, focusNewWorktree, focusSetting:
		switch key {
		case "esc":
			a.input.Blur()
			a.resetInput()
			a.focus = focusInput
		case "enter":
			val := strings.TrimSpace(a.input.Value())
			switch a.focus {
			case focusNewDir:
				dir, err := filepath.Abs(expandHome(val))
				if info, statErr := os.Stat(dir); err != nil || statErr != nil || !info.IsDir() {
					a.status = "folder does not exist: " + val
					return a, nil
				}
				// In a git repository the session may get its own worktree.
				if repo := platform.RepoRoot(dir); repo != "" {
					a.chooseWorkspace(dir, repo)
					return a, nil
				}
				a.askName(dir)
				return a, nil
			case focusNewWorktree:
				return a, a.newWorktreeEntered(val)
			case focusSetting:
				a.settingEntered(val)
				return a, nil
			}
			if val == "" {
				val = filepath.Base(a.newDir)
			}
			return a, a.createSession(val, a.newDir)
		default:
			a.input, cmd = a.input.Update(msg)
		}
		return a, cmd

	case focusNotes:
		if key == "ctrl+y" {
			a.status = a.copyNotes(a.notes.Value())
			return a, nil
		}
		a.notes, cmd = a.notes.Update(msg)
		return a, cmd

	case focusPick:
		return a.handlePickKey(key)

	case focusOverlay:
		switch key {
		case "esc", "q":
			a.focus = a.overlayBack
			a.overlayBack = focusInput
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
			a.status = "cancelled"
		}
		a.confirm = nil
		a.focus = focusInput
		return a, nil
	}
	return a, nil
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
		} else if a.moveSelection(-1) {
			a.refresh()
			return a, a.sessionChanged()
		}
	case "down", "j":
		if a.focus == focusMessages {
			a.moveCursor(1)
		} else if a.moveSelection(1) {
			a.refresh()
			return a, a.sessionChanged()
		}
	case "pgup":
		a.vp.HalfPageUp()
	case "pgdown":
		a.vp.HalfPageDown()
	case "tab":
		if a.focus == focusMessages {
			a.focus = focusInput
		} else if has && len(s.Messages) > 0 {
			a.focus = focusMessages
			a.msgCursor = len(s.Messages) - 1
		}
		a.renderMessages()
	case "n":
		return a, a.newSession()
	case "?":
		a.showHelp = true
		a.overlayBack = focusInput
		a.previewPath = ""
		a.setOverlay("Keyboard shortcuts", helpText)
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
	case "y":
		i, ok := a.copyTarget()
		if !ok {
			a.status = "nothing to copy (tab, select an entry, then y)"
			break
		}
		text := copyText(s.Messages[i])
		if err := platform.CopyToClipboard(text); err != nil {
			a.status = "Kopieren fehlgeschlagen: " + err.Error()
		} else {
			a.status = fmt.Sprintf("copied: %d characters, %d lines", len([]rune(text)), strings.Count(text, "\n")+1)
		}
	case "ctrl+o": // from the button bar
		a.cycleView()
	case "r":
		if err := a.mgr.Start(s.ID); err != nil {
			a.status = "Start failed: " + err.Error()
		}
		a.refresh()
	case "x":
		a.mgr.Stop(s.ID)
		a.status = "session stopped (continue with /start or a message)"
	case "ctrl+d":
		a.confirmRemove(s)
	case "t":
		if s.ClaudeID == "" {
			a.status = "the session has no Claude ID yet (send a message first)"
			break
		}
		a.mgr.Stop(s.ID)
		if err := platform.OpenTerminal(s.Dir, a.mgr.Config().ClaudeBin, "--resume", s.ClaudeID); err != nil {
			a.status = "Terminal: " + err.Error()
		} else {
			a.status = "session opened in a terminal and stopped here"
		}
	case "Y":
		a.status = a.copyNotes(s.Notes)
	case "o":
		return a, a.setTab(tabNotes)
	case "p":
		i, ok := a.pinTarget()
		if !ok {
			a.status = "nothing to pin (tab, select an entry, then p)"
			break
		}
		key := core.MessageKeys(s.Messages)[i]
		if a.mgr.TogglePin(s.ID, key, pinLabel(s.Messages[i])) {
			a.status = "pinned"
		} else {
			a.status = "pin removed"
		}
		a.refresh()
	case "P":
		return a, a.setTab(tabPins)
	case "e":
		a.report(platform.OpenFolder(s.Dir), "opened in the file manager")
	case "c":
		if f, ok := a.cursorFile(); ok {
			a.report(platform.OpenIDE(a.mgr.Config().IDE, s.Dir, f), "opened in the IDE: "+f)
			break
		}
		a.report(platform.OpenIDE(a.mgr.Config().IDE, s.Dir, s.Dir), "IDE started")
	case "d":
		return a, a.setTab(tabFiles)
	case "M":
		id := s.ID
		a.openPicker("Model – "+s.Name, modelOptions, s.Model,
			a.settingApply("Model", modelOptions, func(v string) string { return a.mgr.SetModel(id, v) }))
	case "E":
		id := s.ID
		a.openPicker("Effort – "+s.Name, effortOptions, s.Effort,
			a.settingApply("Effort", effortOptions, func(v string) string { return a.mgr.SetEffort(id, v) }))
	case "g":
		u := a.git[s.Dir].WebURL
		if u == "" {
			u = platform.GitHubSearchURL(s.Name)
		}
		a.report(platform.OpenURL(u), "browser: "+u)
	case "f", "u":
		if !a.git[s.Dir].IsRepo {
			a.status = "not a git repository"
			break
		}
		if f, ok := a.cursorFile(); ok && key == "f" {
			a.report(platform.OpenIDEDiff(a.mgr.Config().IDE, s.Dir, f), "diff in the IDE: "+f)
			break
		}
		dir, pull := s.Dir, key == "u"
		a.status = "running git…"
		a.overlayBack = focusInput
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

// pinTarget returns the index of the entry under the cursor, or of the last
// finished Claude answer when the session list has focus.
func (a *Model) pinTarget() (int, bool) {
	msgs := a.cur.Messages
	if a.focus == focusMessages && a.msgCursor >= 0 && a.msgCursor < len(msgs) {
		return a.msgCursor, !msgs[a.msgCursor].Partial
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Kind == core.KindAssistant && !msgs[i].Partial {
			return i, true
		}
	}
	return 0, false
}

// copyNotes copies the complete notes and returns a status text.
func (a *Model) copyNotes(notes string) string {
	if strings.TrimSpace(notes) == "" {
		return "no notes to copy"
	}
	if err := platform.CopyToClipboard(notes); err != nil {
		return "Kopieren fehlgeschlagen: " + err.Error()
	}
	return fmt.Sprintf("notes copied (%d lines)", strings.Count(strings.TrimRight(notes, "\n"), "\n")+1)
}

// copyTarget is the entry under the cursor, or the last Claude answer when
// the session list has focus.
func (a *Model) copyTarget() (int, bool) {
	return a.pinTarget()
}

// pinLabel is the text shown in the pin list for an entry.
func pinLabel(m core.Message) string {
	switch m.Kind {
	case core.KindUser:
		return "> " + m.Text
	case core.KindTool:
		return "⏺ " + m.Text
	case core.KindResult:
		return "⎿ " + m.Text
	case core.KindThinking:
		return "✻ " + m.Text
	case core.KindTurn:
		return "✓ " + m.Text
	case core.KindDenied:
		return "⛔ " + m.Text
	case core.KindInfo:
		return "● " + m.Text
	}
	return m.Text
}

func (a *Model) jumpTo(key string) {
	for i, k := range core.MessageKeys(a.cur.Messages) {
		if k == key {
			a.tab = tabHistory
			a.input.Blur()
			a.focus = focusMessages
			a.msgCursor = i
			a.renderMessages()
			return
		}
	}
	a.status = "entry not found in the history (e.g. a summary line from before a restart)"
	a.focus = focusInput
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
	keys := core.MessageKeys(msgs)
	for i, m := range msgs {
		a.offsets = append(a.offsets, line)
		selected := a.focus == focusMessages && i == a.msgCursor
		block := a.renderMessage(m, prevTool, selected, a.transcript || a.expanded[i], width)
		if a.cur.IsPinned(keys[i]) {
			first, rest, hasRest := strings.Cut(block, "\n")
			block = first + pinStyle.Render(" ★")
			if hasRest {
				block += "\n" + rest
			}
		}
		if m.Kind == core.KindTool {
			prevTool = m.Tool
		}
		if selected {
			first, rest, hasRest := strings.Cut(block, "\n")
			block = first + dim.Render("  [y] copy")
			if hasRest {
				block += "\n" + rest
			}
			block = markSelected(block)
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
	bodyH := a.bodyHeight()

	box := func(focused bool) lipgloss.Style {
		c := lipgloss.Color("8")
		if focused {
			c = accent
		}
		return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c)
	}

	rightFocused := a.paletteOpen()
	switch a.focus {
	case focusMessages, focusFiles, focusNotes, focusPins, focusPick, focusOverlay:
		rightFocused = true
	}
	left := box(a.focus == focusList).Width(leftW).Height(bodyH).MaxHeight(bodyH + 2).
		Render(a.renderList(bodyH))
	right := box(rightFocused).Width(rightW).Height(bodyH).MaxHeight(bodyH + 2).
		Render(a.renderRight(rightW))
	inputFocused := a.focus == focusInput || a.focus == focusNewDir || a.focus == focusNewName || a.focus == focusNewWorktree || a.focus == focusSetting
	input := box(inputFocused).Width(a.w - 2).Render(a.input.View())
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top, left, right), input, a.hintLine())
}

func (a *Model) renderList(h int) string {
	usage := usageLines(a.mgr.Limits(), time.Now(), leftW)
	list := a.renderSessions(h - len(usage))
	if len(usage) == 0 {
		return list
	}
	// Pad the list so the usage block sits at the bottom of the panel.
	pad := max(h-len(usage)-lipgloss.Height(list), 0)
	return list + strings.Repeat("\n", pad+1) + strings.Join(usage, "\n")
}

func (a *Model) renderSessions(h int) string {
	a.rowMap, a.listRows = nil, 0
	if len(a.sessions) == 0 {
		return dim.Render("No sessions.\n\nctrl+n = new session")
	}
	// Build all rows (group headers + two rows per session), then show the
	// window that contains the selected session.
	var lines []string
	var rowMap []int
	selRow := 0
	for _, g := range a.groups() {
		lines = append(lines, ansi.Truncate(dim.Render("▾ ")+bold.Render(g.label)+dim.Render(fmt.Sprintf(" (%d)", len(g.sessions))), leftW, "…"))
		rowMap = append(rowMap, -1)
		for _, i := range g.sessions {
			s := a.sessions[i]
			mark := " "
			if s.Unread {
				mark = pinStyle.Render("•")
			}
			branch := ""
			if gi := a.git[s.Dir]; gi.IsRepo {
				// Worktree sessions are usually named like their branch.
				if gi.Branch == s.Name {
					branch = dim.Render("  ⎇")
				} else {
					branch = dim.Render("  ⎇ " + gi.Branch)
				}
			}
			title := ansi.Truncate(fmt.Sprintf("  %s %s %s", statusStyle(s.Status).Render(statusIcon(s.Status)), mark, s.Name)+branch, leftW, "…")
			sub := s.Preview()
			if sub == "" {
				sub = s.Status.String()
			}
			sub = dim.Render(ansi.Truncate("      "+strings.ReplaceAll(sub, "\n", " "), leftW, "…"))
			if i == a.sel {
				selRow = len(lines)
				plain := ansi.Strip(title)
				title = selStyle.Render(ansi.Truncate(plain+strings.Repeat(" ", leftW), leftW, ""))
			}
			lines = append(lines, title, sub)
			rowMap = append(rowMap, i, i)
		}
	}
	start := 0
	if selRow+2 > h {
		start = selRow + 2 - h
	}
	end := min(start+h, len(lines))
	a.listStart = start
	a.rowMap = rowMap[start:end]
	a.listRows = end - start
	return strings.Join(lines[start:end], "\n")
}

func (a *Model) renderRight(w int) string {
	// Popups take over the panel.
	switch {
	case a.focus == focusPick:
		return a.renderPick(w)
	case a.focus == focusOverlay:
		return bold.Render(a.overlayT) + "\n" + a.overlay.View()
	case a.switching:
		return a.renderSwitcher(w)
	case a.paletteOpen():
		return a.renderPalette(w)
	}
	s, ok := a.selected()
	if !ok {
		return infoStyle.Render(a.status) + "\n" +
			dim.Render("ctrl+n starts a new session.\n/ shows all commands, ? the help.")
	}
	return a.renderHeader(s, w) + "\n" + a.renderTab(s, w)
}

// renderHeader draws two lines: the tabs with name and status, then model,
// effort, context, git and folder – or a status/activity message.
func (a *Model) renderHeader(s core.Session, w int) string {
	tabs := a.renderTabs(sessionInfo{hasNotes: s.Notes != "", pins: len(s.Pins)})
	name := bold.Render(s.Name) + " " + statusStyle(s.Status).Render(statusIcon(s.Status)+" "+s.Status.String())
	line1 := tabs
	if gap := w - lipgloss.Width(tabs) - lipgloss.Width(name); gap >= 2 {
		line1 += strings.Repeat(" ", gap) + name
	}

	info := []string{modelText(s)}
	if s.Effort != "" {
		info = append(info, "effort "+s.Effort)
	}
	if c := contextText(s); c != "" {
		info = append(info, c)
	}
	if g := a.git[s.Dir]; g.IsRepo {
		gs := "⎇ " + g.Branch
		if g.Dirty > 0 {
			gs += fmt.Sprintf(" ±%d", g.Dirty)
		}
		info = append(info, gs)
	}
	if s.CostUSD > 0 {
		info = append(info, fmt.Sprintf("$%.2f", s.CostUSD))
	}
	info = append(info, s.Dir)
	line2 := dim.Render(ansi.Truncate(strings.Join(info, " · "), w, "…"))
	switch {
	case a.status != "":
		line2 = infoStyle.Render(ansi.Truncate(a.status, w, "…"))
	case s.Status == core.StatusWorking && s.Activity != "":
		line2 = dim.Render(ansi.Truncate(s.Activity+"  ·  "+modelText(s), w, "…"))
	}
	return ansi.Truncate(line1, w, "…") + "\n" + line2
}

// renderTab draws the content of the active tab below the header.
func (a *Model) renderTab(s core.Session, w int) string {
	switch a.tab {
	case tabFiles:
		return a.renderFiles(w)
	case tabNotes:
		return a.notes.View()
	case tabPins:
		if len(s.Pins) == 0 {
			return dim.Render("No pins yet.\nSelect an entry in the history (tab) and press p.")
		}
		var lines []string
		for i, p := range s.Pins {
			line := ansi.Truncate("★ "+p.Excerpt, w, "…")
			if i == a.pinSel && a.focus == focusPins {
				line = selStyle.Render(ansi.Truncate(ansi.Strip(line)+strings.Repeat(" ", w), w, ""))
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n")
	}
	return a.vp.View()
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
	focusNewDir:      "Folder for the new session · enter continue · esc cancel",
	focusNewName:     "Session name · enter start · esc cancel",
	focusNewWorktree: "Name for worktree and branch, free text (e.g. login-fix, PROJ-123, feature/export) · enter create · esc cancel",
	focusSetting:     "enter save · esc cancel",
	focusOverlay:     "↑↓/pgup/pgdn scroll · esc close",
	focusConfirm:     "y confirm · any other key cancels",
	focusPick:        "↑↓ select · enter apply · esc cancel",
}

const helpText = `Writing (the message field always has the focus)
  enter            send
  shift+enter      new line (also alt+enter, ctrl+j or \ at the end of a line)
  ↑ / ↓            earlier messages of this session (in the empty field)
  /                search and run commands (also ctrl+p); entries marked
                   "claude" are Claude Code's own commands (e.g. /compact)
                   and are sent to Claude
  ?                this help (in the empty field)
  esc              clear the text, or interrupt Claude while it works
  ctrl+c           interrupt Claude / clear the text; twice: quit

Everywhere
  ctrl+k           search and switch sessions
  alt+↑ / alt+↓    previous / next session
  ctrl+n           new session (known repository or folder; in git:
                   directly, a new worktree with its own branch, or an
                   existing one)
  alt+1 … alt+4    tabs History, Files, Notes, Pins (also by click)
  ctrl+o           view: normal → all (nothing clipped) → compact
  tab / shift+tab  move between message field, right panel and session list
  pgup / pgdn      scroll the history

History (tab)
  ↑ / ↓            select an entry         enter    expand / clip
  y                copy (complete)         p        pin / unpin
  o                open the file in the IDE d       diff in the IDE
  esc              back to the message field

Session list (tab)
  ↑ / ↓            select a session        enter    write
  del              remove the session (for worktrees optionally with the
                   worktree; the branch is always kept)

Files (alt+2)
  ↑ / ↓  →/←       select a file, expand/collapse folders   enter  preview
  o / d            open in the IDE / diff in the IDE
  m                changed files only (M A ? D, ✎ = edited by Claude)

Notes (alt+3): just type, esc saves, ctrl+y copies everything
Pins (alt+4):  enter jumps to the entry, del removes the pin

Everything else via / or ctrl+p: model, effort, diff, pull, IDE, file
manager, GitHub, session link, terminal, stop/start, copy notes, quit.

Mouse
  click on session / tab / entry   select
  second click on an entry         expand
  click on the hint line           commands
  wheel                            scroll (over the list: switch session)
  shift + drag                     select text to copy it`
