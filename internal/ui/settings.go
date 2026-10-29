package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stefa/llm-cockpit/internal/core"
)

type pickOption struct{ label, value string }

// picker is a small selection list shown in the right panel.
type picker struct {
	title   string
	options []pickOption
	sel     int
	// apply handles the choice; a non-empty text becomes the status line,
	// the command (if any) runs in the background.
	apply func(value string) (string, tea.Cmd)
}

var modelOptions = []pickOption{
	{"Default (Claude setting)", ""},
	{"Opus 5.5", "claude-opus-5-5"},
	{"Sonnet 5.5", "claude-sonnet-5-5"},
	{"Haiku 5.5", "claude-haiku-5-5"},
	{"Fable 5.1", "claude-fable-5-1"},
}

var effortOptions = []pickOption{
	{"Default (Claude setting)", ""},
	{"low", "low"},
	{"medium", "medium"},
	{"high", "high"},
	{"xhigh", "xhigh"},
	{"max", "max"},
}

func optionLabel(opts []pickOption, value string) string {
	for _, o := range opts {
		if o.value == value {
			return o.label
		}
	}
	return value
}

func (a *Model) openPicker(title string, opts []pickOption, current string, apply func(string) (string, tea.Cmd)) {
	a.pick = picker{title: title, options: opts, apply: apply}
	for i, o := range opts {
		if o.value == current {
			a.pick.sel = i
		}
	}
	a.focus = focusPick
}

// choosePick applies option i. The apply function may open the next step
// (another picker or a prompt); otherwise the list gets the focus back.
func (a *Model) choosePick(i int) tea.Cmd {
	if i < 0 || i >= len(a.pick.options) {
		return nil
	}
	p := a.pick
	a.focus = focusInput
	msg, cmd := p.apply(p.options[i].value)
	if msg != "" {
		a.status = msg
	}
	a.refresh()
	return cmd
}

// settingApply wraps a manager setter for the model/effort pickers.
func (a *Model) settingApply(title string, opts []pickOption, set func(string) string) func(string) (string, tea.Cmd) {
	return func(v string) (string, tea.Cmd) {
		return title + ": " + optionLabel(opts, v) + " – " + set(v), nil
	}
}

func (a *Model) handlePickKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		a.focus = focusInput
	case "up", "k":
		a.pick.sel = max(a.pick.sel-1, 0)
	case "down", "j":
		a.pick.sel = min(a.pick.sel+1, len(a.pick.options)-1)
	case "enter", " ":
		return a, a.choosePick(a.pick.sel)
	}
	return a, nil
}

func (a *Model) renderPick(w int) string {
	lines := []string{bold.Render(a.pick.title) + dim.Render("  (enter select, esc cancel)")}
	for i, o := range a.pick.options {
		line := ansi.Truncate("  "+o.label, w, "…")
		if i == a.pick.sel {
			line = selStyle.Render(ansi.Truncate("▶ "+o.label+strings.Repeat(" ", w), w, ""))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// bar draws a usage bar of width w, colored by how full it is.
func bar(frac float64, w int) string {
	frac = min(max(frac, 0), 1)
	full := int(frac*float64(w) + 0.5)
	c := "10"
	switch {
	case frac >= 0.8:
		c = "9"
	case frac >= 0.5:
		c = "11"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(strings.Repeat("█", full)) +
		dim.Render(strings.Repeat("░", w-full))
}

var weekdays = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// resetText formats when a usage window resets: the time today, otherwise
// weekday and time.
func resetText(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	if !t.After(now) {
		return "now"
	}
	t = t.Local()
	if t.Sub(now) < 24*time.Hour && t.Day() == now.Local().Day() {
		return t.Format("15:04")
	}
	return weekdays[t.Weekday()] + " " + t.Format("15:04")
}

func untilText(t, now time.Time) string {
	d := t.Sub(now).Round(time.Minute)
	if d <= 0 {
		return ""
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h >= 24 {
		return fmt.Sprintf("in %dd %dh", h/24, h%24)
	}
	if h > 0 {
		return fmt.Sprintf("in %dh %dm", h, m)
	}
	return fmt.Sprintf("in %dm", m)
}

// usageLines renders the account usage for the bottom of the session list.
func usageLines(l core.Limits, now time.Time, w int) []string {
	if l.UpdatedAt.IsZero() {
		return nil
	}
	row := func(label string, win core.Window) string {
		s := fmt.Sprintf("%-6s %s %3.0f%%", label, bar(win.Utilization, 8), win.Utilization*100)
		if r := resetText(win.ResetsAt, now); r != "" {
			s += dim.Render(" ↺ " + r)
		}
		return ansi.Truncate(s, w, "…")
	}
	lines := []string{
		dim.Render(strings.Repeat("─", w)),
		row("5h", l.FiveHour),
		row("7 days", l.SevenDay),
	}
	if l.Status != "" && l.Status != "allowed" {
		lines = append(lines, errStyle.Render(ansi.Truncate("limit: "+l.Status, w, "…")))
	} else if u := untilText(l.FiveHour.ResetsAt, now); u != "" {
		lines = append(lines, dim.Render(ansi.Truncate("5h window resets "+u, w, "…")))
	}
	return lines
}

// contextText renders "Kontext ██░░ 49k/1M 5%" for a session.
func contextText(s core.Session) string {
	if s.ContextTokens == 0 {
		return ""
	}
	if s.ContextWindow == 0 {
		return "context " + shortTokens(s.ContextTokens)
	}
	frac := float64(s.ContextTokens) / float64(s.ContextWindow)
	return fmt.Sprintf("context %s %s/%s %.0f%%", bar(frac, 6),
		shortTokens(s.ContextTokens), shortTokens(s.ContextWindow), frac*100)
}

func shortTokens(n int) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return fmt.Sprintf("%dM", n/1_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
}

// modelText names the model of a session: the chosen one, else the one the
// running process reports.
func modelText(s core.Session) string {
	if s.Model != "" {
		return optionLabel(modelOptions, s.Model)
	}
	if s.ActiveModel != "" {
		return s.ActiveModel
	}
	return "default model"
}
