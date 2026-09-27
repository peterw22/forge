package main

import (
	"context"
	"strings"
	"testing"

	"github.com/peterw22/pi-go/internal/agent"
)

// Claude echoes an MCP read of an image as one stream-json line holding the
// base64 data; a screenshot easily exceeds the old 1 MiB scanner limit.
func TestClaudeStreamAcceptsLongImageToolResultLine(t *testing.T) {
	image := strings.Repeat("A", 3<<20)
	input := strings.Join([]string{
		claudeTestInit,
		`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"mcp__pi-go-agent__read"}}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + image + `"}}]}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"The screenshot shows the chat."}}}`,
		`{"type":"result","subtype":"success","session_id":"session-1","result":"The screenshot shows the chat."}`,
	}, "\n")
	events := make(chan agent.ProviderEvent, 8)
	if err := parseClaudeCLIStream(context.Background(), strings.NewReader(input), events, func(string) {}); err != nil {
		t.Fatal(err)
	}
	close(events)
	var text string
	for event := range events {
		if event.Type == agent.ProviderTextDelta {
			text += event.Delta
		}
	}
	if text != "The screenshot shows the chat." {
		t.Fatalf("text = %q", text)
	}
}
