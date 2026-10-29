package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchName(t *testing.T) {
	tests := map[string]string{
		"PROJ-123 Warenkorb fix": "PROJ-123-Warenkorb-fix",
		"feature/export":         "feature/export",
		"  login fix!! ":         "login-fix",
		"a..b.lock":              "a.b",
		"Größe ändern":           "Gr-e-ndern",
	}
	for in, want := range tests {
		if got := BranchName(in); got != want {
			t.Errorf("BranchName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	repo := newRepo(t)
	run := func(args ...string) {
		t.Helper()
		if out, err := git(repo, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "wip")
	run("branch", "frei")

	if base := DefaultBase(repo); base != "HEAD" {
		t.Fatalf("base without origin = %q", base)
	}
	path := WorktreeDir(repo, "feature/x")
	if filepath.Base(path) != "feature-x" || !strings.HasSuffix(filepath.Dir(path), "-worktrees") {
		t.Fatalf("worktree dir %q", path)
	}
	if err := AddWorktree(repo, path, "feature/x", "HEAD"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })

	info := GitStatus(path)
	if !info.IsWorktree || info.Branch != "feature/x" || !sameDir(info.Root, repo) {
		t.Fatalf("worktree info %+v (repo %s)", info, repo)
	}
	if main := GitStatus(repo); main.IsWorktree || !sameDir(main.Root, repo) {
		t.Fatalf("main info %+v", main)
	}
	wts, err := ListWorktrees(repo)
	if err != nil || len(wts) != 2 || !wts[0].Main || wts[1].Branch != "feature/x" {
		t.Fatalf("worktrees %+v %v", wts, err)
	}
	if free := FreeBranches(repo, 10); len(free) != 1 || free[0] != "frei" {
		t.Fatalf("free branches %v", free)
	}
	// An existing branch can get its own worktree as well.
	path2 := WorktreeDir(repo, "frei")
	if err := AddWorktree(repo, path2, "frei", ""); err != nil {
		t.Fatal(err)
	}

	// Uncommitted changes block a normal removal, --force removes anyway.
	if err := os.WriteFile(filepath.Join(path, "neu.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(repo, path, false); err == nil {
		t.Fatal("removal of a dirty worktree should fail without force")
	}
	if err := RemoveWorktree(repo, path, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree folder still exists: %v", err)
	}
	if err := RemoveWorktree(repo, path2, false); err != nil {
		t.Fatal(err)
	}
}

func sameDir(a, b string) bool {
	ea, _ := filepath.EvalSymlinks(a)
	eb, _ := filepath.EvalSymlinks(b)
	return strings.EqualFold(filepath.Clean(ea), filepath.Clean(eb))
}
