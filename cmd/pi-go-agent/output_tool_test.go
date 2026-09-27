package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

func outputEventsProvider(events []agent.ProviderEvent, streamErr error) agent.Provider {
	return qwenTestProviderFunc(func(_ context.Context, _ agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
		out := make(chan agent.ProviderEvent, len(events))
		for _, event := range events {
			out <- event
		}
		close(out)
		errs := make(chan error, 1)
		if streamErr != nil {
			errs <- streamErr
		}
		close(errs)
		return out, errs
	})
}

func TestCollectOutputTool(t *testing.T) {
	arguments := map[string]any{"summary": "Updated the tests."}
	call := agent.ProviderEvent{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Type: "toolCall", ID: "result", Name: "submit_turn_summary", Arguments: arguments}}
	done := agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "toolUse"}
	wrong := call
	wrong.ToolCall.Name = "bash"
	for _, test := range []struct {
		name      string
		events    []agent.ProviderEvent
		err       error
		wantError bool
	}{
		{name: "valid", events: []agent.ProviderEvent{call, done}},
		{name: "thinking and text are not result data", events: []agent.ProviderEvent{{Type: agent.ProviderThinkingDelta, Delta: "thinking"}, {Type: agent.ProviderTextDelta, Delta: "not JSON"}, call, done}},
		{name: "text only", events: []agent.ProviderEvent{{Type: agent.ProviderTextDelta, Delta: `{"summary":"Updated the tests."}`}, done}, wantError: true},
		{name: "missing call", events: []agent.ProviderEvent{done}, wantError: true},
		{name: "wrong tool", events: []agent.ProviderEvent{wrong, done}, wantError: true},
		{name: "duplicate", events: []agent.ProviderEvent{call, call, done}, wantError: true},
		{name: "no completion", events: []agent.ProviderEvent{call}, wantError: true},
		{name: "truncated", events: []agent.ProviderEvent{call, {Type: agent.ProviderDone, StopReason: "length"}}, wantError: true},
		{name: "filtered", events: []agent.ProviderEvent{call, {Type: agent.ProviderDone, StopReason: "error"}}, wantError: true},
		{name: "call after done", events: []agent.ProviderEvent{done, call}, wantError: true},
		{name: "duplicate done", events: []agent.ProviderEvent{call, done, done}, wantError: true},
		{name: "error after call", events: []agent.ProviderEvent{call, {Type: agent.ProviderError, Err: errors.New("failed")}}, wantError: true},
		{name: "empty error event", events: []agent.ProviderEvent{call, {Type: agent.ProviderError}}, wantError: true},
		{name: "error channel after completion", events: []agent.ProviderEvent{call, done}, err: errors.New("failed"), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := collectOutputTool(t.Context(), outputEventsProvider(test.events, test.err), agent.Request{Tools: []agent.Tool{turnSummaryTool()}})
			if (err != nil) != test.wantError {
				t.Fatalf("result=%#v err=%v", result, err)
			}
			if err == nil && !reflect.DeepEqual(result, arguments) {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}

func TestCollectOutputToolCancelsProviderOnInvalidCall(t *testing.T) {
	var providerContext context.Context
	provider := qwenTestProviderFunc(func(ctx context.Context, _ agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
		providerContext = ctx
		events := make(chan agent.ProviderEvent, 1)
		events <- agent.ProviderEvent{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Name: "wrong"}}
		return events, make(chan error)
	})
	_, err := collectOutputTool(t.Context(), provider, agent.Request{Tools: []agent.Tool{turnSummaryTool()}})
	if err == nil || providerContext.Err() != context.Canceled {
		t.Fatalf("err=%v provider context=%v", err, providerContext.Err())
	}
}

func TestCollectOutputToolCancellationAfterCall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	provider := qwenTestProviderFunc(func(ctx context.Context, _ agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
		events := make(chan agent.ProviderEvent)
		errs := make(chan error)
		go func() {
			defer close(events)
			defer close(errs)
			select {
			case events <- agent.ProviderEvent{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Name: "submit_turn_summary", Arguments: map[string]any{"summary": "Done."}}}:
			case <-ctx.Done():
			}
			cancel()
		}()
		return events, errs
	})
	_, err := collectOutputTool(ctx, provider, agent.Request{Tools: []agent.Tool{turnSummaryTool()}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestSafetyOutputArguments(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{"allowed": true, "reason": "bounded read", "notificationSummary": "This operation reads workspace files.", "authorization": "none", "effectScopes": []any{}}
	}
	for _, field := range []string{"allowed", "reason", "notificationSummary", "authorization", "effectScopes"} {
		for _, mode := range []string{"missing", "null", "wrong type"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				arguments := valid()
				switch mode {
				case "missing":
					delete(arguments, field)
				case "null":
					arguments[field] = nil
				case "wrong type":
					arguments[field] = 42
				}
				if _, err := safetyDecisionFromArguments(arguments); err == nil {
					t.Fatalf("accepted %#v", arguments)
				}
			})
		}
	}
	for _, value := range []any{[]any{false}, []any{nil}, []any(nil), []string(nil), "scope"} {
		arguments := valid()
		arguments["effectScopes"] = value
		if _, err := safetyDecisionFromArguments(arguments); err == nil {
			t.Fatalf("accepted effectScopes=%#v", value)
		}
	}
	arguments := valid()
	arguments["extra"] = "unexpected"
	if _, err := safetyDecisionFromArguments(arguments); err == nil {
		t.Fatal("accepted extra argument")
	}
	for _, allowed := range []bool{true, false} {
		arguments := valid()
		arguments["allowed"] = allowed
		decision, err := safetyDecisionFromArguments(arguments)
		if err != nil || decision.Allowed != allowed {
			t.Fatalf("decision=%#v err=%v", decision, err)
		}
	}
}

func TestSafetyOutputFailuresNeverApprove(t *testing.T) {
	for _, events := range [][]agent.ProviderEvent{
		{{Type: agent.ProviderTextDelta, Delta: `{"allowed":true}`}, {Type: agent.ProviderDone, StopReason: "stop"}},
		{{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Name: "submit_safety_decision", Arguments: map[string]any{"allowed": true}}}, {Type: agent.ProviderDone, StopReason: "toolUse"}},
	} {
		gate := newSafetyGate(outputEventsProvider(events, nil))
		decision := gate.Check(t.Context(), agent.GuardRequest{Tool: "bash", WorkingDirectory: t.TempDir(), Arguments: map[string]any{"command": "pwd"}})
		if decision.Allowed || decision.NotificationSummary != "The safety classifier could not evaluate this operation." {
			t.Fatalf("decision=%#v", decision)
		}
	}
}

func TestTurnSummaryRejectsMalformedToolFields(t *testing.T) {
	for _, arguments := range []map[string]any{nil, {}, {"summary": nil}, {"summary": true}, {"summary": "Done.", "extra": "value"}} {
		provider := outputEventsProvider([]agent.ProviderEvent{
			{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Name: "submit_turn_summary", Arguments: arguments}},
			{Type: agent.ProviderDone, StopReason: "toolUse"},
		}, nil)
		if _, err := summarizeAssistantTurn(t.Context(), provider, defaultClassifierModel, agent.Message{}); err == nil {
			t.Fatalf("accepted %#v", arguments)
		}
	}
}

func TestOutputToolsUseAutoChoiceWithThinking(t *testing.T) {
	for _, tool := range []agent.Tool{safetyDecisionTool(), turnSummaryTool()} {
		if tool.Execute != nil || tool.Parameters["additionalProperties"] != false {
			t.Fatalf("unexpected output tool definition: %#v", tool)
		}
		request := agent.Request{Model: defaultClassifierModel, Thinking: safetyThinking, Tools: []agent.Tool{tool}}
		codex, err := buildCodexRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		if codex.ToolChoice != "auto" || len(codex.Tools) != 1 || codex.Reasoning.(map[string]string)["effort"] != safetyThinking {
			t.Fatalf("Codex request=%#v", codex)
		}
		qwen, err := buildQwenRequest(request)
		if err != nil {
			t.Fatal(err)
		}
		if qwen.ToolChoice != "auto" || len(qwen.Tools) != 1 {
			t.Fatalf("Qwen request=%#v", qwen)
		}
	}
}

func TestCodexIncompleteToolCallDoesNotBecomeSuccessfulCompletion(t *testing.T) {
	events := make(chan agent.ProviderEvent, 8)
	consumer := newCodexEventConsumer(events)
	consumer.tools["item"] = &codexToolCall{itemID: "item", callID: "call", name: "submit_turn_summary"}
	consumer.toolOrder = []string{"item"}
	consumer.tools["item"].arguments.WriteString(`{"summary":"Done."}`)
	if err := consumer.consume(codexResponseEvent{Type: "response.incomplete"}); err != nil {
		t.Fatal(err)
	}
	close(events)
	for event := range events {
		if event.Type == agent.ProviderDone && event.StopReason != "length" {
			t.Fatalf("incomplete response reported as %q", event.StopReason)
		}
	}
}
