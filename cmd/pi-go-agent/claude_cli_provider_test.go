package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

const claudeTestInit = `{"type":"system","subtype":"init","session_id":"session-1","tools":["mcp__pi-go-agent__bash","mcp__pi-go-agent__read","mcp__pi-go-agent__replace","mcp__pi-go-agent__write"],"mcp_servers":[{"name":"pi-go-agent","status":"connected"}]}`

// stubClaudeOnPath puts a claude that reports no models first on PATH, so model
// checks use the pinned list on a machine without Claude Code.
func stubClaudeOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestClaudeRejectsOffAndMinimalEffort(t *testing.T) {
	stubClaudeOnPath(t)
	p := newClaudeCLIProvider("unused")
	for _, level := range []string{"off", "minimal", ""} {
		req := agent.Request{Model: "claude/claude-sonnet-5", Thinking: level, SessionID: "session", WorkingDirectory: t.TempDir(), ToolGuard: agyDenyGuard{}, OnToolEvent: func(agent.Event) {}, Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "hello"}}}}}
		events, errs := p.Stream(context.Background(), req)
		for range events {
		}
		err := <-errs
		if err == nil || !strings.Contains(err.Error(), "effort") {
			t.Fatalf("level=%q err=%v", level, err)
		}
	}
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !claudeEffortAllowed(level) {
			t.Fatalf("rejected %s", level)
		}
	}
}

func TestParseClaudeCLIStream(t *testing.T) {
	input := strings.Join([]string{
		claudeTestInit,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"Checking the arithmetic."}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hel"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}}`,
		`{"type":"result","subtype":"success","session_id":"session-1","result":"hello","usage":{"input_tokens":2,"output_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":5}}`,
	}, "\n")
	events := make(chan agent.ProviderEvent, 8)
	saved := ""
	if err := parseClaudeCLIStream(context.Background(), strings.NewReader(input), events, func(id string) { saved = id }); err != nil {
		t.Fatal(err)
	}
	close(events)
	text := ""
	thinking := ""
	done := 0
	for event := range events {
		if event.Type == agent.ProviderThinkingDelta {
			thinking += event.Delta
		}
		if event.Type == agent.ProviderTextDelta {
			text += event.Delta
		}
		if event.Type == agent.ProviderDone {
			done++
			if event.Usage.TotalTokens != 14 {
				t.Fatalf("usage=%#v", event.Usage)
			}
		}
	}
	if text != "hello" || thinking != "Checking the arithmetic." || saved != "session-1" || done != 1 {
		t.Fatalf("text=%q saved=%q done=%d", text, saved, done)
	}
}

func TestClaudeCLIStreamFailsClosed(t *testing.T) {
	cases := []string{
		`{"type":"system","subtype":"init","session_id":"s","tools":["Bash"],"mcp_servers":[{"name":"pi-go-agent","status":"connected"}]}`,
		strings.Replace(claudeTestInit, `"connected"`, `"failed"`, 1),
		claudeTestInit + "\n" + `{"type":"system","subtype":"permission_denied"}`,
		claudeTestInit + "\n" + `{"type":"result","subtype":"success","session_id":"session-1","result":"x","permission_denials":[{}]}`,
		claudeTestInit + "\n" + `{"type":"result","subtype":"success","session_id":"other","result":"x"}`,
		claudeTestInit + "\n" + `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"I will inspect."}}}` + "\n" + `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use"}}}` + "\n" + `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"final"}}}` + "\n" + `{"type":"result","subtype":"success","session_id":"session-1","result":"different"}`,
		claudeTestInit,
	}
	for _, input := range cases {
		events := make(chan agent.ProviderEvent, 8)
		if err := parseClaudeCLIStream(context.Background(), strings.NewReader(input), events, func(string) {}); err == nil {
			t.Errorf("accepted unsafe stream: %s", input)
		}
	}
}
