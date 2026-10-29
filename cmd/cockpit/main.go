package main

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stefa/llm-cockpit/internal/core"
	"github.com/stefa/llm-cockpit/internal/ui"
)

func main() {
	mgr, err := core.NewManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Zustand konnte nicht geladen werden:", err)
	}
	if _, err := exec.LookPath(mgr.Config().ClaudeBin); err != nil {
		fmt.Fprintf(os.Stderr, "Claude CLI %q nicht gefunden: %v\n", mgr.Config().ClaudeBin, err)
		os.Exit(1)
	}

	p := tea.NewProgram(ui.New(mgr), tea.WithAltScreen(), tea.WithMouseCellMotion())
	mgr.Subscribe(func() { p.Send(ui.ChangedMsg{}) })
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mgr.StopAll()
}
