package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

type mcpCallingProvider struct {
	results chan agent.ToolResult
}

// Stream mimics a CLI provider: the tool runs through the MCP bridge inside
// the provider's own turn rather than as an agent.ProviderToolCall.
func (p *mcpCallingProvider) Stream(ctx context.Context, req agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
	events := make(chan agent.ProviderEvent, 2)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		bridge, err := newProviderMCPTurnBridge(ctx, req, false, func() bool { return true })
		if err != nil {
			errs <- err
			return
		}
		defer bridge.Close()
		call, err := httpNewAuthenticatedRequest(ctx, bridge.address+"/call", bridge.token, strings.NewReader(`{"name":"bash","arguments":{"command":"printf ok"}}`))
		if err != nil {
			errs <- err
			return
		}
		resp, err := agyMCPClient.Do(call)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		var result agent.ToolResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			errs <- err
			return
		}
		p.results <- result
		events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: "done"}
		events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "stop"}
	}()
	return events, errs
}

func TestCLIMCPClassifierRejectionAsksForApproval(t *testing.T) {
	for _, approve := range []bool{true, false} {
		provider := &mcpCallingProvider{results: make(chan agent.ToolResult, 1)}
		executed := false
		core, err := agent.New(agent.Config{Model: "test", Provider: provider, SessionID: "s", WorkingDirectory: t.TempDir(), ToolGuard: &countingDenyGuard{}, AllowApproval: true, Tools: []agent.Tool{{Name: "bash", Execute: func(context.Context, map[string]any, func(agent.ToolResult)) (agent.ToolResult, error) {
			executed = true
			return textResult("ok"), nil
		}}}})
		if err != nil {
			t.Fatal(err)
		}
		approvals := make(chan agent.Event, 1)
		if err := core.Run(context.Background(), "do it", func(event agent.Event) {
			if event.Type == agent.EventApprovalRequired {
				approvals <- event
				go core.ResolveApproval(event.ApprovalID, approve)
			}
		}); err != nil {
			t.Fatal(err)
		}
		var approval agent.Event
		select {
		case approval = <-approvals:
		default:
			t.Fatal("classifier rejection did not ask for approval")
		}
		result := <-provider.results
		if !strings.HasPrefix(approval.ToolCallID, "agy-mcp-") || approval.ToolName != "bash" {
			t.Fatalf("approval = %#v", approval)
		}
		if executed != approve || result.IsError == approve {
			t.Fatalf("approve=%v executed=%v result=%#v", approve, executed, result)
		}
		if !approve && !strings.Contains(result.Content[0].Text, "User rejected") {
			t.Fatalf("rejection result = %#v", result)
		}
	}
}
