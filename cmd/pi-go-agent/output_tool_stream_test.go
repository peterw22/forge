package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

func TestSummaryConsumesStreamedToolArguments(t *testing.T) {
	for _, backend := range []string{"codex", "qwen"} {
		for _, malformed := range []bool{false, true} {
			name := backend + "/valid"
			if malformed {
				name = backend + "/malformed"
			}
			t.Run(name, func(t *testing.T) {
				provider := qwenTestProviderFunc(func(_ context.Context, _ agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
					events := make(chan agent.ProviderEvent, 8)
					errs := make(chan error, 1)
					fragments := []string{`{"summary":"Updated `, `the tests."}`}
					if malformed {
						fragments[1] = `the tests.`
					}
					var err error
					if backend == "codex" {
						consumer := newCodexEventConsumer(events)
						input := []codexResponseEvent{{Type: "response.output_item.added", Item: json.RawMessage(`{"type":"function_call","id":"item","call_id":"call","name":"submit_turn_summary"}`)}}
						for _, fragment := range fragments {
							input = append(input, codexResponseEvent{Type: "response.function_call_arguments.delta", ItemID: "item", Delta: fragment})
						}
						input = append(input, codexResponseEvent{Type: "response.completed"})
						for _, event := range input {
							if err = consumer.consume(event); err != nil {
								break
							}
						}
					} else {
						var input bytes.Buffer
						for index, fragment := range fragments {
							function := map[string]any{"arguments": fragment}
							tool := map[string]any{"index": 0, "function": function}
							var finish any
							if index == 0 {
								tool["id"] = "call"
								function["name"] = "submit_turn_summary"
							} else {
								finish = "tool_calls"
							}
							data, marshalErr := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{tool}}, "finish_reason": finish}}})
							if marshalErr != nil {
								t.Fatal(marshalErr)
							}
							input.WriteString("data: " + string(data) + "\n\n")
						}
						input.WriteString("data: [DONE]\n\n")
						err = readQwenSSE(&input, events)
					}
					if err != nil {
						errs <- err
					}
					close(events)
					close(errs)
					return events, errs
				})
				summary, err := summarizeAssistantTurn(t.Context(), provider, defaultClassifierModel, agent.Message{})
				if malformed {
					if err == nil || !strings.Contains(err.Error(), "arguments") {
						t.Fatalf("expected malformed arguments error, got summary=%q err=%v", summary, err)
					}
				} else if err != nil || summary != "Updated the tests." {
					t.Fatalf("summary=%q err=%v", summary, err)
				}
			})
		}
	}
}
