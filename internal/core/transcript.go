package core

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// loadTranscript reads the history of a Claude Code session from
// ~/.claude/projects/*/<claudeID>.jsonl.
func loadTranscript(claudeID string) []Message {
	home, err := os.UserHomeDir()
	if err != nil || claudeID == "" {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", claudeID+".jsonl"))
	if len(files) == 0 {
		return nil
	}
	f, err := os.Open(files[0])
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []Message
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var ev streamEvent
			if json.Unmarshal(line, &ev) == nil && (ev.Type == "user" || ev.Type == "assistant") {
				out = append(out, messagesFrom(ev)...)
			}
		}
		if err != nil {
			return out
		}
	}
}
