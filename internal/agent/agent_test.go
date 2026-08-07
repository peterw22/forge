package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type scriptedProvider struct {
	mu       sync.Mutex
	requests []Request
	scripts  [][]ProviderEvent
}

func (p *scriptedProvider) Stream(_ context.Context, request Request) (<-chan ProviderEvent, <-chan error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	script := p.scripts[len(p.requests)-1]
	p.mu.Unlock()
	events := make(chan ProviderEvent, len(script))
	for _, event := range script {
		events <- event
	}
	close(events)
	errs := make(chan error)
	close(errs)
	return events, errs
}

type denyingGuard struct{}

func (denyingGuard) Check(context.Context, GuardRequest) GuardDecision {
	return GuardDecision{Reason: "destructive command", Description: "Command:\nrm file"}
}

func TestToolGuardWaitsForExplicitApproval(t *testing.T) {
	provider := &scriptedProvider{scripts: [][]ProviderEvent{
		{{Type: ProviderToolCall, ToolCall: ContentBlock{Type: "toolCall", ID: "danger", Name: "bash"}}, {Type: ProviderDone, StopReason: "toolUse"}},
		{{Type: ProviderTextDelta, Delta: "done"}, {Type: ProviderDone, StopReason: "stop"}},
	}}
	executed := false
	core, err := New(Config{Model: "test", Provider: provider, ToolGuard: denyingGuard{}, AllowApproval: true, Tools: []Tool{{Name: "bash", Execute: func(context.Context, map[string]any, func(ToolResult)) (ToolResult, error) {
		executed = true
		return ToolResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}}, nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan Event, 20)
	done := make(chan error, 1)
	go func() { done <- core.Run(context.Background(), "do it", func(event Event) { events <- event }) }()
	var approval Event
	var safetyStatuses []string
	for approval.Type != EventApprovalRequired {
		approval = <-events
		if approval.Type == EventToolSafetyUpdate {
			safetyStatuses = append(safetyStatuses, approval.SafetyStatus)
		}
	}
	if approval.ApprovalID == "" || approval.Reason != "destructive command" {
		t.Fatalf("approval = %#v", approval)
	}
	if len(safetyStatuses) != 2 || safetyStatuses[0] != "classifying" || safetyStatuses[1] != "rejected" {
		t.Fatalf("safety statuses = %#v", safetyStatuses)
	}
	if !core.ResolveApproval(approval.ApprovalID, true) {
		t.Fatal("approval was not pending")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !executed {
		t.Fatal("approved tool did not execute")
	}
	approvedStatus := false
	for len(events) > 0 {
		event := <-events
		if event.Type == EventToolSafetyUpdate && event.SafetyStatus == "approved" {
			approvedStatus = true
		}
	}
	if !approvedStatus {
		t.Fatal("manual approval did not emit green safety status")
	}
}

func TestCompactUsesStructuredCheckpointAndRetainsRecentTurn(t *testing.T) {
	provider := &scriptedProvider{scripts: [][]ProviderEvent{{
		{Type: ProviderTextDelta, Delta: "## Goal\nKeep working"},
		{Type: ProviderDone, StopReason: "stop", Usage: Usage{Input: 100, Output: 20, TotalTokens: 120}},
	}}}
	core, err := New(Config{Model: "test", Thinking: "high", Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("x", 50000)
	messages := []Message{
		{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: large}}},
		{Role: RoleAssistant, Content: []ContentBlock{{Type: "text", Text: large}}},
		{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: large}}},
		{Role: RoleAssistant, Content: []ContentBlock{{Type: "text", Text: large}}},
		{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: large}}},
		{Role: RoleAssistant, Content: []ContentBlock{{Type: "text", Text: large}}},
	}
	core.Restore(messages, "", "", Usage{})
	persisted := false
	result, err := core.Compact(context.Background(), "focus on tests", func(value CompactionResult) error { persisted = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !persisted || result.Summary != "## Goal\nKeep working" || len(result.RetainedTail) != 2 {
		t.Fatalf("result = %#v", result)
	}
	state := core.Snapshot()
	if state.Streaming || len(state.Messages) != 3 || state.Messages[0].Role != RoleCompactionSummary {
		t.Fatalf("state = %#v", state)
	}
	if state.Usage.TotalTokens != 120 || state.ContextTokens >= result.TokensBefore {
		t.Fatalf("usage/context = %#v", state)
	}
	requestText := provider.requests[0].Messages[0].Content[0].Text
	if provider.requests[0].SystemPrompt != summarizationSystemPrompt || !strings.Contains(requestText, "Additional focus: focus on tests") || !strings.Contains(requestText, "## Critical Context") {
		t.Fatalf("summary request = %q", requestText)
	}
}

func TestCompactRejectsShortContext(t *testing.T) {
	provider := &scriptedProvider{}
	core, err := New(Config{Model: "test", Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	core.Restore([]Message{{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "short"}}}}, "", "", Usage{})
	if _, err := core.Compact(context.Background(), "", nil); err == nil || !strings.Contains(err.Error(), "not enough context") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunExecutesToolAndContinuesWithResult(t *testing.T) {
	provider := &scriptedProvider{scripts: [][]ProviderEvent{
		{
			{Type: ProviderTransport, Transport: "WS"},
			{Type: ProviderTextDelta, Delta: "I will echo."},
			{Type: ProviderToolCall, ToolCall: ContentBlock{Type: "toolCall", ID: "call_1", Name: "echo", Arguments: map[string]any{"text": "hello"}}},
			{Type: ProviderDone, StopReason: "toolUse", Usage: Usage{Input: 2, Output: 3, TotalTokens: 5}},
		},
		{
			{Type: ProviderTextDelta, Delta: "Done."},
			{Type: ProviderDone, StopReason: "stop", Usage: Usage{Input: 4, Output: 1, TotalTokens: 5}},
		},
	}}
	var updates int
	core, err := New(Config{
		Model:    "test",
		Provider: provider,
		Tools: []Tool{{
			Name: "echo",
			Execute: func(_ context.Context, args map[string]any, update func(ToolResult)) (ToolResult, error) {
				result := ToolResult{Content: []ContentBlock{{Type: "text", Text: args["text"].(string)}}}
				update(result)
				return result, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []Event
	if err := core.Run(context.Background(), "Please echo hello", func(event Event) {
		events = append(events, event)
		if event.Type == EventToolExecutionUpdate {
			updates++
		}
	}); err != nil {
		t.Fatal(err)
	}

	if len(provider.requests) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(provider.requests))
	}
	second := provider.requests[1].Messages
	if len(second) != 3 || second[2].Role != RoleToolResult || second[2].Content[0].Text != "hello" {
		t.Fatalf("second request messages = %#v", second)
	}
	if updates != 1 {
		t.Fatalf("tool updates = %d, want 1", updates)
	}
	state := core.Snapshot()
	if state.Streaming || len(state.Messages) != 4 {
		t.Fatalf("state = %#v", state)
	}
	if state.Usage.TotalTokens != 10 {
		t.Fatalf("total tokens = %d, want 10", state.Usage.TotalTokens)
	}
	if state.UpstreamTransport != "WS" {
		t.Fatalf("upstream transport = %q, want WS", state.UpstreamTransport)
	}
	foundTransport := false
	for _, event := range events {
		if event.Type == EventUpstreamTransport && event.UpstreamTransport == "WS" {
			foundTransport = true
		}
	}
	if !foundTransport {
		t.Fatal("upstream transport event was not emitted")
	}
	if events[0].Type != EventAgentStart || events[len(events)-1].Type != EventAgentEnd {
		t.Fatalf("agent event boundaries = %s, %s", events[0].Type, events[len(events)-1].Type)
	}
}

func TestRunTurnsToolPanicIntoErrorResult(t *testing.T) {
	provider := &scriptedProvider{scripts: [][]ProviderEvent{
		{{Type: ProviderToolCall, ToolCall: ContentBlock{Type: "toolCall", ID: "call_panic", Name: "bad"}}, {Type: ProviderDone, StopReason: "toolUse"}},
		{{Type: ProviderTextDelta, Delta: "recovered"}, {Type: ProviderDone, StopReason: "stop"}},
	}}
	core, err := New(Config{Model: "test", Provider: provider, Tools: []Tool{{Name: "bad", Execute: func(context.Context, map[string]any, func(ToolResult)) (ToolResult, error) { panic("boom") }}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Run(context.Background(), "run it", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	result := provider.requests[1].Messages[2]
	if result.Role != RoleToolResult || !result.IsError || result.Content[0].Text == "" {
		t.Fatalf("panic result = %#v", result)
	}
}

func TestRunRejectsConcurrentCalls(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	provider := providerFunc(func(context.Context, Request) (<-chan ProviderEvent, <-chan error) {
		events := make(chan ProviderEvent)
		errs := make(chan error)
		go func() {
			close(started)
			<-unblock
			events <- ProviderEvent{Type: ProviderDone, StopReason: "stop"}
			close(events)
			close(errs)
		}()
		return events, errs
	})
	core, err := New(Config{Model: "test", Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error)
	go func() { finished <- core.Run(context.Background(), "one", func(Event) {}) }()
	<-started
	if err := core.Run(context.Background(), "two", func(Event) {}); err == nil {
		t.Fatal("concurrent Run succeeded")
	}
	close(unblock)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

type providerFunc func(context.Context, Request) (<-chan ProviderEvent, <-chan error)

func (f providerFunc) Stream(ctx context.Context, request Request) (<-chan ProviderEvent, <-chan error) {
	return f(ctx, request)
}
