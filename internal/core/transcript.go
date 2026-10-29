package core

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
)

// loadTranscript reads the history of a Claude Code session from
// ~/.claude/projects/*/<claudeID>.jsonl. It also returns the context size of
// the last main-agent request.
func loadTranscript(claudeID string) ([]Message, int) {
	home, err := os.UserHomeDir()
	if err != nil || claudeID == "" {
		return nil, 0
	}
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", claudeID+".jsonl"))
	if len(files) == 0 {
		return nil, 0
	}
	f, err := os.Open(files[0])
	if err != nil {
		return nil, 0
	}
	defer f.Close()

	var out []Message
	context := 0
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var ev streamEvent
			if json.Unmarshal(line, &ev) == nil && (ev.Type == "user" || ev.Type == "assistant") {
				out = append(out, messagesFrom(ev)...)
				if ev.Type == "assistant" && !ev.isSubagent() {
					if c := contextFrom(ev); c > 0 {
						context = c
					}
				}
			}
		}
		if err != nil {
			return out, context
		}
	}
}
