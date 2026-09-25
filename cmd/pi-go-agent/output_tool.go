package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/peterw22/pi-go/internal/agent"
)

// structuredOutputProvider answers an output-only request directly. CLI
// providers run tools internally and cannot surface a tool call, so they return
// the would-be tool arguments instead. Each implementation appends its own
// output-mechanism instruction to the system prompt.
type structuredOutputProvider interface {
	CompleteStructured(ctx context.Context, request agent.Request) (map[string]any, error)
}

// structuredOutputRouter selects a direct structured provider for a model, or
// returns nil when the model uses the streaming output-tool path.
type structuredOutputRouter interface {
	structuredFor(model string) structuredOutputProvider
}

// collectOutputTool consumes an output-only tool call, never executing it or
// feeding a tool result back to the model. Providers use automatic tool choice;
// ordinary response text is not a fallback source of structured results on the
// streaming path. Callers validate the returned arguments identically for every
// provider.
func collectOutputTool(ctx context.Context, provider agent.Provider, request agent.Request) (map[string]any, error) {
	if len(request.Tools) != 1 {
		return nil, errors.New("structured output requires exactly one tool definition")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expected := request.Tools[0].Name
	if router, ok := provider.(structuredOutputRouter); ok {
		if direct := router.structuredFor(request.Model); direct != nil {
			return direct.CompleteStructured(ctx, request)
		}
	}
	request.SystemPrompt += "\n\nCall " + expected + " exactly once with your result in its individual arguments. Do not return the result as ordinary text, JSON text, or Markdown."
	events, errs := provider.Stream(ctx, request)
	var arguments map[string]any
	var called, done bool
	for events != nil || errs != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch event.Type {
			case agent.ProviderToolCall:
				if done || called || event.ToolCall.Name != expected {
					return nil, fmt.Errorf("expected exactly one %s tool call before completion", expected)
				}
				called = true
				arguments = event.ToolCall.Arguments
			case agent.ProviderDone:
				if done || (event.StopReason != "toolUse" && event.StopReason != "stop") {
					return nil, fmt.Errorf("output tool stream did not complete successfully: %q", event.StopReason)
				}
				done = true
			case agent.ProviderError:
				if event.Err != nil {
					return nil, event.Err
				}
				return nil, errors.New("output tool stream failed")
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return nil, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !done || !called {
		return nil, fmt.Errorf("output tool stream ended without completion or a %s call", expected)
	}
	return arguments, nil
}

// Require all and only the declared fields, including explicit false booleans
// and empty arrays. Null/missing values must not silently become Go zero values.
func requireOutputFields(arguments map[string]any, fields ...string) error {
	if len(arguments) != len(fields) {
		return errors.New("output tool returned missing or unexpected arguments")
	}
	for _, field := range fields {
		if value, ok := arguments[field]; !ok || value == nil {
			return fmt.Errorf("output tool requires argument %q", field)
		}
	}
	return nil
}

func outputString(arguments map[string]any, field string) (string, error) {
	value, ok := arguments[field].(string)
	if !ok {
		return "", fmt.Errorf("output tool argument %q must be a string", field)
	}
	return value, nil
}
