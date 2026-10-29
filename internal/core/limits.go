package core

import (
	"encoding/json"
	"time"
)

// Window is one usage window of the subscription (e.g. five hours).
type Window struct {
	Utilization float64   `json:"utilization"` // 0..1
	ResetsAt    time.Time `json:"resets_at"`
}

// Limits is the account-wide usage reported by Claude Code's
// rate_limit_event. It is shared by all sessions.
type Limits struct {
	FiveHour  Window    `json:"five_hour"`
	SevenDay  Window    `json:"seven_day"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type rateLimitInfo struct {
	Status         string `json:"status"`
	UnifiedWindows map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
	} `json:"unifiedWindows"`
}

// parseLimits reads a rate_limit_event; ok is false if it has no windows.
func parseLimits(raw json.RawMessage, now time.Time) (Limits, bool) {
	var info rateLimitInfo
	if json.Unmarshal(raw, &info) != nil || len(info.UnifiedWindows) == 0 {
		return Limits{}, false
	}
	win := func(key string) Window {
		w := info.UnifiedWindows[key]
		var reset time.Time
		if w.ResetsAt > 0 {
			reset = time.Unix(w.ResetsAt, 0)
		}
		return Window{Utilization: w.Utilization, ResetsAt: reset}
	}
	return Limits{
		FiveHour:  win("five_hour"),
		SevenDay:  win("seven_day"),
		Status:    info.Status,
		UpdatedAt: now,
	}, true
}

// contextFrom returns the context size of an assistant event: everything sent
// as input with that request, cached or not.
func contextFrom(ev streamEvent) int {
	var m struct {
		Usage *usage `json:"usage"`
	}
	if json.Unmarshal(ev.Message, &m) != nil || m.Usage == nil {
		return 0
	}
	return m.Usage.InputTokens + m.Usage.CacheCreation + m.Usage.CacheRead
}
