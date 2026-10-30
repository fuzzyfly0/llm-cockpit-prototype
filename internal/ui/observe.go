package ui

import (
	"fmt"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stefa/llm-cockpit/internal/core"
)

// observeLabel is the marker of sessions that run in a Claude terminal.
const observeLabel = "⧉"

// openObserve offers Claude Code sessions started outside the cockpit
// (changed within the last day) to follow them.
func (a *Model) openObserve() {
	found := a.mgr.ExternalSessions(24 * time.Hour)
	if len(found) == 0 {
		a.status = "no Claude sessions outside the cockpit in the last 24 hours"
		return
	}
	var opts []pickOption
	for i, e := range found {
		label := fmt.Sprintf("%s  %s  %s", e.Modified.Format("15:04"), filepath.Base(e.Dir), dim.Render(e.LastPrompt))
		opts = append(opts, pickOption{label, fmt.Sprint(i)})
	}
	a.openPicker("Follow a Claude terminal session (last 24 hours)", opts, "0", func(v string) (string, tea.Cmd) {
		var i int
		fmt.Sscan(v, &i)
		id := a.mgr.AddObserved(found[i])
		a.refresh()
		for idx, s := range a.sessions {
			if s.ID == id {
				a.sel = idx
			}
		}
		a.refresh()
		return "following " + found[i].Dir + " (" + observeLabel + " write in the Claude window · /takeover continues it here)", a.gitAllCmd()
	})
}

// takeOver brings an observed session back into the cockpit.
func (a *Model) takeOver() {
	s, ok := a.selected()
	if !ok || !s.Observe {
		a.status = "the selected session is not running in a Claude terminal"
		return
	}
	a.mgr.TakeOver(s.ID)
	a.status = "taken over – close the Claude terminal; the next message continues the session here"
	a.refresh()
}

// observedHint explains how to write to an observed session.
func observedHint(s core.Session) string {
	if !s.Observe {
		return ""
	}
	return observeLabel + " runs in a Claude terminal – write there · /takeover continues it here"
}
