package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stefa/llm-cockpit/internal/core"
	"github.com/stefa/llm-cockpit/internal/ui"
)

func main() {
	mgr, err := core.NewManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not load state:", err)
	}
	if _, err := exec.LookPath(mgr.Config().ClaudeBin); err != nil {
		fmt.Fprintf(os.Stderr, "Claude CLI %q not found: %v\n", mgr.Config().ClaudeBin, err)
		os.Exit(1)
	}

	// COCKPIT_DEBUG=1 logs every key and mouse event (to diagnose input).
	if os.Getenv("COCKPIT_DEBUG") != "" {
		path := filepath.Join(os.TempDir(), "llm-cockpit-debug.log")
		f, err := tea.LogToFile(path, "")
		if err == nil {
			defer f.Close()
			ui.SetDebugLog(log.New(f, "", log.Ltime|log.Lmicroseconds))
			fmt.Fprintln(os.Stderr, "debug log:", path)
		}
	}

	p := tea.NewProgram(ui.New(mgr), tea.WithAltScreen(), tea.WithMouseCellMotion())
	mgr.Subscribe(func() { p.Send(ui.ChangedMsg{}) })
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mgr.StopAll()
}
