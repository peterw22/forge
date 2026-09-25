package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
)

var agyMCPClient = &http.Client{} // The turn ends on completion or explicit abort, not a fixed tool timeout.

func httpNewAuthenticatedRequest(ctx context.Context, address, token string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

type agyTurnBridge struct {
	listener       net.Listener
	server         *http.Server
	address, token string
}

func newAgyTurnBridge(ctx context.Context, workspace string, guard agent.ToolGuard, messages []agent.Message, tools []agent.Tool, notify ...func(agent.Event)) (*agyTurnBridge, error) {
	return newMCPGuardedTurnBridge(ctx, workspace, guard, messages, tools, true, nil, notify...)
}

func newMCPGuardedTurnBridge(ctx context.Context, workspace string, guard agent.ToolGuard, messages []agent.Message, tools []agent.Tool, verifyAgyPolicy bool, ready func() bool, notify ...func(agent.Event)) (*agyTurnBridge, error) {
	return newMCPGuardedTurnBridgeWithYOLO(ctx, workspace, guard, messages, tools, verifyAgyPolicy, ready, false, notify...)
}

// YOLO skips only the classifier. The MCP authentication, provider tool
// allowlist, and agy's deny-native policy remain mandatory.
func newMCPGuardedTurnBridgeWithYOLO(ctx context.Context, workspace string, guard agent.ToolGuard, messages []agent.Message, tools []agent.Tool, verifyAgyPolicy bool, ready func() bool, yolo bool, notify ...func(agent.Event)) (*agyTurnBridge, error) {
	if guard == nil {
		return nil, errors.New("agy MCP classifier is required")
	}
	decide := func(ctx context.Context, call agent.ContentBlock) agent.GuardDecision {
		if yolo {
			return agent.GuardDecision{Allowed: true}
		}
		return guard.Check(ctx, agent.GuardRequest{Tool: call.Name, Arguments: call.Arguments, WorkingDirectory: workspace, Messages: messages})
	}
	return newMCPTurnBridge(ctx, workspace, decide, tools, verifyAgyPolicy, ready, notify...)
}

// newProviderMCPTurnBridge prefers the agent's own tool policy so a classifier
// rejection can still be manually approved, exactly as for native tool calls.
func newProviderMCPTurnBridge(ctx context.Context, req agent.Request, verifyAgyPolicy bool, ready func() bool) (*agyTurnBridge, error) {
	if req.GuardTool == nil {
		return newMCPGuardedTurnBridgeWithYOLO(ctx, req.WorkingDirectory, req.ToolGuard, req.Messages, req.Tools, verifyAgyPolicy, ready, req.YOLO, req.OnToolEvent)
	}
	if req.ToolGuard == nil {
		return nil, errors.New("agy MCP classifier is required")
	}
	return newMCPTurnBridge(ctx, req.WorkingDirectory, req.GuardTool, req.Tools, verifyAgyPolicy, ready, req.OnToolEvent)
}

func newMCPTurnBridge(ctx context.Context, workspace string, decide func(context.Context, agent.ContentBlock) agent.GuardDecision, tools []agent.Tool, verifyAgyPolicy bool, ready func() bool, notify ...func(agent.Event)) (*agyTurnBridge, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	bridge := &agyTurnBridge{listener: listener, address: "http://" + listener.Addr().String(), token: hex.EncodeToString(tokenBytes)}
	mux := http.NewServeMux()
	mux.HandleFunc("/call", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+bridge.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var invocation struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&invocation); err != nil {
			http.Error(w, "invalid call", 400)
			return
		}
		emit := func(event agent.Event) {
			if len(notify) > 0 && notify[0] != nil {
				notify[0](event)
			}
		}
		callID := fmt.Sprintf("agy-mcp-%d", time.Now().UnixNano())
		emit(agent.Event{Type: agent.EventToolExecutionStart, ToolCallID: callID, ToolName: invocation.Name, Arguments: invocation.Arguments})
		result := agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: "Blocked by safety classifier"}}, IsError: true}
		for _, tool := range tools {
			if tool.Name == invocation.Name {
				if ready != nil && !ready() {
					result.Content[0].Text = "MCP tool boundary not yet verified"
					break
				}
				if verifyAgyPolicy {
					home, err := agyHome()
					if err != nil {
						result.Content[0].Text = err.Error()
						break
					}
					if err := checkAgyPolicy(home, workspace); err != nil {
						result.Content[0].Text = "Agy policy changed: " + err.Error()
						break
					}
				}
				decision := decide(r.Context(), agent.ContentBlock{Type: "toolCall", ID: callID, Name: tool.Name, Arguments: invocation.Arguments})
				if !decision.Allowed {
					result.Content[0].Text += "; " + decision.Reason
					break
				}
				value, err := tool.Execute(r.Context(), invocation.Arguments, func(update agent.ToolResult) {
					emit(agent.Event{Type: agent.EventToolExecutionUpdate, ToolCallID: callID, ToolName: tool.Name, Result: &update})
				})
				if err != nil {
					result.Content[0].Text = "Tool failed: " + err.Error()
					break
				}
				result = value
				break
			}
		}
		emit(agent.Event{Type: agent.EventToolExecutionEnd, ToolCallID: callID, ToolName: invocation.Name, Result: &result, IsError: result.IsError})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
	bridge.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = bridge.server.Close() }()
	go func() { _ = bridge.server.Serve(listener) }()
	return bridge, nil
}
func (b *agyTurnBridge) Close() { _ = b.server.Close() }

// checkAgyPolicy accepts only an explicit, private MCP-only installation.
// It never edits the user's existing global agy settings or MCP servers.
func checkAgyPolicy(home, workspace string) error {
	if strings.TrimSpace(workspace) == "" || !filepath.IsAbs(workspace) {
		return errors.New("agy requires an absolute workspace")
	}
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	data, err := os.ReadFile(settings)
	if err != nil {
		return fmt.Errorf("agy policy: %w", err)
	}
	var document struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
			Ask   []string `json:"ask"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return err
	}
	if len(document.Permissions.Deny) != 6 {
		return errors.New("agy policy includes unexpected deny rules")
	}
	for _, rule := range document.Permissions.Allow {
		if rule != "mcp(pi-go-agent/*)" {
			return errors.New("agy policy allows an unexpected tool")
		}
	}
	needed := map[string]bool{"read_file(*)": false, "write_file(*)": false, "read_url(*)": false, "execute_url(*)": false, "command(*)": false, "unsandboxed(*)": false}
	for _, rule := range document.Permissions.Deny {
		if _, ok := needed[rule]; ok {
			needed[rule] = true
		}
		if strings.HasPrefix(rule, "mcp(") {
			return errors.New("agy policy denies MCP")
		}
	}
	for rule, found := range needed {
		if !found {
			return fmt.Errorf("agy policy lacks %s denial", rule)
		}
	}
	if len(document.Permissions.Allow) != 1 || document.Permissions.Allow[0] != "mcp(pi-go-agent/*)" || len(document.Permissions.Ask) != 0 {
		return errors.New("agy policy allows non-MCP tools")
	}
	config := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	data, err = os.ReadFile(config)
	if err != nil {
		return err
	}
	var servers struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &servers); err != nil {
		return err
	}
	entry, ok := servers.MCPServers["pi-go-agent"]
	if !ok || len(servers.MCPServers) != 1 || entry.Command != agyMCPLauncher || len(entry.Args) != 2 || entry.Args[0] != "-c" || entry.Args[1] != agyMCPScript || len(entry.Env) != 0 {
		return errors.New("agy MCP configuration must launch exclusively this agent's stdio child")
	}
	// Workspace-local MCP definitions may override or add servers.
	path := workspace
	for {
		for _, name := range []string{filepath.Join(path, ".agents", "mcp_config.json"), filepath.Join(path, ".antigravitycli", "mcp_config.json")} {
			if _, err := os.Stat(name); err == nil {
				return errors.New("workspace MCP override is not allowed")
			}
		}
		next := filepath.Dir(path)
		if next == path {
			break
		}
		path = next
	}
	return nil
}
