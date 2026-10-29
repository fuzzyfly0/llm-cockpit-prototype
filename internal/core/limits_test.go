package core

import (
	"reflect"
	"testing"
	"time"
)

// Recorded from a real session.
const rateLimitLine = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1793303400,"rateLimitType":"five_hour","unifiedWindows":{"five_hour":{"utilization":0.26,"resetsAt":1793303400},"seven_day":{"utilization":0.58,"resetsAt":1793563200}}}}`

func TestRateLimitEventUpdatesLimits(t *testing.T) {
	m := &Manager{st: &state{}}
	s := &Session{partial: -1}
	m.handle(s, parse(t, rateLimitLine))
	l := m.Limits()
	if l.FiveHour.Utilization != 0.26 || l.SevenDay.Utilization != 0.58 || l.Status != "allowed" {
		t.Fatalf("limits: %+v", l)
	}
	if !l.FiveHour.ResetsAt.Equal(time.Unix(1793303400, 0)) || !l.SevenDay.ResetsAt.Equal(time.Unix(1793563200, 0)) {
		t.Fatalf("reset times: %+v", l)
	}
}

func TestContextAndWindow(t *testing.T) {
	m := &Manager{st: &state{}}
	s := &Session{partial: -1, proc: &process{}}
	m.handle(s, parse(t, `{"type":"system","subtype":"init","model":"claude-haiku-5-5"}`))
	m.handle(s, parse(t, `{"type":"assistant","uuid":"a","message":{"content":[{"type":"text","text":"x"}],"usage":{"input_tokens":4,"cache_creation_input_tokens":11438,"cache_read_input_tokens":37817,"output_tokens":10}}}`))
	// Subagent requests must not change the main context size.
	m.handle(s, parse(t, `{"type":"assistant","uuid":"b","parent_tool_use_id":"t","message":{"content":[{"type":"text","text":"y"}],"usage":{"input_tokens":5}}}`))
	m.handle(s, parse(t, `{"type":"result","modelUsage":{"claude-haiku-5-5":{"contextWindow":1000000}}}`))
	if s.ContextTokens != 49259 || s.ContextWindow != 1000000 || s.ActiveModel != "claude-haiku-5-5" {
		t.Fatalf("context=%d window=%d model=%q", s.ContextTokens, s.ContextWindow, s.ActiveModel)
	}
}

func TestStartArgs(t *testing.T) {
	cfg := Config{PermissionMode: "acceptEdits", ExtraArgs: []string{"--model", "sonnet"}}
	s := &Session{ClaudeID: "abc", Model: "claude-haiku-5-5", Effort: "high"}
	got := startArgs(cfg, s)
	tail := got[len(got)-8:]
	want := []string{"--resume", "abc", "--model", "sonnet", "--model", "claude-haiku-5-5", "--effort", "high"}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("args tail %v, want %v", tail, want)
	}
}

func TestSetModelOnStoppedSession(t *testing.T) {
	m, _ := NewManagerAt("")
	m.st.Sessions = []*Session{{ID: "s1"}}
	if msg := m.SetModel("s1", "claude-haiku-5-5"); msg != "gilt ab dem nächsten Start" {
		t.Fatalf("message %q", msg)
	}
	if m.find("s1").Model != "claude-haiku-5-5" {
		t.Fatal("model not stored")
	}
}

func TestMessageKeys(t *testing.T) {
	msgs := []Message{
		{UUID: "u1", Kind: KindTool, Text: "Glob"},
		{UUID: "u1", Kind: KindTool, Text: "Grep"},
		{Kind: KindTurn, Text: "fertig"},
		{UUID: "u2", Kind: KindAssistant, Text: "x"},
	}
	keys := MessageKeys(msgs)
	if keys[0] != "u1" || keys[1] != "u1#1" || keys[3] != "u2" {
		t.Fatalf("keys %v", keys)
	}
	// Entries without UUID get a content hash that is stable.
	if keys[2] == "" || keys[2] != MessageKeys(msgs[2:3])[0] {
		t.Fatalf("hash key %q not stable", keys[2])
	}
}
