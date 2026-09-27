package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

type countingDenyGuard struct{ calls int }

func (g *countingDenyGuard) Check(context.Context, agent.GuardRequest) agent.GuardDecision {
	g.calls++
	return agent.GuardDecision{Reason: "classifier rejected"}
}

func TestCLIMCPYoloBypassesOnlyClassifier(t *testing.T) {
	for _, provider := range []string{"agy", "claude"} {
		t.Run(provider, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			guard := &countingDenyGuard{}
			executed := 0
			tools := []agent.Tool{{Name: "bash", Execute: func(context.Context, map[string]any, func(agent.ToolResult)) (agent.ToolResult, error) {
				executed++
				return textResult("ok"), nil
			}}}
			bridge, err := newMCPGuardedTurnBridgeWithYOLO(ctx, t.TempDir(), guard, nil, tools, false, func() bool { return true }, true)
			if err != nil {
				t.Fatal(err)
			}
			defer bridge.Close()
			invoke := func(name, token string) (int, agent.ToolResult) {
				req, err := httpNewAuthenticatedRequest(ctx, bridge.address+"/call", token, strings.NewReader(`{"name":"`+name+`","arguments":{"command":"printf ok"}}`))
				if err != nil {
					t.Fatal(err)
				}
				resp, err := agyMCPClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var result agent.ToolResult
				if resp.StatusCode == http.StatusOK {
					if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
						t.Fatal(err)
					}
				}
				return resp.StatusCode, result
			}
			if code, _ := invoke("bash", "invalid"); code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated status=%d", code)
			}
			code, result := invoke("unknown", bridge.token)
			if code != http.StatusOK || !result.IsError || executed != 0 {
				t.Fatalf("unknown tool code=%d result=%#v", code, result)
			}
			code, result = invoke("bash", bridge.token)
			if code != http.StatusOK || result.IsError || executed != 1 || guard.calls != 0 {
				t.Fatalf("YOLO code=%d result=%#v executed=%d checks=%d", code, result, executed, guard.calls)
			}
		})
	}
}

func TestCLIMCPYoloCannotSkipClaudeInventoryCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := false
	tools := []agent.Tool{{Name: "read", Execute: func(context.Context, map[string]any, func(agent.ToolResult)) (agent.ToolResult, error) {
		ran = true
		return textResult("unsafe"), nil
	}}}
	bridge, err := newMCPGuardedTurnBridgeWithYOLO(ctx, t.TempDir(), &countingDenyGuard{}, nil, tools, false, func() bool { return false }, true)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	req, err := httpNewAuthenticatedRequest(ctx, bridge.address+"/call", bridge.token, strings.NewReader(`{"name":"read","arguments":{"path":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := agyMCPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result agent.ToolResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if ran || !result.IsError || !strings.Contains(result.Content[0].Text, "not yet verified") {
		t.Fatalf("ran=%v result=%#v", ran, result)
	}
}
