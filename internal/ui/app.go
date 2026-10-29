// Package ui is the Bubble Tea terminal frontend on top of core.Manager.
package ui

import (
	"fmt"
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
	histPos    int
	quitArmed  time.Time
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
		return a.routeKey(msg)
	case tea.MouseMsg:
		return a.handleMouse(msg)
	}
	return a, nil
}

// routeKey dispatches a key: popups and dialogs first, then global keys,
// then the focused area.
func (a *Model) routeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch a.focus {
	case focusPick, focusOverlay, focusConfirm, focusNewDir, focusNewName, focusNewWorktree:
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
		return "alles"
	case a.details:
		return "normal"
	}
	return "kompakt"
}

// cycleView switches the history view: normal → alles → kompakt → normal.
// "alles" shows every entry completely (like ctrl+o in Claude Code),
// "kompakt" only headers and one result line.
func (a *Model) cycleView() {
	switch a.viewName() {
	case "normal":
		a.details, a.transcript = true, true
	case "alles":
		a.details, a.transcript = false, false
	default:
		a.details, a.transcript = true, false
	}
	a.status = "Ansicht: " + a.viewName() + " (ctrl+o schaltet weiter: normal → alles → kompakt)"
	a.renderMessages()
}

// newMessageField creates the multi-line message field. Enter sends (see
// handleInputKey); new lines come from shift+enter, alt+enter, ctrl+j or a
// trailing backslash.
func newMessageField() textarea.Model {
	in := textarea.New()
	in.Prompt = "› "
	in.Placeholder = "Nachricht an die Session · enter senden · shift+enter neue Zeile · / Befehle · ? Hilfe"
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
	case focusConfirm, focusNewDir, focusNewName, focusNewWorktree:
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
		if m := a.paletteMatches(); row-1 < len(m) {
			return a.runCommand(m[row-1])
		}
		return a, nil
	}

	switch {
	case inLeft && row >= 0:
		if row < a.listRows && a.rowMap[row] >= 0 {
			// Selecting a session by mouse makes it ready for typing.
			idx := a.rowMap[row]
			cmds := []tea.Cmd{a.leaveForInput()}
			if idx != a.sel {
				a.sel = idx
				a.refresh()
				cmds = append(cmds, a.sessionChanged())
			}
			a.renderMessages()
			return a, tea.Batch(cmds...)
		}
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

// leaveForInput gives the message field the focus (saving open notes).
func (a *Model) leaveForInput() tea.Cmd {
	if a.focus == focusNotes {
		if s, ok := a.selected(); ok {
			a.mgr.SetNotes(s.ID, a.notes.Value())
		}
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
	case focusNewDir, focusNewName, focusNewWorktree:
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
					a.status = "Ordner existiert nicht: " + val
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
			a.status = "abgebrochen"
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
	case "y":
		i, ok := a.copyTarget()
		if !ok {
			a.status = "nichts zum Kopieren (tab, Eintrag wählen, dann y)"
			break
		}
		text := copyText(s.Messages[i])
		if err := platform.CopyToClipboard(text); err != nil {
			a.status = "Kopieren fehlgeschlagen: " + err.Error()
		} else {
			a.status = fmt.Sprintf("kopiert: %d Zeichen, %d Zeilen", len([]rune(text)), strings.Count(text, "\n")+1)
		}
	case "ctrl+o": // from the button bar
		a.cycleView()
	case "r":
		if err := a.mgr.Start(s.ID); err != nil {
			a.status = "Start fehlgeschlagen: " + err.Error()
		}
		a.refresh()
	case "x":
		a.mgr.Stop(s.ID)
		a.status = "Session gestoppt (mit r oder einer Nachricht fortsetzen)"
	case "ctrl+d":
		a.confirmRemove(s)
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
	case "Y":
		a.status = a.copyNotes(s.Notes)
	case "o":
		return a, a.setTab(tabNotes)
	case "p":
		i, ok := a.pinTarget()
		if !ok {
			a.status = "nichts zum Pinnen (tab, Eintrag wählen, dann p)"
			break
		}
		key := core.MessageKeys(s.Messages)[i]
		if a.mgr.TogglePin(s.ID, key, pinLabel(s.Messages[i])) {
			a.status = "gepinnt"
		} else {
			a.status = "Pin entfernt"
		}
		a.refresh()
	case "P":
		return a, a.setTab(tabPins)
	case "e":
		a.report(platform.OpenFolder(s.Dir), "Explorer geöffnet")
	case "c":
		if f, ok := a.cursorFile(); ok {
			a.report(platform.OpenIDE(a.mgr.Config().IDE, s.Dir, f), "In der IDE geöffnet: "+f)
			break
		}
		a.report(platform.OpenIDE(a.mgr.Config().IDE, s.Dir, s.Dir), "IDE gestartet")
	case "d":
		return a, a.setTab(tabFiles)
	case "M":
		id := s.ID
		a.openPicker("Modell – "+s.Name, modelOptions, s.Model,
			a.settingApply("Modell", modelOptions, func(v string) string { return a.mgr.SetModel(id, v) }))
	case "E":
		id := s.ID
		a.openPicker("Effort – "+s.Name, effortOptions, s.Effort,
			a.settingApply("Effort", effortOptions, func(v string) string { return a.mgr.SetEffort(id, v) }))
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
		return "keine Notizen zum Kopieren"
	}
	if err := platform.CopyToClipboard(notes); err != nil {
		return "Kopieren fehlgeschlagen: " + err.Error()
	}
	return fmt.Sprintf("Notizen kopiert (%d Zeilen)", strings.Count(strings.TrimRight(notes, "\n"), "\n")+1)
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
	a.status = "Stelle nicht im Verlauf gefunden (z. B. Abschlusszeile vor einem Neustart)"
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
			block = first + dim.Render("  [y] kopieren")
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
	inputFocused := a.focus == focusInput || a.focus == focusNewDir || a.focus == focusNewName || a.focus == focusNewWorktree
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
		return dim.Render("Keine Sessions.\n\nctrl+n = neue Session")
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
			dim.Render("ctrl+n startet eine neue Session.\n/ zeigt alle Befehle, ? die Hilfe.")
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
		info = append(info, "Effort "+s.Effort)
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
			return dim.Render("Noch keine Pins.\nIm Verlauf (tab) einen Eintrag wählen und p drücken.")
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
	focusNewDir:      "Ordner für die neue Session · enter weiter · esc abbrechen",
	focusNewName:     "Name der Session · enter starten · esc abbrechen",
	focusNewWorktree: "Name für Worktree und Branch, frei wählbar (z. B. login-fix, PROJ-123, feature/export) · enter anlegen · esc abbrechen",
	focusOverlay:     "↑↓/pgup/pgdn scrollen · esc schließen",
	focusConfirm:     "y bestätigen · andere Taste abbrechen",
	focusPick:        "↑↓ wählen · enter übernehmen · esc abbrechen",
}

const helpText = `Schreiben (das Nachrichtenfeld ist immer aktiv)
  enter            senden
  shift+enter      neue Zeile (auch alt+enter, ctrl+j oder \ am Zeilenende)
  ↑ / ↓            frühere Nachrichten dieser Session (im leeren Feld)
  /                Befehle suchen und ausführen (auch ctrl+p)
  ?                diese Hilfe (im leeren Feld)
  esc              Text löschen bzw. Claude unterbrechen
  ctrl+c           Text löschen, zweimal: Cockpit beenden

Überall
  ctrl+k           Session suchen und wechseln
  alt+↑ / alt+↓    vorherige / nächste Session
  ctrl+n           neue Session (bekanntes Repo oder Ordner; bei git:
                   direkt, neuer Worktree mit eigenem Branch oder vorhandener)
  alt+1 … alt+4    Reiter Verlauf, Dateien, Notizen, Pins (auch Klick)
  ctrl+o           Ansicht: normal → alles (nichts gekürzt) → kompakt
  tab / shift+tab  zwischen Eingabe, rechtem Bereich und Session-Liste
  pgup / pgdn      Verlauf scrollen

Verlauf (tab)
  ↑ / ↓            Eintrag wählen          enter    ganz zeigen / kürzen
  y                kopieren (vollständig)  p        pinnen / entpinnen
  o                Datei in der IDE öffnen d        Diff in der IDE
  esc              zurück zum Nachrichtenfeld

Session-Liste (tab)
  ↑ / ↓            Session wählen          enter    schreiben
  entf             Session entfernen (bei Worktrees wahlweise mit Worktree;
                   der Branch bleibt immer erhalten)

Dateien (alt+2)
  ↑ / ↓  →/←       Datei wählen, Ordner auf/zu    enter  Vorschau
  o / d            in der IDE öffnen / Diff in der IDE
  m                nur geänderte Dateien (M A ? D, ✎ = von Claude bearbeitet)

Notizen (alt+3): schreiben, esc speichert, ctrl+y kopiert alles
Pins (alt+4):    enter springt zur Stelle, entf entfernt den Pin

Alles andere über / bzw. ctrl+p: Modell, Effort, Diff, Pull, IDE, Explorer,
GitHub, Session-Link, Terminal, Stop/Start, Notizen kopieren, Beenden.

Maus
  Klick auf Session / Reiter / Eintrag   auswählen
  2. Klick auf Eintrag                   ganz zeigen
  Klick auf die Hinweiszeile unten       Befehle
  Mausrad                                scrollen (über der Liste: Session)
  Shift + Ziehen                         Text markieren und kopieren`
