package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Config struct {
	ClaudeBin      string `json:"claude_bin"`
	PermissionMode string `json:"permission_mode"`
	// IDE is the command used to open a folder, e.g. "idea", "idea64", "code".
	IDE string `json:"ide"`
	// ExtraArgs are appended to every claude invocation, e.g. ["--model", "sonnet"].
	ExtraArgs []string `json:"extra_args,omitempty"`
	// ThinkingDisplay asks Claude to stream its thinking: "summarized"
	// (default, like Claude Code), "omitted" or "off" (don't ask).
	ThinkingDisplay string `json:"thinking_display,omitempty"`
	// SessionLink is opened with /link: the session name is appended, or
	// replaces {name}, e.g. "https://firma.atlassian.net/browse/".
	SessionLink string `json:"session_link,omitempty"`
}

type state struct {
	Config   Config     `json:"config"`
	Sessions []*Session `json:"sessions"`
	// Limits is the last known account usage, shown until fresh data arrives.
	Limits Limits `json:"limits"`
}

func defaultStatePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "llm-cockpit", "state.json"), nil
}

func loadState(p string) (*state, error) {
	st := &state{}
	data, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		applyDefaults(&st.Config)
		return st, err
	}
	if errors.Is(err, os.ErrNotExist) {
		applyDefaults(&st.Config)
		return st, nil
	}
	err = json.Unmarshal(data, st)
	applyDefaults(&st.Config)
	return st, err
}

// applyDefaults fills empty config fields so a partial state.json still works.
func applyDefaults(c *Config) {
	if c.ClaudeBin == "" {
		c.ClaudeBin = "claude"
	}
	if c.PermissionMode == "" {
		c.PermissionMode = "acceptEdits"
	}
	if c.IDE == "" {
		c.IDE = "idea"
	}
	if c.ThinkingDisplay == "" {
		c.ThinkingDisplay = "summarized"
	}
}

func saveState(p string, st *state) error {
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
