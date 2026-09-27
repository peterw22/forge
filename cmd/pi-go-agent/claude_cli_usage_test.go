package main

import (
	"context"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

// Usage recorded from a Claude Code 2.1.282 Haiku turn with one tool call.
// The result sums both API calls; the context is only the second call.
func TestClaudeUsageContextIsLastCall(t *testing.T) {
	input := strings.Join([]string{
		claudeTestInit,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":7858,"cache_read_input_tokens":0,"output_tokens":3}}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","usage":{"input_tokens":10,"cache_creation_input_tokens":7858,"cache_read_input_tokens":0,"output_tokens":104}}}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":8,"cache_creation_input_tokens":5411,"cache_read_input_tokens":4974,"output_tokens":3}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","usage":{"input_tokens":8,"cache_creation_input_tokens":5411,"cache_read_input_tokens":4974,"output_tokens":48}}}`,
		`{"type":"result","subtype":"success","session_id":"session-1","result":"ok","usage":{"input_tokens":18,"cache_creation_input_tokens":13269,"cache_read_input_tokens":4974,"output_tokens":152},"modelUsage":{"claude-haiku-4-5-20251001":{"contextWindow":200000}}}`,
	}, "\n")
	events := make(chan agent.ProviderEvent, 8)
	if err := parseClaudeCLIStream(context.Background(), strings.NewReader(input), events, func(string) {}); err != nil {
		t.Fatal(err)
	}
	close(events)
	var usage agent.Usage
	for event := range events {
		if event.Type == agent.ProviderDone {
			usage = event.Usage
		}
	}
	want := agent.Usage{Input: 18261, Output: 152, CacheRead: 4974, CacheWrite: 13269, TotalTokens: 18413, ContextTokens: 10441, ContextWindow: 200000}
	if usage != want {
		t.Fatalf("usage = %#v", usage)
	}
}
