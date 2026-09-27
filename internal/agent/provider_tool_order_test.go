package agent

import (
	"context"
	"testing"
)

// bridgedToolProvider mimics a CLI provider whose MCP bridge runs a tool
// between two text blocks of one streamed turn.
type bridgedToolProvider struct{}

func (bridgedToolProvider) Stream(_ context.Context, request Request) (<-chan ProviderEvent, <-chan error) {
	events := make(chan ProviderEvent, 8)
	errs := make(chan error)
	go func() {
		defer close(events)
		defer close(errs)
		events <- ProviderEvent{Type: ProviderThinkingDelta, Delta: "plan"}
		events <- ProviderEvent{Type: ProviderTextDelta, Delta: "I will check."}
		request.OnToolEvent(Event{Type: EventToolExecutionStart, ToolCallID: "call-1", ToolName: "read"})
		request.OnToolEvent(Event{Type: EventToolExecutionEnd, ToolCallID: "call-1", ToolName: "read", Result: &ToolResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}}})
		events <- ProviderEvent{Type: ProviderTextDelta, Delta: "\n\nDone."}
		events <- ProviderEvent{Type: ProviderDone, StopReason: "stop"}
	}()
	return events, errs
}

func TestProviderToolSplitsAssistantText(t *testing.T) {
	core, err := New(Config{Model: "test", Provider: bridgedToolProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	core.ConfigureProviderSession(nil, nil, func(event Event) {
		if event.Type == EventToolExecutionStart {
			order = append(order, "tool")
		}
	})
	if err := core.Run(context.Background(), "go", func(event Event) {
		if event.Type == EventMessageEnd && event.Message.Role == RoleAssistant {
			order = append(order, contentText(event.Message.Content))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 || order[0] != "I will check." || order[1] != "tool" || order[2] != "\n\nDone." {
		t.Fatalf("order = %q", order)
	}
	messages := core.Snapshot().Messages
	if len(messages) != 3 || messages[1].Content[0].Type != "thinking" || contentText(messages[2].Content) != "\n\nDone." {
		t.Fatalf("messages = %#v", messages)
	}
}
