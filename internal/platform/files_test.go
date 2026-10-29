package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newRepo creates a git repo with a committed, a modified, a deleted, an
// untracked and an ignored file.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write("keep.txt", "a\n")
	write("src/main.go", "package main\n")
	write("gone.txt", "x\n")
	write(".gitignore", "build/\n")
	run("init", "-q")
	run("add", ".")
	run("commit", "-q", "-m", "init")
	write("src/main.go", "package main\n// changed\n")
	write("src/new.go", "package main\n")
	write("build/out.bin", "ignored")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestListFilesGit(t *testing.T) {
	files, err := ListFiles(newRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.Status
	}
	want := map[string]string{
		".gitignore": "", "keep.txt": "", "src/main.go": "M", "src/new.go": "?", "gone.txt": "D",
	}
	for p, st := range want {
		if s, ok := got[p]; !ok || s != st {
			t.Errorf("%s: got status %q (present=%v), want %q", p, s, ok, st)
		}
	}
	if _, ok := got["build/out.bin"]; ok {
		t.Error("ignored file listed")
	}
}

func TestListFilesPlainDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "x", "i.js"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), nil, 0o644)
	files, err := ListFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "a.txt" {
		t.Fatalf("got %+v", files)
	}
}

func TestReadPreview(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	_ = os.WriteFile(p, []byte("eins\n\tzwei"), 0o644)
	out, err := ReadPreview(p)
	if err != nil || out != "    1  eins\n    2      zwei\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	_ = os.WriteFile(p, []byte{0, 1, 2}, 0o644)
	if out, _ := ReadPreview(p); out != "(Binärdatei)" {
		t.Fatalf("binary: %q", out)
	}
}
