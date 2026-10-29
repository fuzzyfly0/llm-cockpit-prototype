package platform

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher reports changes below a directory (debounced). .git and the usual
// dependency folders are not watched; git-only changes such as commits are
// picked up by the periodic refresh instead.
type Watcher struct {
	w     *fsnotify.Watcher
	once  sync.Once
	timer *time.Timer
}

const watchDebounce = 250 * time.Millisecond

// Watch calls onChange (on its own goroutine) after files below dir changed.
func Watch(dir string, onChange func()) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{w: fw}
	w.timer = time.AfterFunc(time.Hour, onChange)
	w.timer.Stop()
	addTree(fw, dir)
	go w.loop()
	return w, nil
}

func addTree(fw *fsnotify.Watcher, root string) {
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != root && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if n++; n > 5000 {
			return fs.SkipAll
		}
		_ = fw.Add(p)
		return nil
	})
}

func (w *Watcher) loop() {
	for {
		select {
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			if ev.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() && !skipDirs[filepath.Base(ev.Name)] {
					addTree(w.w, ev.Name)
				}
			}
			w.timer.Reset(watchDebounce)
		case _, ok := <-w.w.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *Watcher) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		w.timer.Stop()
		_ = w.w.Close()
	})
}
