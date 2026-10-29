package platform

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchReportsChanges(t *testing.T) {
	dir := t.TempDir()
	changed := make(chan struct{}, 10)
	w, err := Watch(dir, func() { changed <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	expect := func(what string) {
		t.Helper()
		select {
		case <-changed:
		case <-time.After(3 * time.Second):
			t.Fatalf("no change reported for %s", what)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect("new file")

	// A folder created after the watcher started is watched as well.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	expect("new folder")
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	expect("file in new folder")
}
