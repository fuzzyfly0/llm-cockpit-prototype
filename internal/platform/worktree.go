package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// repoRoot returns the main checkout of the repository containing dir and
// whether dir belongs to a linked worktree.
func repoRoot(dir string) (string, bool) {
	out, err := git(dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return "", false
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		return "", false
	}
	top := filepath.Clean(strings.TrimSpace(lines[0]))
	common := filepath.Clean(strings.TrimSpace(lines[1]))
	// The common dir is <main checkout>/.git for normal repositories.
	root := common
	if filepath.Base(common) == ".git" {
		root = filepath.Dir(common)
	}
	return root, !strings.EqualFold(root, top)
}

// RepoRoot returns the main checkout of the repository containing dir, or
// "" if dir is not inside a git repository.
func RepoRoot(dir string) string {
	root, _ := repoRoot(dir)
	return root
}

type Worktree struct {
	Path   string
	Branch string // empty for a detached HEAD
	Main   bool
}

// ListWorktrees lists all worktrees of the repository (main checkout first).
func ListWorktrees(repo string) ([]Worktree, error) {
	out, err := git(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var list []Worktree
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n") {
		var wt Worktree
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				wt.Path = filepath.Clean(strings.TrimPrefix(line, "worktree "))
			case strings.HasPrefix(line, "branch "):
				wt.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
			}
		}
		if wt.Path != "" {
			wt.Main = len(list) == 0
			list = append(list, wt)
		}
	}
	return list, nil
}

// FreeBranches returns up to n local branches (most recent first) that are
// not checked out in any worktree.
func FreeBranches(repo string, n int) []string {
	out, err := git(repo, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	used := map[string]bool{}
	if wts, err := ListWorktrees(repo); err == nil {
		for _, wt := range wts {
			used[wt.Branch] = true
		}
	}
	var free []string
	for _, b := range strings.Split(out, "\n") {
		if b = strings.TrimSpace(b); b != "" && !used[b] {
			free = append(free, b)
			if len(free) == n {
				break
			}
		}
	}
	return free
}

// DefaultBase is the start point for new branches: origin's default branch
// if known, else the current HEAD of the main checkout.
func DefaultBase(repo string) string {
	if ref, err := git(repo, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil && ref != "" && ref != "origin/HEAD" {
		return ref
	}
	return "HEAD"
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._/-]+`)

// BranchName turns a free-form name ("PROJ-123 Warenkorb fix") into a valid
// branch name ("PROJ-123-Warenkorb-fix").
func BranchName(name string) string {
	b := unsafeChars.ReplaceAllString(strings.TrimSpace(name), "-")
	b = regexp.MustCompile(`-+`).ReplaceAllString(b, "-")
	b = regexp.MustCompile(`/+`).ReplaceAllString(b, "/")
	b = strings.Trim(b, "-./")
	b = strings.ReplaceAll(b, "..", ".")
	return strings.TrimSuffix(b, ".lock")
}

// WorktreeDir is where the cockpit puts new worktrees: next to the main
// checkout in "<repo>-worktrees/<branch with / replaced>".
func WorktreeDir(repo, branch string) string {
	return filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-worktrees",
		strings.ReplaceAll(branch, "/", "-"))
}

// AddWorktree creates a worktree at path. With base != "" a new branch is
// created from base; otherwise the existing branch is checked out.
func AddWorktree(repo, path, branch, base string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("folder already exists: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	args := []string{"worktree", "add"}
	if base != "" {
		args = append(args, "-b", branch, path, base)
	} else {
		args = append(args, path, branch)
	}
	if out, err := git(repo, args...); err != nil {
		return errors.New(strings.TrimSpace(out))
	}
	return nil
}

// RemoveWorktree deletes a linked worktree (the branch is kept). force also
// removes uncommitted changes. Windows may still hold files of a just
// stopped process, so the removal is retried briefly.
func RemoveWorktree(repo, path string, force bool) error {
	args := []string{"worktree", "remove", path}
	if force {
		args = []string{"worktree", "remove", "--force", path}
	}
	var out string
	var err error
	for i := 0; i < 5; i++ {
		if out, err = git(repo, args...); err == nil {
			return nil
		}
		time.Sleep(400 * time.Millisecond)
	}
	return errors.New(strings.TrimSpace(out))
}
