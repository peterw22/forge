package main

import (
	"context"
	"strings"
	"testing"

	"github.com/peterw22/pi-go/internal/agent"
)

func TestClaudeResponseBoundaries(t *testing.T) {
	const first = `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Inspecting."}}}`
	const tool = `{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use"}}}`
	const start = `{"type":"stream_event","event":{"type":"message_start"}}`
	const chunk = `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"An"}}}`
	const result = `{"type":"result","subtype":"success","session_id":"session-1","result":"Answer."}`
	for _, tc := range []struct {
		name    string
		records []string
		want    string
	}{
		{"result-only after tool", []string{first, tool, result}, "Inspecting.\n\nAnswer."},
		{"message boundary and partial result", []string{first, start, chunk, result}, "Inspecting.\n\nAnswer."},
		{"multiple tools do not multiply separators", []string{first, tool, tool, start, chunk, result}, "Inspecting.\n\nAnswer."},
		{"initial message has no leading separator", []string{start, chunk, result}, "Answer."},
		{"existing paragraph break", []string{strings.Replace(first, "Inspecting.", `Inspecting.\n\n`, 1), tool, chunk, result}, "Inspecting.\n\nAnswer."},
		{"multiple commentary messages", []string{first, tool, start, first, tool, start, chunk, result}, "Inspecting.\n\nInspecting.\n\nAnswer."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.Join(append([]string{claudeTestInit}, tc.records...), "\n")
			events := make(chan agent.ProviderEvent, 32)
			if err := parseClaudeCLIStream(context.Background(), strings.NewReader(input), events, func(string) {}); err != nil {
				t.Fatal(err)
			}
			close(events)
			var got strings.Builder
			for event := range events {
				if event.Type == agent.ProviderTextDelta {
					got.WriteString(event.Delta)
				}
			}
			if got.String() != tc.want {
				t.Fatalf("got %q, want %q", got.String(), tc.want)
			}
		})
	}
}
