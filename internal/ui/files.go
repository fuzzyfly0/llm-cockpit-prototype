package ui

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/stefa/llm-cockpit/internal/platform"
)

type fileNode struct {
	name, path string // path is relative to the session dir, with slashes
	dir        bool
	status     string
	touched    bool
	changed    bool // this node or a descendant is modified/touched
	children   []*fileNode
}

type fileRow struct {
	node  *fileNode
	depth int
}

type filesMsg struct {
	dir   string
	files []platform.FileEntry
	err   error
}

// fileView is the state of the file overview of the selected session.
type fileView struct {
	dir         string
	root        *fileNode
	expanded    map[string]bool
	onlyChanged bool
	sel, top    int
	rows        []fileRow
	changed     int
	total       int
	err         error
}

func buildTree(files []platform.FileEntry, touched map[string]bool) (*fileNode, int) {
	root := &fileNode{dir: true}
	index := map[string]*fileNode{"": root}
	changed := 0
	for _, f := range files {
		parts := strings.Split(f.Path, "/")
		parent := root
		for i := range parts[:len(parts)-1] {
			p := strings.Join(parts[:i+1], "/")
			n, ok := index[p]
			if !ok {
				n = &fileNode{name: parts[i], path: p, dir: true}
				index[p] = n
				parent.children = append(parent.children, n)
			}
			parent = n
		}
		leaf := &fileNode{name: parts[len(parts)-1], path: f.Path, status: f.Status, touched: touched[f.Path]}
		parent.children = append(parent.children, leaf)
		if leaf.status != "" || leaf.touched {
			changed++
			leaf.changed = true
			for p := path.Dir(f.Path); p != "." && p != "/"; p = path.Dir(p) {
				index[p].changed = true
			}
			root.changed = true
		}
	}
	sortTree(root)
	return root, changed
}

func sortTree(n *fileNode) {
	sort.Slice(n.children, func(i, j int) bool {
		a, b := n.children[i], n.children[j]
		if a.dir != b.dir {
			return a.dir
		}
		return strings.ToLower(a.name) < strings.ToLower(b.name)
	})
	for _, c := range n.children {
		if c.dir {
			sortTree(c)
		}
	}
}

func (v *fileView) rebuild() {
	keep := ""
	if v.sel >= 0 && v.sel < len(v.rows) {
		keep = v.rows[v.sel].node.path
	}
	v.rows = v.rows[:0]
	var walk func(n *fileNode, depth int)
	walk = func(n *fileNode, depth int) {
		for _, c := range n.children {
			if v.onlyChanged && !c.changed {
				continue
			}
			v.rows = append(v.rows, fileRow{c, depth})
			if c.dir && (v.onlyChanged || v.expanded[c.path]) {
				walk(c, depth+1)
			}
		}
	}
	if v.root != nil {
		walk(v.root, 0)
	}
	for i, r := range v.rows {
		if r.node.path == keep {
			v.sel = i
		}
	}
	v.sel = max(min(v.sel, len(v.rows)-1), 0)
}

func (v *fileView) current() (*fileNode, bool) {
	if v.sel < 0 || v.sel >= len(v.rows) {
		return nil, false
	}
	return v.rows[v.sel].node, true
}

func filesCmd(dir string) tea.Cmd {
	return func() tea.Msg {
		files, err := platform.ListFiles(dir)
		return filesMsg{dir, files, err}
	}
}

// openFiles shows the file overview of the selected session.
func (a *Model) openFiles() tea.Cmd {
	s, ok := a.selected()
	if !ok {
		return nil
	}
	if a.files.dir != s.Dir {
		a.files = fileView{dir: s.Dir, expanded: map[string]bool{}}
	}
	a.focus = focusFiles
	return filesCmd(s.Dir)
}

func (a *Model) applyFiles(msg filesMsg) {
	if msg.dir != a.files.dir {
		return
	}
	touched := map[string]bool{}
	for _, f := range a.cur.TouchedFiles() {
		if rel, err := filepath.Rel(msg.dir, f); err == nil && !strings.HasPrefix(rel, "..") {
			touched[filepath.ToSlash(rel)] = true
		}
	}
	first := a.files.root == nil
	a.files.root, a.files.changed = buildTree(msg.files, touched)
	a.files.total = len(msg.files)
	a.files.err = msg.err
	if first {
		// Start with every folder that contains changes opened.
		var open func(n *fileNode)
		open = func(n *fileNode) {
			for _, c := range n.children {
				if c.dir && c.changed {
					a.files.expanded[c.path] = true
					open(c)
				}
			}
		}
		open(a.files.root)
	}
	a.files.rebuild()
}

func (a *Model) filesHeight() int { return max(a.bodyHeight()-1, 1) }

func (a *Model) renderFiles(w int) string {
	v := &a.files
	s, _ := a.selected()
	mode := "alle"
	if v.onlyChanged {
		mode = "nur Änderungen"
	}
	head := bold.Render("Dateien – "+s.Name) +
		dim.Render(fmt.Sprintf("  %d Dateien, %d geändert · Ansicht: %s", v.total, v.changed, mode))
	lines := []string{ansi.Truncate(head, w, "…")}
	if v.root == nil {
		return lines[0] + "\n" + dim.Render("lade…")
	}
	if v.err != nil {
		lines = append(lines, infoStyle.Render(v.err.Error()))
	}
	h := a.filesHeight() - (len(lines) - 1)
	if v.sel < v.top {
		v.top = v.sel
	}
	if v.sel >= v.top+h {
		v.top = v.sel - h + 1
	}
	for i := v.top; i < len(v.rows) && i < v.top+h; i++ {
		lines = append(lines, renderFileRow(v.rows[i], v.expanded, v.onlyChanged, i == v.sel, w))
	}
	if len(v.rows) == 0 {
		lines = append(lines, dim.Render("keine Dateien"))
	}
	return strings.Join(lines, "\n")
}

var statusColors = map[string]string{"M": "11", "A": "10", "?": "10", "D": "9", "R": "14"}

func renderFileRow(r fileRow, expanded map[string]bool, onlyChanged, selected bool, w int) string {
	n := r.node
	mark := " "
	if n.status != "" {
		mark = lipgloss.NewStyle().Foreground(lipgloss.Color(statusColors[n.status])).Render(n.status)
	}
	touched := " "
	if n.touched {
		touched = llmStyle.Render("✎")
	}
	icon, name := "  ", n.name
	if n.dir {
		icon = "▸ "
		if expanded[n.path] || onlyChanged {
			icon = "▾ "
		}
		name += "/"
	}
	line := mark + touched + " " + strings.Repeat("  ", r.depth) + icon + name
	if n.dir && n.changed {
		line = mark + touched + " " + strings.Repeat("  ", r.depth) + icon + pinStyle.Render(name)
	}
	line = ansi.Truncate(line, w, "…")
	if selected {
		line = selStyle.Render(ansi.Strip(line) + strings.Repeat(" ", max(w-lipgloss.Width(line), 0)))
	}
	return line
}

func (a *Model) handleFilesKey(key string) (tea.Model, tea.Cmd) {
	v := &a.files
	n, ok := v.current()
	switch key {
	case "esc", "q", "d":
		a.focus = focusList
	case "up", "k":
		v.sel = max(v.sel-1, 0)
	case "down", "j":
		v.sel = min(v.sel+1, len(v.rows)-1)
	case "pgup":
		v.sel = max(v.sel-a.filesHeight()/2, 0)
	case "pgdown":
		v.sel = max(min(v.sel+a.filesHeight()/2, len(v.rows)-1), 0)
	case "home":
		v.sel = 0
	case "end":
		v.sel = max(len(v.rows)-1, 0)
	case "m":
		v.onlyChanged = !v.onlyChanged
		v.rebuild()
	case "r":
		return a, filesCmd(v.dir)
	case "right", "l":
		if ok && n.dir {
			v.expanded[n.path] = true
			v.rebuild()
		}
	case "left", "h":
		if !ok {
			break
		}
		if n.dir && v.expanded[n.path] {
			delete(v.expanded, n.path)
			v.rebuild()
			break
		}
		parent := path.Dir(n.path)
		for i, r := range v.rows {
			if r.node.path == parent {
				v.sel = i
			}
		}
	case "enter", " ":
		return a, a.activateFile()
	case "c":
		if ok {
			a.report(platform.OpenIDE(a.mgr.Config().IDE, v.dir, a.absPath(n.path)), "In IDE geöffnet: "+n.path)
		}
	case "e":
		if ok {
			dir := a.absPath(n.path)
			if !n.dir {
				dir = filepath.Dir(dir)
			}
			a.report(platform.OpenFolder(dir), "Explorer geöffnet")
		}
	case "f":
		if ok && !n.dir {
			a.report(platform.OpenIDEDiff(a.mgr.Config().IDE, v.dir, a.absPath(n.path)), "Diff in der IDE: "+n.path)
		}
	case "F":
		if ok && !n.dir {
			dir, rel := v.dir, n.path
			a.overlayBack = focusFiles
			return a, func() tea.Msg {
				out, err := platform.GitDiffFile(dir, rel)
				return overlayMsg{"git diff – " + rel, withErr(out, err), ""}
			}
		}
	}
	return a, nil
}

// activateFile toggles a folder or opens the preview of a file.
func (a *Model) activateFile() tea.Cmd {
	v := &a.files
	n, ok := v.current()
	if !ok {
		return nil
	}
	if n.dir {
		if v.expanded[n.path] {
			delete(v.expanded, n.path)
		} else {
			v.expanded[n.path] = true
		}
		v.rebuild()
		return nil
	}
	p, rel := a.absPath(n.path), n.path
	a.overlayBack = focusFiles
	return func() tea.Msg {
		out, err := platform.ReadPreview(p)
		return overlayMsg{rel + dim.Render("  (aktualisiert sich live)"), withErr(out, err), p}
	}
}

func (a *Model) absPath(rel string) string {
	return filepath.Join(a.files.dir, filepath.FromSlash(rel))
}

// fitLines cuts every line to width w so wide content cannot break the layout.
func fitLines(s string, w int) string {
	if w <= 0 {
		return s
	}
	lines := strings.Split(strings.ReplaceAll(s, "\t", "    "), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(strings.TrimRight(l, "\r"), w, "…")
	}
	return strings.Join(lines, "\n")
}
