package ui

// Split view: the right area can show up to four sessions at once, like
// panes in Windows Terminal or tmux. The active pane is the normal right
// panel (tabs, selection, copy …) of the selected session; the other panes
// show the live history of their session. The message field always writes
// to the active pane.

import (
	"fmt"
	"slices"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stefa/llm-cockpit/internal/core"
)

type layoutKind struct {
	name  string
	panes int
}

var layouts = []layoutKind{
	{"1 session", 1},
	{"2 side by side", 2},
	{"2 stacked", 2},
	{"3 (1 + 2 stacked)", 3},
	{"4 (2×2)", 4},
}

// Smallest useful pane (inner size); smaller layouts fall back to 1 pane.
const minPaneW, minPaneH = 40, 10

// rect is a pane's outer box relative to the right area's top-left corner.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool { return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h }

// paneRects returns the pane boxes for the current layout and size, or one
// box for the whole area if the panes would be too small.
func (a *Model) paneRects() []rect {
	W, H := a.w-leftW-2, a.bodyHeight()+2
	full := []rect{{0, 0, W, H}}
	halfW, halfH := W/2, H/2
	var rs []rect
	switch a.layoutIdx {
	case 1:
		rs = []rect{{0, 0, halfW, H}, {halfW, 0, W - halfW, H}}
	case 2:
		rs = []rect{{0, 0, W, halfH}, {0, halfH, W, H - halfH}}
	case 3:
		rs = []rect{{0, 0, halfW, H}, {halfW, 0, W - halfW, halfH}, {halfW, halfH, W - halfW, H - halfH}}
	case 4:
		rs = []rect{{0, 0, halfW, halfH}, {halfW, 0, W - halfW, halfH}, {0, halfH, halfW, H - halfH}, {halfW, halfH, W - halfW, H - halfH}}
	default:
		return full
	}
	for _, r := range rs {
		if r.w-2 < minPaneW || r.h-2 < minPaneH {
			return full
		}
	}
	return rs
}

// split reports whether more than one pane is shown right now.
func (a *Model) split() bool { return len(a.paneRects()) > 1 }

// popupOpen reports whether a popup (choice list, overlay, command list,
// session search) takes over the whole right area.
func (a *Model) popupOpen() bool {
	return a.focus == focusPick || a.focus == focusOverlay || a.paletteOpen() || a.switching
}

// activeInner is the content size of the active pane.
func (a *Model) activeInner() (w, h int) {
	rs := a.paneRects()
	r := rs[min(a.activePane, len(rs)-1)]
	return max(r.w-2, 10), max(r.h-2, 3)
}

// cycleLayout switches 1 → 2 side by side → 2 stacked → 3 → 4 → 1.
func (a *Model) cycleLayout() {
	a.layoutIdx = (a.layoutIdx + 1) % len(layouts)
	a.fillPanes()
	a.applyLayout()
	if n := len(a.paneRects()); n < layouts[a.layoutIdx].panes {
		a.status = fmt.Sprintf("layout: %s – the window is too small, showing 1 session", layouts[a.layoutIdx].name)
	} else {
		a.status = "layout: " + layouts[a.layoutIdx].name + " (ctrl+\\ cycles · alt+arrows move between panes)"
	}
}

// applyLayout recalculates sizes after a layout change.
func (a *Model) applyLayout() {
	a.layout()
	a.renderPanes()
}

// fillPanes makes a.panes as long as the layout needs: the active session
// stays in its pane, free panes get sessions that are not shown yet.
func (a *Model) fillPanes() {
	n := layouts[a.layoutIdx].panes
	cur := ""
	if s, ok := a.selected(); ok {
		cur = s.ID
	}
	if len(a.panes) == 0 {
		a.panes = []string{cur}
	}
	if a.activePane >= n {
		// The active pane disappears: keep its session in the first pane.
		a.panes[0], a.activePane = cur, 0
	}
	a.panes = a.panes[:min(len(a.panes), n)]
	for len(a.panes) < n {
		next := ""
		for _, i := range a.displayOrder() {
			if id := a.sessions[i].ID; !slices.Contains(a.panes, id) {
				next = id
				break
			}
		}
		a.panes = append(a.panes, next)
	}
	if a.paneVPs == nil {
		a.paneVPs = map[int]*viewport.Model{}
	}
}

// syncPanes puts the selected session into the active pane; if it was shown
// in another pane already, the two panes swap.
func (a *Model) syncPanes(id string) {
	if len(a.panes) == 0 || a.activePane >= len(a.panes) {
		return
	}
	prev := a.panes[a.activePane]
	if prev == id {
		return
	}
	if j := slices.Index(a.panes, id); j >= 0 {
		a.panes[j] = prev
	}
	a.panes[a.activePane] = id
}

// activatePane makes pane i the active one (its session gets selected).
func (a *Model) activatePane(i int) tea.Cmd {
	if i == a.activePane || i >= len(a.panes) {
		return nil
	}
	a.activePane = i
	a.layout()
	for idx, s := range a.sessions {
		if s.ID == a.panes[i] && a.panes[i] != "" {
			a.sel = idx
			a.refresh()
			a.renderPanes()
			return a.sessionChanged()
		}
	}
	// An empty pane: the next session chosen in the list goes there.
	a.status = "empty pane – choose a session in the list (or ctrl+k)"
	a.renderPanes()
	return nil
}

// movePane moves the active pane in a direction (alt+arrows).
func (a *Model) movePane(dx, dy int) tea.Cmd {
	rs := a.paneRects()
	if len(rs) < 2 {
		return nil
	}
	cur := rs[min(a.activePane, len(rs)-1)]
	cx, cy := cur.x+cur.w/2, cur.y+cur.h/2
	best, bestD := -1, 1<<30
	for i, r := range rs {
		x, y := r.x+r.w/2, r.y+r.h/2
		ddx, ddy := x-cx, y-cy
		if (dx != 0 && ddx*dx <= 0) || (dy != 0 && ddy*dy <= 0) {
			continue
		}
		if d := ddx*ddx + ddy*ddy; d < bestD {
			best, bestD = i, d
		}
	}
	if best < 0 {
		return nil
	}
	return a.activatePane(best)
}

// renderPanes re-renders the inactive panes (after changes or resizes).
func (a *Model) renderPanes() {
	rs := a.paneRects()
	if len(rs) < 2 {
		return
	}
	for i, r := range rs {
		if i == a.activePane || i >= len(a.panes) {
			continue
		}
		vp := a.paneVPs[i]
		if vp == nil {
			v := viewport.New(0, 0)
			vp = &v
			a.paneVPs[i] = vp
		}
		atBottom := vp.AtBottom() || vp.TotalLineCount() <= vp.Height
		vp.Width, vp.Height = max(r.w-2, 10), max(r.h-3, 1)
		s, ok := a.mgr.Get(a.panes[i])
		if !ok {
			vp.SetContent("")
			continue
		}
		content, _ := a.renderEntries(s, vp.Width, -1, func(int) bool { return a.transcript })
		vp.SetContent(content)
		if atBottom {
			vp.GotoBottom()
		}
	}
}

// renderSplit draws all panes of the right area.
func (a *Model) renderSplit(rightFocused bool) string {
	rs := a.paneRects()
	boxes := make([]string, len(rs))
	for i, r := range rs {
		iw, ih := r.w-2, r.h-2
		c := lipgloss.Color("8")
		if i == a.activePane {
			c = accent
			if !rightFocused {
				c = lipgloss.Color("4") // active pane, keyboard elsewhere
			}
		}
		style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c).
			Width(iw).Height(ih).MaxHeight(r.h)
		var content string
		switch {
		case i == a.activePane:
			if s, ok := a.selected(); ok {
				content = a.renderHeader(s, iw) + "\n" + a.renderTab(s, iw)
			} else {
				content = dim.Render("empty pane – choose a session in the list")
			}
		case i < len(a.panes) && a.panes[i] != "" && a.paneVPs[i] != nil:
			if s, ok := a.mgr.Get(a.panes[i]); ok {
				content = a.paneHeader(i, s, iw) + "\n" + a.paneVPs[i].View()
				break
			}
			fallthrough
		default:
			content = dim.Render(fmt.Sprintf("%s empty – click here, then choose a session", paneMark(i)))
		}
		boxes[i] = style.Render(content)
	}
	switch a.layoutIdx {
	case 1:
		return lipgloss.JoinHorizontal(lipgloss.Top, boxes[0], boxes[1])
	case 2:
		return lipgloss.JoinVertical(lipgloss.Left, boxes[0], boxes[1])
	case 3:
		return lipgloss.JoinHorizontal(lipgloss.Top, boxes[0], lipgloss.JoinVertical(lipgloss.Left, boxes[1], boxes[2]))
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top, boxes[0], boxes[1]),
		lipgloss.JoinHorizontal(lipgloss.Top, boxes[2], boxes[3]))
}

// paneHeader is the one-line header of an inactive pane.
func (a *Model) paneHeader(i int, s core.Session, w int) string {
	h := paneMark(i) + " " + bold.Render(s.Name) + " " + statusStyle(s.Status).Render(statusIcon(s.Status)+" "+s.Status.String())
	if s.Status == core.StatusWorking && s.Activity != "" {
		h += dim.Render("  " + s.Activity)
	} else if c := contextText(s); c != "" {
		h += dim.Render("  ") + c
	}
	return ansi.Truncate(h, w, "…")
}

var paneMarks = []string{"①", "②", "③", "④"}

func paneMark(i int) string {
	if i < len(paneMarks) {
		return paneMarks[i]
	}
	return fmt.Sprint(i + 1)
}

// paneOf returns the pane number mark of a session shown in the split view.
func (a *Model) paneOf(id string) string {
	if !a.split() {
		return ""
	}
	for i, p := range a.panes {
		if p == id && i < len(a.paneRects()) {
			return paneMark(i)
		}
	}
	return ""
}

// clickPane handles a click in the split right area at (x, y) relative to
// it. It activates another pane or returns the content row/column inside
// the active pane for the normal click handling.
func (a *Model) clickPane(x, y int) (row, col int, handled bool, cmd tea.Cmd) {
	for i, r := range a.paneRects() {
		if !r.contains(x, y) {
			continue
		}
		if i != a.activePane {
			return 0, 0, true, a.activatePane(i)
		}
		return y - r.y - 1, x - r.x - 1, false, nil
	}
	return 0, 0, true, nil
}

// scrollPane scrolls an inactive pane under (x, y); false if none.
func (a *Model) scrollPane(x, y, delta int) bool {
	for i, r := range a.paneRects() {
		if r.contains(x, y) && i != a.activePane {
			if vp := a.paneVPs[i]; vp != nil {
				vp.SetYOffset(vp.YOffset + delta)
			}
			return true
		}
	}
	return false
}
