package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stefa/llm-cockpit/internal/core"
)

// Styles mimicking the Claude Code terminal output.
var (
	dotText    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Render("⏺ ")
	dotTool    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("⏺ ")
	dotSub     = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Render("⏺ ")
	toolName   = lipgloss.NewStyle().Bold(true)
	resultPipe = dim.Render("  ⎿  ")
	removed    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	added      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	turnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	thinkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
)

// Lines shown before an entry is cut off ("… +N Zeilen").
const (
	detailLines = 12
	resultLines = 4
)

// displayName maps tool names to the labels Claude Code uses.
func displayName(tool string) string {
	switch tool {
	case "Edit", "MultiEdit":
		return "Update"
	case "TodoWrite":
		return "Update Todos"
	}
	return tool
}

// wrap word-wraps text to width and prefixes the first line with first and
// all following lines with rest.
func wrap(first, rest, text string, width int) string {
	w := max(width-lipgloss.Width(first), 10)
	wrapped := strings.TrimRight(lipgloss.NewStyle().Width(w).Render(text), " ")
	lines := strings.Split(wrapped, "\n")
	for i := range lines {
		l := strings.TrimRight(lines[i], " ")
		if i == 0 {
			lines[i] = first + l
		} else {
			lines[i] = rest + l
		}
	}
	return strings.Join(lines, "\n")
}

// clip returns at most n lines of text (all if n <= 0) and how many were cut.
func clip(text string, n int) ([]string, int) {
	lines := strings.Split(strings.ReplaceAll(text, "\t", "    "), "\n")
	if n <= 0 || len(lines) <= n {
		return lines, 0
	}
	return lines[:n], len(lines) - n
}

func moreLine(prefix string, n int) string {
	return prefix + dim.Render(fmt.Sprintf("… +%d Zeilen (enter zeigt alles)", n))
}

// colorDiff colors "- " / "+ " lines as removed/added text.
func colorDiff(l string) string {
	switch {
	case strings.HasPrefix(l, "- "):
		return removed.Render(l)
	case strings.HasPrefix(l, "+ "):
		return added.Render(l)
	}
	return dim.Render(l)
}

// relTo shortens paths inside dir to a relative path.
func relTo(dir, p string) string {
	if dir == "" || p == "" {
		return p
	}
	if rel, err := filepath.Rel(dir, filepath.FromSlash(p)); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

// renderMessage draws one history entry. prevTool is the tool of the
// preceding tool call (for result summaries); expanded shows all lines.
func (a *Model) renderMessage(m core.Message, prevTool string, selected, expanded bool, width int) string {
	gutter := "  "
	if selected {
		gutter = "▶ "
	}
	indent := "  "
	if m.Sub {
		indent = dim.Render("│ ")
		gutter += dim.Render("│ ")
	}
	w := width - 2
	if m.Sub {
		w -= 2
	}
	limit := func(n int) int {
		if expanded {
			return 0
		}
		return n
	}

	switch m.Kind {
	case core.KindUser:
		return wrap(gutter+userStyle.Render("> "), indent+"  ", m.Text, width)

	case core.KindAssistant:
		text := m.Text
		if m.Partial {
			text += "▌"
		}
		dot := dotText
		if m.Sub {
			dot = dotSub
		}
		return wrap(gutter+dot, indent+"  ", text, width)

	case core.KindTool:
		arg := strings.TrimSpace(strings.TrimPrefix(m.Text, m.Tool))
		if m.File != "" {
			arg = relTo(a.cur.Dir, m.File)
		}
		head := gutter + dotTool + toolName.Render(displayName(m.Tool))
		if arg != "" && m.Tool != "TodoWrite" {
			head += "(" + arg + ")"
		}
		head = ansi.Truncate(head, width, "…")
		if selected && m.File != "" {
			head += dim.Render("  [c] IDE · [f] Diff")
		}
		// Short reads and searches need no body; everything else shows its input.
		showBody := a.details || expanded || m.Tool == "TodoWrite"
		if m.Tool == "Read" || m.Detail == "" || !showBody {
			return head
		}
		if (m.Tool == "Bash" || m.Tool == "PowerShell") && !expanded && lipgloss.Width(m.Detail) < width-10 && !strings.Contains(m.Detail, "\n") {
			return head // the command is already in the header
		}
		lines, more := clip(m.Detail, limit(detailLines))
		out := []string{head}
		for _, l := range lines {
			out = append(out, indent+"    "+ansi.Truncate(colorDiff(l), w-4, "…"))
		}
		if more > 0 {
			out = append(out, moreLine(indent+"    ", more))
		}
		return strings.Join(out, "\n")

	case core.KindResult:
		text := m.Detail
		if text == "" {
			text = m.Text
		}
		// Long file contents are summarised like Claude Code does.
		if prevTool == "Read" && !expanded {
			n := len(strings.Split(text, "\n"))
			return gutter + resultPipe + dim.Render(fmt.Sprintf("%d Zeilen gelesen", n))
		}
		n := resultLines
		if !a.details {
			n = 1
		}
		lines, more := clip(text, limit(n))
		style := dim
		if strings.HasPrefix(m.Text, "Fehler:") {
			style = errStyle
		}
		var out []string
		for i, l := range lines {
			prefix := indent + "     "
			if i == 0 {
				prefix = gutter + resultPipe
			}
			out = append(out, prefix+style.Render(ansi.Truncate(l, w-5, "…")))
		}
		if more > 0 {
			out = append(out, moreLine(indent+"     ", more))
		}
		return strings.Join(out, "\n")

	case core.KindThinking:
		return gutter + thinkStyle.Render("✻ "+m.Text)

	case core.KindTurn:
		return gutter + turnStyle.Render(ansi.Truncate("✓ "+m.Text, w-2, "…"))

	case core.KindDenied:
		return wrap(gutter+errStyle.Render("⛔ verweigert: "), indent+"   ", m.Text, width)

	default:
		return wrap(gutter+infoStyle.Render("● "), indent+"  ", infoStyle.Render(m.Text), width)
	}
}
