package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

type agyDenyGuard struct{}

func (agyDenyGuard) Check(context.Context, agent.GuardRequest) agent.GuardDecision {
	return agent.GuardDecision{Reason: "denied by test classifier"}
}

func TestAgyTurnBridgeDeniesBeforeExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := false
	tools := []agent.Tool{{Name: "write", Execute: func(context.Context, map[string]any, func(agent.ToolResult)) (agent.ToolResult, error) {
		ran = true
		return agent.ToolResult{}, nil
	}}}
	bridge, err := newAgyTurnBridge(ctx, t.TempDir(), agyDenyGuard{}, nil, tools)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	payload := strings.NewReader(`{"name":"write","arguments":{"path":"x"}}`)
	request, err := httpNewAuthenticatedRequest(ctx, bridge.address+"/call", bridge.token, payload)
	if err != nil {
		t.Fatal(err)
	}
	response, err := agyMCPClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result agent.ToolResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if ran || !result.IsError {
		t.Fatalf("denied tool ran=%v result=%#v", ran, result)
	}
	unauthorized, err := http.Post(bridge.address+"/call", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.StatusCode)
	}
}

func TestClaudeBridgeRejectsToolBeforeVerifiedInit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := false
	tools := []agent.Tool{{Name: "read", Execute: func(context.Context, map[string]any, func(agent.ToolResult)) (agent.ToolResult, error) {
		ran = true
		return textResult("unsafe"), nil
	}}}
	bridge, err := newMCPGuardedTurnBridge(ctx, t.TempDir(), agyDenyGuard{}, nil, tools, false, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	request, err := httpNewAuthenticatedRequest(ctx, bridge.address+"/call", bridge.token, strings.NewReader(`{"name":"read","arguments":{"path":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := agyMCPClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var result agent.ToolResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if ran || !result.IsError || !strings.Contains(result.Content[0].Text, "not yet verified") {
		t.Fatalf("ran=%v result=%#v", ran, result)
	}
}

func TestAgyMCPProtocol(t *testing.T) {
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{"path":"x"}}}` + "\n")
	var output bytes.Buffer
	invoked := 0
	tools := []agent.Tool{{Name: "read", Description: "read", Parameters: objectSchema("path")}}
	if err := serveAgyMCP(context.Background(), input, &output, tools, func(_ context.Context, name string, args map[string]any) (agent.ToolResult, error) {
		invoked++
		return textResult("ok"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if invoked != 1 || !strings.Contains(output.String(), `"isError":false`) || !strings.Contains(output.String(), `"tools"`) {
		t.Fatalf("output=%s invoked=%d", output.String(), invoked)
	}
}
