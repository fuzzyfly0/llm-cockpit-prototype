package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/stefa/llm-cockpit/internal/core"
	"github.com/stefa/llm-cockpit/internal/platform"
)

// settingText describes a free-text setting edited in the message field.
type settingText struct {
	label string
	get   func(c core.Config) string
	set   func(c *core.Config, v string)
}

var textSettings = map[string]settingText{
	"ide": {"IDE command (e.g. idea, idea64, code, cursor or a full path)",
		func(c core.Config) string { return c.IDE },
		func(c *core.Config, v string) { c.IDE = v }},
	"link": {"Session link: the session name is appended or replaces {name}",
		func(c core.Config) string { return c.SessionLink },
		func(c *core.Config, v string) { c.SessionLink = v }},
	"bin": {"Claude Code program (name in PATH or full path)",
		func(c core.Config) string { return c.ClaudeBin },
		func(c *core.Config, v string) { c.ClaudeBin = v }},
	"args": {"Extra arguments for every Claude start (space separated)",
		func(c core.Config) string { return strings.Join(c.ExtraArgs, " ") },
		func(c *core.Config, v string) { c.ExtraArgs = strings.Fields(v) }},
}

var permissionOptions = []pickOption{
	{"default – Claude asks; tools needing approval are denied here", "default"},
	{"acceptEdits – file edits are allowed (recommended)", "acceptEdits"},
	{"plan – Claude only plans, changes nothing", "plan"},
	{"bypassPermissions – everything allowed, no questions (risky)", "bypassPermissions"},
}

var thinkingOptions = []pickOption{
	{"summarized – show Claude's thinking (like Claude Code)", "summarized"},
	{"omitted – only that and how long Claude thinks", "omitted"},
	{"off – don't ask Claude for it", "off"},
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// openSettings shows the configuration as a list; choosing an entry edits it.
func (a *Model) openSettings() {
	c := a.mgr.Config()
	opts := []pickOption{
		{"IDE command: " + c.IDE, "ide"},
		{"Permission mode: " + c.PermissionMode, "perm"},
		{"Thinking text: " + c.ThinkingDisplay, "think"},
		{"Session link: " + orNone(c.SessionLink), "link"},
		{"Claude program: " + c.ClaudeBin, "bin"},
		{"Extra arguments: " + orNone(strings.Join(c.ExtraArgs, " ")), "args"},
		{"Open state.json in the default editor", "file"},
	}
	a.openPicker("Settings (apply to sessions started from now on)", opts, "", func(v string) (string, tea.Cmd) {
		switch v {
		case "perm":
			a.openPicker("Permission mode", permissionOptions, c.PermissionMode, func(v string) (string, tea.Cmd) {
				a.mgr.UpdateConfig(func(c *core.Config) { c.PermissionMode = v })
				return "permission mode: " + v + " – applies to sessions started from now on", nil
			})
		case "think":
			a.openPicker("Thinking text", thinkingOptions, c.ThinkingDisplay, func(v string) (string, tea.Cmd) {
				a.mgr.UpdateConfig(func(c *core.Config) { c.ThinkingDisplay = v })
				return "thinking text: " + v + " – applies to sessions started from now on", nil
			})
		case "file":
			if p := a.mgr.StatePath(); p != "" {
				a.report(platform.OpenFolder(p), "opened "+p)
			}
		default:
			if ts, ok := textSettings[v]; ok {
				a.settingKey = v
				a.focus = focusSetting
				a.input.Prompt = "Setting › "
				a.input.SetValue(ts.get(c))
				a.status = ts.label + " · enter save · esc cancel"
				return "", a.input.Focus()
			}
		}
		return "", nil
	})
}

// settingEntered saves the text typed for a.settingKey.
func (a *Model) settingEntered(value string) {
	ts := textSettings[a.settingKey]
	a.mgr.UpdateConfig(func(c *core.Config) { ts.set(c, strings.TrimSpace(value)) })
	a.status = "saved – applies to sessions started from now on"
	a.settingKey = ""
	a.resetInput()
	a.focus = focusInput
}
