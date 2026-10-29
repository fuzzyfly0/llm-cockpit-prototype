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
}

type state struct {
	Config   Config     `json:"config"`
	Sessions []*Session `json:"sessions"`
}

func statePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "llm-cockpit", "state.json"), nil
}

func loadState() (*state, error) {
	st := &state{Config: Config{ClaudeBin: "claude", PermissionMode: "acceptEdits", IDE: "idea"}}
	p, err := statePath()
	if err != nil {
		return st, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, st)
}

func saveState(st *state) error {
	p, err := statePath()
	if err != nil {
		return err
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
