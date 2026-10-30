package ui

// Keyboard concept, following Claude Code and other well-known tools:
//
//   - The message field always has the focus by default (Claude Code).
//     "/" opens the command list, "?" the help, ↑ the previous prompt.
//   - esc closes popups or interrupts Claude; ctrl+c clears the input,
//     twice quits; ctrl+o switches the view (Claude Code).
//   - ctrl+p command list, ctrl+n new session (VS Code), alt+↑/↓ previous /
//     next session (Slack), alt+1…4 the tabs on the right (browser tabs),
//     tab / shift+tab move between areas.
//   - Panels have a few single-key actions shown in the hint line (lazygit);
//     any other typed character goes to the message field (Slack).

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stefa/llm-cockpit/internal/core"
	"github.com/stefa/llm-cockpit/internal/platform"
)

type tab int

const (
	tabHistory tab = iota
	tabFiles
	tabNotes
	tabPins
)

var tabNames = []string{"History", "Files", "Notes", "Pins"}

// command is one entry of the command list ("/" or ctrl+p).
type command struct {
	name, label, keys string
	action            string // action id for handleNav
	// claude marks Claude Code's own commands (reported at session start);
	// they are sent to Claude as a message instead of run by the cockpit.
	claude bool
}

var commands = []command{
	{"new", "New session", "ctrl+n", "n", false},
	{"model", "Choose the session's model", "", "M", false},
	{"effort", "Choose the session's effort", "", "E", false},
	{"view", "Switch view: normal / all / compact", "ctrl+o", "ctrl+o", false},
	{"history", "History tab", "alt+1", "tab:0", false},
	{"files", "Files tab", "alt+2", "tab:1", false},
	{"notes", "Notes tab", "alt+3", "tab:2", false},
	{"pins", "Pins tab", "alt+4", "tab:3", false},
	{"copy", "Copy the selected entry / last answer", "y in history", "y", false},
	{"copy-notes", "Copy the notes", "ctrl+y in notes", "Y", false},
	{"pin", "Pin the selected entry / last answer", "p in history", "p", false},
	{"diff", "Show the project's git diff", "", "f", false},
	{"pull", "git pull (fast-forward only)", "", "u", false},
	{"ide", "Open the project in the IDE", "", "c", false},
	{"explorer", "Open the folder in the file manager", "", "e", false},
	{"session", "Search and switch sessions", "ctrl+k", "switch", false},
	{"layout", "Split view: 1 · 2 side by side · 2 stacked · 3 · 4 sessions", "ctrl+\\", "layout", false},
	{"link", "Open the session link (config session_link + name)", "", "link", false},
	{"github", "Open GitHub (repository or search)", "", "g", false},
	{"terminal", "Continue the session in a Claude terminal (the cockpit follows it)", "", "t", false},
	{"observe", "Follow a Claude terminal session started outside the cockpit", "", "observe", false},
	{"takeover", "Continue an observed terminal session in the cockpit", "", "takeover", false},
	{"interrupt", "Interrupt Claude", "esc / ctrl+c", "interrupt", false},
	{"stop", "Stop the session", "", "x", false},
	{"start", "Start / resume the session", "", "r", false},
	{"remove", "Remove the session", "del in the list", "ctrl+d", false},
	{"settings", "Settings (IDE, permission mode, thinking, link, …)", "", "settings", false},
	{"help", "Help and keyboard shortcuts", "?", "?", false},
	{"quit", "Quit the cockpit", "ctrl+c ×2", "q", false},
}

// paletteQuery is the command name typed after "/" (arguments excluded).
func (a *Model) paletteQuery() string {
	q := strings.TrimPrefix(strings.TrimSpace(a.input.Value()), "/")
	if i := strings.IndexAny(q, " \n"); i >= 0 {
		q = q[:i]
	}
	return strings.ToLower(q)
}

// paletteMatches returns the cockpit commands and Claude Code's own
// commands matching the "/…" text in the input, best matches first: exact
// name, name prefix, name contains the text, description contains it.
func (a *Model) paletteMatches() []command {
	q := a.paletteQuery()
	all := slices.Clone(commands)
	for _, name := range a.mgr.ClaudeCommands() {
		if !strings.HasPrefix(name, "_") { // skip internal commands
			all = append(all, command{name: name, label: "Claude Code command, sent to Claude", claude: true})
		}
	}
	rank := func(c command) int {
		name := strings.ToLower(c.name)
		switch {
		case q == "":
			return 0
		case name == q:
			return 1
		case strings.HasPrefix(name, q):
			return 2
		case strings.Contains(name, q):
			return 3
		case strings.Contains(strings.ToLower(c.label), q):
			return 4
		}
		return -1
	}
	var out []command
	for _, c := range all {
		if rank(c) >= 0 {
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(x, y command) int { return rank(x) - rank(y) })
	return out
}

// sendClaudeCommand sends a Claude Code slash command as a message,
// including arguments typed after it ("/compact focus on the API").
func (a *Model) sendClaudeCommand(c command) tea.Cmd {
	text := "/" + c.name
	if typed := strings.TrimSpace(a.input.Value()); strings.EqualFold(a.paletteQuery(), c.name) {
		text = typed
	}
	a.input.SetValue("")
	a.paletteSel = 0
	a.fitInput()
	s, ok := a.selected()
	if !ok {
		a.status = "create a session first (ctrl+n)"
		return nil
	}
	if err := a.mgr.Send(s.ID, text); err != nil {
		a.status = "Sending failed: " + err.Error()
	}
	return nil
}

// paletteOpen reports whether the command list is shown.
func (a *Model) paletteOpen() bool {
	return a.focus == focusInput && !a.switching && strings.HasPrefix(a.input.Value(), "/")
}

func (a *Model) openPalette() tea.Cmd {
	a.focus = focusInput
	a.input.SetValue("/")
	a.input.CursorEnd()
	a.paletteSel = 0
	return a.input.Focus()
}

func (a *Model) runCommand(c command) (tea.Model, tea.Cmd) {
	if c.claude {
		return a, a.sendClaudeCommand(c)
	}
	a.input.SetValue("")
	a.fitInput()
	a.paletteSel = 0
	return a.runAction(c.action)
}

// runAction executes an action id from the command list or a panel key.
func (a *Model) runAction(action string) (tea.Model, tea.Cmd) {
	switch {
	case strings.HasPrefix(action, "tab:"):
		var t int
		fmt.Sscanf(action, "tab:%d", &t)
		return a, a.setTab(tab(t))
	case action == "interrupt":
		a.interrupt()
		return a, nil
	case action == "switch":
		return a, a.openSwitcher()
	case action == "settings":
		a.openSettings()
		return a, nil
	case action == "layout":
		a.cycleLayout()
		return a, nil
	case action == "observe":
		a.openObserve()
		return a, nil
	case action == "takeover":
		a.takeOver()
		return a, nil
	case action == "link":
		s, ok := a.selected()
		tmpl := a.mgr.Config().SessionLink
		switch {
		case !ok:
		case tmpl == "":
			a.status = `no link configured: set e.g. "session_link": "https://company.atlassian.net/browse/" in state.json`
		default:
			u := platform.SessionURL(tmpl, s.Name)
			a.report(platform.OpenURL(u), "browser: "+u)
		}
		return a, nil
	}
	return a.handleNav(action)
}

// openSwitcher starts the session search (ctrl+k, like Slack's quick
// switcher). The typed draft is kept and restored afterwards.
func (a *Model) openSwitcher() tea.Cmd {
	if len(a.sessions) == 0 {
		a.status = "no sessions yet (ctrl+n)"
		return nil
	}
	a.switching = true
	a.draft = a.input.Value()
	a.switchSel = 0
	a.focus = focusInput
	a.input.Prompt = "Session › "
	a.input.SetValue("")
	a.fitInput()
	return a.input.Focus()
}

func (a *Model) closeSwitcher() {
	a.switching = false
	a.input.Prompt = "› "
	a.input.SetValue(a.draft)
	a.draft = ""
	a.fitInput()
}

// switchMatches lists sessions matching the search text in name, folder,
// repository, branch or notes – in the order of the session list.
func (a *Model) switchMatches() []int {
	q := strings.ToLower(strings.TrimSpace(a.input.Value()))
	var out []int
	for _, i := range a.displayOrder() {
		s := a.sessions[i]
		g := a.git[s.Dir]
		hay := strings.ToLower(strings.Join([]string{s.Name, s.Dir, g.Root, g.Branch, s.Notes}, " "))
		if q == "" || strings.Contains(hay, q) {
			out = append(out, i)
		}
	}
	return out
}

func (a *Model) chooseSwitch(i int) tea.Cmd {
	a.closeSwitcher()
	a.sel = i
	a.refresh()
	return a.sessionChanged()
}

func (a *Model) handleSwitcherKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.closeSwitcher()
		return a, nil
	case "up":
		a.switchSel = max(a.switchSel-1, 0)
		return a, nil
	case "down":
		a.switchSel++
		return a, nil
	case "enter", "tab":
		if m := a.switchMatches(); len(m) > 0 {
			return a, a.chooseSwitch(m[min(a.switchSel, len(m)-1)])
		}
		return a, nil
	}
	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	a.switchSel = 0
	return a, cmd
}

func (a *Model) renderSwitcher(w int) string {
	matches := a.switchMatches()
	lines := []string{bold.Render("Switch session") + dim.Render("  (type to search name, folder, branch, notes · enter switch · esc back)")}
	if len(matches) == 0 {
		lines = append(lines, dim.Render("  no matching session"))
	}
	a.switchSel = max(min(a.switchSel, len(matches)-1), 0)
	for n, i := range matches {
		s := a.sessions[i]
		line := fmt.Sprintf("  %s %s", statusIcon(s.Status), s.Name)
		if g := a.git[s.Dir]; g.IsRepo {
			line += dim.Render("  ⎇ " + g.Branch)
		}
		line += dim.Render("  " + s.Dir)
		line = ansi.Truncate(line, w, "…")
		if n == a.switchSel {
			line = selStyle.Render(ansi.Truncate(ansi.Strip(line)+strings.Repeat(" ", w), w, ""))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

var claudeTag = lipgloss.NewStyle().Foreground(lipgloss.Color("208")).Bold(true).Render("claude ▸ ")

func (a *Model) renderPalette(w int) string {
	matches := a.paletteMatches()
	lines := []string{bold.Render("Commands") + dim.Render("  (type to filter · ↑↓ select · enter run · esc close · ") +
		claudeTag + dim.Render("= sent to Claude)")}
	if len(matches) == 0 {
		lines = append(lines, dim.Render("  no matching command"))
	}
	a.paletteSel = max(min(a.paletteSel, len(matches)-1), 0)
	// Scroll so the selection stays visible.
	rows := max(a.bodyHeight()-1, 1)
	if a.paletteSel < a.paletteTop {
		a.paletteTop = a.paletteSel
	}
	if a.paletteSel >= a.paletteTop+rows {
		a.paletteTop = a.paletteSel - rows + 1
	}
	a.paletteTop = max(min(a.paletteTop, len(matches)-rows), 0)
	for i := a.paletteTop; i < len(matches) && i < a.paletteTop+rows; i++ {
		c := matches[i]
		line := fmt.Sprintf("  /%-18s ", c.name)
		if c.claude {
			line += claudeTag + dim.Render(c.label)
		} else {
			line += c.label
			if c.keys != "" {
				line += dim.Render("  " + c.keys)
			}
		}
		line = ansi.Truncate(line, w, "…")
		if i == a.paletteSel {
			line = selStyle.Render(ansi.Truncate(ansi.Strip(line)+strings.Repeat(" ", w), w, ""))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// setTab shows a tab on the right; the panel gets the keyboard focus
// except for the history, where typing continues in the message field.
func (a *Model) setTab(t tab) tea.Cmd {
	s, ok := a.selected()
	if !ok {
		a.status = "create a session first (ctrl+n)"
		return nil
	}
	a.tab = t
	switch t {
	case tabFiles:
		return a.openFiles()
	case tabNotes:
		a.focus = focusNotes
		if a.notesFor != s.ID {
			a.saveNotes()
			a.notes.SetValue(s.Notes)
			a.notesFor = s.ID
		}
		return a.notes.Focus()
	case tabPins:
		a.focus = focusPins
		a.pinSel = max(min(a.pinSel, len(s.Pins)-1), 0)
		return nil
	}
	a.focus = focusInput
	a.renderMessages()
	return a.input.Focus()
}

// saveNotes stores the notes editor into the session it was loaded for
// (which is not necessarily the selected one after a session switch).
func (a *Model) saveNotes() bool {
	if a.notesFor == "" {
		return false
	}
	a.mgr.SetNotes(a.notesFor, a.notes.Value())
	return true
}

// backToInput leaves a panel: notes are saved, the history tab is shown
// again and the message field gets the focus (esc).
func (a *Model) backToInput() tea.Cmd {
	if a.focus == focusNotes || a.tab == tabNotes {
		if a.saveNotes() {
			a.status = "notes saved"
		}
		a.notesFor = ""
		a.notes.Blur()
	}
	a.tab = tabHistory
	a.focus = focusInput
	a.refresh()
	return a.input.Focus()
}

func (a *Model) interrupt() {
	s, ok := a.selected()
	if !ok {
		return
	}
	if a.mgr.Interrupt(s.ID) {
		a.status = "interrupting Claude"
	}
}

// isPrintable reports whether a key types a character.
func isPrintable(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyRunes && !msg.Alt
}

// toInput moves the focus to the message field and types msg there.
func (a *Model) toInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.focus == focusMessages || a.focus == focusList {
		a.renderMessages()
	}
	a.focus = focusInput
	focusCmd := a.input.Focus()
	_, cmd := a.handleInputKey(msg)
	return a, tea.Batch(focusCmd, cmd)
}

// handleGlobalKey handles keys that work everywhere; ok is false if the key
// is not a global one.
func (a *Model) handleGlobalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		// Like Claude Code: clear the text, else interrupt a working
		// session, else quit on the second press.
		if a.focus == focusInput && a.input.Value() != "" {
			a.input.SetValue("")
			a.fitInput()
			a.quitArmed = time.Time{}
			return a, nil, true
		}
		if a.cur.Status == core.StatusWorking {
			a.interrupt()
			a.quitArmed = time.Time{}
			return a, nil, true
		}
		if time.Since(a.quitArmed) < 2*time.Second {
			a.mgr.StopAll()
			return a, tea.Quit, true
		}
		a.quitArmed = time.Now()
		a.status = "press ctrl+c again to quit"
		return a, nil, true
	case "ctrl+o":
		a.cycleView()
		return a, nil, true
	case "ctrl+p":
		return a, a.openPalette(), true
	case "ctrl+n":
		return a, a.newSession(), true
	case "ctrl+k":
		return a, a.openSwitcher(), true
	case "ctrl+\\":
		a.cycleLayout()
		return a, nil, true
	case "alt+left", "alt+right":
		// Split view: move between panes (like Windows Terminal).
		if a.split() {
			return a, a.movePane(map[bool]int{true: -1, false: 1}[msg.String() == "alt+left"], 0), true
		}
		return a, nil, false
	case "alt+up", "alt+down":
		d := 1
		if msg.String() == "alt+up" {
			d = -1
		}
		if a.split() {
			return a, a.movePane(0, d), true
		}
		if a.moveSelection(d) {
			a.refresh()
			return a, a.sessionChanged(), true
		}
		return a, nil, true
	case "alt+1", "alt+2", "alt+3", "alt+4":
		return a, a.setTab(tab(msg.String()[4] - '1')), true
	}
	return a, nil, false
}

// handleInputKey handles keys while the message field has the focus.
func (a *Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if a.switching {
		return a.handleSwitcherKey(msg)
	}
	if a.paletteOpen() {
		switch key {
		case "esc":
			a.input.SetValue("")
			return a, nil
		case "up":
			a.paletteSel = max(a.paletteSel-1, 0)
			return a, nil
		case "down":
			a.paletteSel++
			return a, nil
		case "enter", "tab":
			if m := a.paletteMatches(); len(m) > 0 {
				return a.runCommand(m[min(a.paletteSel, len(m)-1)])
			}
			return a, nil
		}
		var cmd tea.Cmd
		a.input, cmd = a.input.Update(msg)
		a.paletteSel = 0
		return a, cmd
	}
	empty := a.input.Value() == ""
	switch {
	case key == "esc":
		if a.input.Value() != "" {
			a.input.SetValue("")
		} else {
			a.interrupt()
		}
		return a, nil
	case key == "?" && empty:
		return a.handleNav("?")
	case key == "up" && !empty && a.histPos < 0 && strings.Contains(a.input.Value(), "\n"):
		// Multi-line text: arrows move the cursor inside the field.
	case (key == "up" || key == "down") && (empty || a.histPos >= 0):
		a.browseHistory(key == "up")
		return a, nil
	case key == "pgup":
		a.vp.HalfPageUp()
		return a, nil
	case key == "pgdown":
		a.vp.HalfPageDown()
		return a, nil
	case key == "tab":
		return a, a.cycleFocus(1)
	case key == "shift+tab":
		return a, a.cycleFocus(-1)
	case key == "enter":
		// New line: shift+enter (Windows) or a trailing backslash (any
		// terminal, like Claude Code); alt+enter and ctrl+j are bound in
		// the field's key map.
		if platform.ShiftPressed() {
			a.input.InsertString("\n")
			a.fitInput()
			return a, nil
		}
		if v := a.input.Value(); strings.HasSuffix(v, "\\") {
			a.input.SetValue(strings.TrimSuffix(v, "\\") + "\n")
			a.fitInput()
			return a, nil
		}
		text := strings.TrimSpace(a.input.Value())
		if s, ok := a.selected(); ok && text != "" {
			if err := a.mgr.Send(s.ID, text); err != nil {
				a.status = "Sending failed: " + err.Error()
			}
			a.input.SetValue("")
			a.histPos = -1
			a.fitInput()
		} else if !ok && text != "" {
			a.status = "create a session first (ctrl+n)"
		}
		return a, nil
	}
	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	a.histPos = -1
	a.fitInput()
	return a, cmd
}

// browseHistory steps through the prompts sent in this session (↑/↓ in an
// empty message field, like a shell or Claude Code).
func (a *Model) browseHistory(back bool) {
	var prompts []string
	for _, m := range a.cur.Messages {
		if m.Kind == core.KindUser && !m.Sub {
			prompts = append(prompts, m.Text)
		}
	}
	if len(prompts) == 0 {
		return
	}
	if a.histPos < 0 {
		a.histPos = len(prompts)
	}
	if back {
		a.histPos = max(a.histPos-1, 0)
	} else {
		a.histPos++
	}
	if a.histPos >= len(prompts) {
		a.histPos = -1
		a.input.SetValue("")
		return
	}
	a.input.SetValue(prompts[a.histPos])
	a.fitInput()
}

// cycleFocus moves between message field, right panel and session list.
func (a *Model) cycleFocus(d int) tea.Cmd {
	right := focusMessages
	switch a.tab {
	case tabFiles:
		right = focusFiles
	case tabNotes:
		right = focusNotes
	case tabPins:
		right = focusPins
	}
	order := []focus{focusInput, right, focusList}
	pos := 0
	for i, f := range order {
		if f == a.focus {
			pos = i
		}
	}
	next := order[(pos+d+len(order))%len(order)]
	if a.focus == focusNotes && next != focusNotes {
		a.saveNotes()
		a.notes.Blur()
	}
	a.focus = next
	switch next {
	case focusInput:
		a.renderMessages()
		return a.input.Focus()
	case focusMessages:
		if a.msgCursor < 0 || a.msgCursor >= len(a.cur.Messages) {
			a.msgCursor = len(a.cur.Messages) - 1
		}
		a.input.Blur()
		a.renderMessages()
	case focusNotes:
		return a.notes.Focus()
	default:
		a.input.Blur()
		a.renderMessages()
	}
	return nil
}

// handlePanelKey handles keys in the history, the session list and the pin
// list. Unused printable keys go to the message field.
func (a *Model) handlePanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		return a, a.backToInput()
	case "tab":
		return a, a.cycleFocus(1)
	case "shift+tab":
		return a, a.cycleFocus(-1)
	}
	switch a.focus {
	case focusMessages:
		switch key {
		case "up", "down", "pgup", "pgdown", "enter":
			return a.handleNav(key)
		case "y", "p":
			return a.handleNav(key)
		case "o":
			return a.handleNav("c") // open the entry's file in the IDE
		case "d":
			return a.handleNav("f") // diff of the entry's file in the IDE
		}
	case focusList:
		switch key {
		case "up", "down":
			return a.handleNav(key)
		case "enter":
			a.focus = focusInput
			a.renderMessages()
			return a, a.input.Focus()
		case "delete":
			return a.handleNav("ctrl+d")
		}
	case focusPins:
		pins := a.cur.Pins
		switch key {
		case "up":
			a.pinSel = max(a.pinSel-1, 0)
		case "down":
			a.pinSel = min(a.pinSel+1, len(pins)-1)
		case "delete", "x":
			if a.pinSel < len(pins) {
				a.mgr.TogglePin(a.cur.ID, pins[a.pinSel].UUID, "")
				a.refresh()
				a.pinSel = max(min(a.pinSel, len(a.cur.Pins)-1), 0)
			}
		case "enter":
			if a.pinSel < len(pins) {
				a.jumpTo(pins[a.pinSel].UUID)
			}
		default:
			if isPrintable(msg) {
				return a.toInput(msg)
			}
		}
		return a, nil
	}
	if isPrintable(msg) {
		return a.toInput(msg)
	}
	return a, nil
}

// switchHint names the keys for moving between sessions or panes.
func (a *Model) switchHint() string {
	if a.split() {
		return "alt+arrows pane · ctrl+\\ layout"
	}
	return "alt+↑↓ session · ctrl+\\ split"
}

// hintLine is the single line below the message field.
func (a *Model) hintLine() string {
	var h string
	switch a.focus {
	case focusInput:
		switch {
		case a.switching:
			h = "type to search · ↑↓ select · enter switch · esc back"
		case a.paletteOpen():
			h = "type to filter · ↑↓ select · enter run · esc close"
		case a.cur.Observe:
			h = observedHint(a.cur) + " · / commands · " + a.switchHint()
		case a.cur.Status == core.StatusWorking:
			h = "esc / ctrl+c interrupt · / commands · ? help · tab history · " + a.switchHint() + " · ctrl+o view"
		default:
			h = "enter send · / commands · ? help · ↑ last message · tab history · " + a.switchHint() + " · ctrl+o view"
		}
	case focusMessages:
		h = "↑↓ entry · enter expand · y copy · p pin · o open file in IDE · d diff in IDE · esc back"
	case focusList:
		h = "↑↓ session · enter write · del remove · esc back · ctrl+n new"
	case focusFiles:
		h = "↑↓ file · enter open · →← expand/collapse · o open in IDE · d diff in IDE · m changes only · esc back"
	case focusNotes:
		h = "write notes · esc save and back · ctrl+y copy all"
	case focusPins:
		h = "↑↓ pin · enter jump to entry · del remove · esc back"
	default:
		h = helpLine[a.focus]
	}
	right := "ctrl+k session · ctrl+p commands"
	gap := a.w - lipgloss.Width(h) - lipgloss.Width(right) - 1
	if gap < 1 {
		return dim.Render(ansi.Truncate(h, a.w, "…"))
	}
	return dim.Render(h + strings.Repeat(" ", gap+1) + right)
}

// renderTabs draws the tab bar and remembers the click ranges.
func (a *Model) renderTabs(s sessionInfo) string {
	var parts []string
	a.tabX = a.tabX[:0]
	x := 0
	for i, name := range tabNames {
		label := name
		switch tab(i) {
		case tabNotes:
			if s.hasNotes {
				label += " ✎"
			}
		case tabPins:
			if s.pins > 0 {
				label += fmt.Sprintf(" %d", s.pins)
			}
		}
		label = " " + label + " "
		style := dim
		if tab(i) == a.tab {
			style = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(accent)
		}
		parts = append(parts, style.Render(label))
		w := lipgloss.Width(label)
		a.tabX = append(a.tabX, [2]int{x, x + w})
		x += w + 1
	}
	return strings.Join(parts, dim.Render("│"))
}

type sessionInfo struct {
	hasNotes bool
	pins     int
}

// tabAt returns the tab at column x of the tab bar (relative to the panel).
func (a *Model) tabAt(x int) (tab, bool) {
	for i, r := range a.tabX {
		if x >= r[0] && x < r[1] {
			return tab(i), true
		}
	}
	return 0, false
}
