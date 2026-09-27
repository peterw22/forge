package main

import (
	"context"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

// Claude's stream-json result is final-answer-only, whereas text deltas can
// include commentary before an MCP call. That is not a frontend failure.
func TestClaudeCommentaryToolFinalAnswer(t *testing.T) {
	input := strings.Join([]string{
		claudeTestInit,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"I will inspect the bridge."}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"mcp__pi-go-agent__read"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"The answer is 42."}}}`,
		`{"type":"result","subtype":"success","session_id":"session-1","result":"The answer is 42."}`,
	}, "\n")
	events := make(chan agent.ProviderEvent, 8)
	if err := parseClaudeCLIStream(context.Background(), strings.NewReader(input), events, func(string) {}); err != nil {
		t.Fatal(err)
	}
	close(events)
	var text string
	var done int
	for event := range events {
		if event.Type == agent.ProviderTextDelta {
			text += event.Delta
		}
		if event.Type == agent.ProviderDone {
			done++
		}
	}
	if text != "I will inspect the bridge.\n\nThe answer is 42." || done != 1 {
		t.Fatalf("text=%q done=%d", text, done)
	}
}
