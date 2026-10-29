package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stefa/llm-cockpit/internal/core"
	"github.com/stefa/llm-cockpit/internal/platform"
)

// worktreeMsg reports a finished "git worktree add".
type worktreeMsg struct {
	name, path, branch, base string
	err                      error
}

// gitAllCmd fetches git info for every session folder (for grouping).
func (a *Model) gitAllCmd() tea.Cmd {
	seen := map[string]bool{}
	var cmds []tea.Cmd
	for _, s := range a.sessions {
		if dir := s.Dir; !seen[dir] {
			seen[dir] = true
			cmds = append(cmds, func() tea.Msg { return gitMsg{dir, platform.GitStatus(dir)} })
		}
	}
	return tea.Batch(cmds...)
}

func (a *Model) resetInput() {
	a.input.SetValue("")
	a.input.Prompt = "› "
}

// newSession starts the "new session" flow: known repositories/folders are
// offered first so the path only has to be typed for a new place.
func (a *Model) newSession() tea.Cmd {
	opts := a.knownPlaces()
	if len(opts) == 0 {
		return a.askDir()
	}
	opts = append(opts, pickOption{"Other folder …", ""})
	a.openPicker("New session – where?", opts, opts[0].value, func(v string) (string, tea.Cmd) {
		if v == "" {
			return "", a.askDir()
		}
		if repo := platform.RepoRoot(v); repo != "" {
			a.chooseWorkspace(v, repo)
			return "", nil
		}
		a.askName(v)
		return "", nil
	})
	return nil
}

// knownPlaces lists the repositories (main checkouts) and plain folders of
// all sessions, the selected session's place first.
func (a *Model) knownPlaces() []pickOption {
	place := func(s core.Session) string {
		if g := a.git[s.Dir]; g.IsRepo && g.Root != "" {
			return g.Root
		}
		return s.Dir
	}
	var opts []pickOption
	seen := map[string]bool{}
	add := func(p string) {
		if k := strings.ToLower(filepath.Clean(p)); !seen[k] {
			seen[k] = true
			label := filepath.Base(p) + "  (" + p + ")"
			if g := a.git[p]; g.IsRepo || platform.RepoRoot(p) != "" {
				label = "Repo " + label
			}
			opts = append(opts, pickOption{label, p})
		}
	}
	if s, ok := a.selected(); ok {
		add(place(s))
	}
	for _, s := range a.sessions {
		add(place(s))
	}
	return opts
}

// askDir asks for a folder path (prefilled with the working directory).
func (a *Model) askDir() tea.Cmd {
	wd, _ := os.Getwd()
	a.focus = focusNewDir
	a.input.Prompt = "New session › folder: "
	a.input.SetValue(wd)
	a.input.CursorEnd()
	return a.input.Focus()
}

// askName asks for the session name of a session working directly in dir.
func (a *Model) askName(dir string) {
	a.newDir = dir
	a.focus = focusNewName
	a.input.Prompt = "New session › name: "
	a.input.SetValue(filepath.Base(dir))
	a.input.CursorEnd()
	a.input.Focus()
}

// createSession starts a session, selects it and makes it ready for typing.
func (a *Model) createSession(name, dir string) tea.Cmd {
	if _, err := a.mgr.Create(name, dir); err != nil {
		a.status = "Start failed: " + err.Error()
	}
	a.sel = len(a.mgr.List()) - 1
	a.resetInput()
	a.focus = focusInput
	a.refresh()
	return tea.Batch(a.input.Focus(), a.gitAllCmd())
}

// chooseWorkspace asks how a new session in a git repository should work:
// directly in the folder, in a new worktree or in an existing one.
func (a *Model) chooseWorkspace(dir, repo string) {
	a.resetInput()
	a.newDir, a.newRepo = dir, repo
	branch := platform.GitStatus(dir).Branch
	opts := []pickOption{
		{fmt.Sprintf("Work directly in the folder (⎇ %s)", branch), "direct"},
		{"New worktree with a new branch …", "new"},
	}
	if wts, err := platform.ListWorktrees(repo); err == nil {
		for _, wt := range wts {
			if wt.Main || strings.EqualFold(filepath.Clean(wt.Path), filepath.Clean(dir)) {
				continue
			}
			opts = append(opts, pickOption{fmt.Sprintf("Worktree: %s (⎇ %s)", filepath.Base(wt.Path), wt.Branch), "wt:" + wt.Path})
		}
	}
	for _, b := range platform.FreeBranches(repo, 15) {
		opts = append(opts, pickOption{"Branch: " + b + " → own worktree", "br:" + b})
	}
	a.openPicker("New session in "+filepath.Base(repo), opts, "direct", func(v string) (string, tea.Cmd) {
		switch {
		case v == "direct":
			a.askName(dir)
		case v == "new":
			a.focus = focusNewWorktree
			a.input.Prompt = "New worktree › name (becomes the branch): "
			a.input.Focus()
		case strings.HasPrefix(v, "wt:"):
			path := strings.TrimPrefix(v, "wt:")
			return "", a.createSession(filepath.Base(path), path)
		case strings.HasPrefix(v, "br:"):
			b := strings.TrimPrefix(v, "br:")
			return "creating worktree for " + b + "…", addWorktreeCmd(repo, b, b, "")
		}
		return "", nil
	})
}

// addWorktreeCmd runs "git worktree add" in the background (it can take a
// while in big repositories). base == "" checks out an existing branch.
func addWorktreeCmd(repo, name, branch, base string) tea.Cmd {
	path := platform.WorktreeDir(repo, branch)
	return func() tea.Msg {
		err := platform.AddWorktree(repo, path, branch, base)
		return worktreeMsg{name: name, path: path, branch: branch, base: base, err: err}
	}
}

func (a *Model) onWorktreeAdded(msg worktreeMsg) tea.Cmd {
	if msg.err != nil {
		a.status = "could not create the worktree: " + msg.err.Error()
		return nil
	}
	cmd := a.createSession(msg.name, msg.path)
	from := ""
	if msg.base != "" {
		from = " from " + msg.base
	}
	a.status = fmt.Sprintf("worktree created: %s (branch %s%s)", msg.path, msg.branch, from)
	return cmd
}

// newWorktreeEntered handles the name typed for a new worktree.
func (a *Model) newWorktreeEntered(name string) tea.Cmd {
	branch := platform.BranchName(name)
	if branch == "" {
		a.status = "please enter a name, e.g. login-fix or PROJ-123"
		return nil
	}
	a.resetInput()
	a.focus = focusInput
	base := platform.DefaultBase(a.newRepo)
	a.status = fmt.Sprintf("creating worktree %s (branch %s from %s)…", branch, branch, base)
	return addWorktreeCmd(a.newRepo, branch, branch, base)
}

// confirmRemove asks before removing a session; sessions in a linked
// worktree can take the worktree with them.
func (a *Model) confirmRemove(s core.Session) {
	g := a.git[s.Dir]
	if !g.IsWorktree {
		a.focus = focusConfirm
		a.status = fmt.Sprintf("Remove session %q from the cockpit? (y/n)", s.Name)
		a.confirm = func() { a.mgr.Remove(s.ID); a.refresh() }
		return
	}
	opts := []pickOption{
		{"Remove from the cockpit only (keep the worktree)", "keep"},
		{fmt.Sprintf("Remove the session and delete the worktree (branch %s is kept)", g.Branch), "delete"},
		{"Cancel", "cancel"},
	}
	a.openPicker("Remove session – "+s.Name, opts, "keep", func(v string) (string, tea.Cmd) {
		switch v {
		case "keep":
			a.mgr.Remove(s.ID)
			return "session removed, worktree kept: " + s.Dir, nil
		case "delete":
			if g.Dirty > 0 {
				a.openPicker(fmt.Sprintf("Worktree %s has %d uncommitted changes", filepath.Base(s.Dir), g.Dirty),
					[]pickOption{{"Cancel", "cancel"}, {"Delete anyway – the changes will be lost", "force"}}, "cancel",
					func(v string) (string, tea.Cmd) {
						if v != "force" {
							return "cancelled", nil
						}
						return a.removeWithWorktree(s, g.Root, true)
					})
				return "", nil
			}
			return a.removeWithWorktree(s, g.Root, false)
		}
		return "cancelled", nil
	})
}

func (a *Model) removeWithWorktree(s core.Session, repo string, force bool) (string, tea.Cmd) {
	a.mgr.Remove(s.ID) // stops the Claude process first
	dir := s.Dir
	return "deleting worktree " + dir + "…", func() tea.Msg {
		if err := platform.RemoveWorktree(repo, dir, force); err != nil {
			return statusMsg("worktree not deleted: " + err.Error())
		}
		return statusMsg("session and worktree deleted: " + dir)
	}
}

// group is one repository (or "no repository") in the session list.
type group struct {
	label    string
	sessions []int // indexes into a.sessions
}

// groups orders the sessions by repository: worktrees of the same repo
// share a group. Groups keep the order in which they first appear.
func (a *Model) groups() []group {
	var out []group
	index := map[string]int{}
	for i, s := range a.sessions {
		key, label := "dir:"+s.Dir, filepath.Base(s.Dir)
		if g, ok := a.git[s.Dir]; ok {
			if g.IsRepo && g.Root != "" {
				key, label = "repo:"+strings.ToLower(g.Root), filepath.Base(g.Root)
			} else if !g.IsRepo {
				key, label = "none", "No git repository"
			}
		}
		gi, ok := index[key]
		if !ok {
			gi = len(out)
			index[key] = gi
			out = append(out, group{label: label})
		}
		out[gi].sessions = append(out[gi].sessions, i)
	}
	return out
}

// displayOrder lists session indexes in the order shown in the list.
func (a *Model) displayOrder() []int {
	var order []int
	for _, g := range a.groups() {
		order = append(order, g.sessions...)
	}
	return order
}

// moveSelection selects the previous/next session in display order.
func (a *Model) moveSelection(d int) bool {
	order := a.displayOrder()
	for pos, i := range order {
		if i == a.sel {
			if n := pos + d; n >= 0 && n < len(order) {
				a.sel = order[n]
				return true
			}
			return false
		}
	}
	return false
}
