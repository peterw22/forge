package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
	"github.com/peterw22/forge/internal/session"
)

func TestAgyProviderFailsClosedWithoutPolicy(t *testing.T) {
	p := newAgyProvider("agy")
	req := agent.Request{Model: "agy/gemini-3.8-flash-low", SessionID: "test", Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "hello"}}}}}
	events, errs := p.Stream(context.Background(), req)
	for range events {
	}
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAgyResumesPersistedConversationID(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agy")
	argsFile := filepath.Join(t.TempDir(), "args")
	conversation := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + argsFile + "'\nprintf '%s\\n' '{\"event\":\"init\",\"conversation_id\":\"" + conversation + "\"}' '{\"event\":\"result\",\"result\":{\"conversation_id\":\"" + conversation + "\",\"status\":\"SUCCESS\",\"response\":\"ok\"}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("PI_GO_AGY_HOME", home)
	if err := setupAgyHome(home); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewAt(workspace, workspace, "agy/gemini-3.8-flash-low", "high")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := store.ID()
	if err := store.SetProviderBinding(session.ProviderBinding{Provider: "agy", Model: "agy/gemini-3.8-flash-low", CWD: workspace, ConversationID: conversation}); err != nil {
		t.Fatal(err)
	}
	provider := newAgyProvider(binary)
	provider.policyReady = true
	req := agent.Request{Model: "agy/gemini-3.8-flash-low", SessionID: id, WorkingDirectory: workspace, ToolGuard: agyDenyGuard{}, OnToolEvent: func(agent.Event) {}, LoadProviderConversation: func(provider, model, cwd string) (string, error) {
		binding, err := session.ProviderBindingForSession(workspace, id, provider, model, cwd)
		return binding.ConversationID, err
	}, SaveProviderConversation: func(provider, model, cwd, next string) error {
		return store.SetProviderBinding(session.ProviderBinding{Provider: provider, Model: model, CWD: cwd, ConversationID: next})
	}, Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "first"}}}, {Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Type: "text", Text: "prior"}}}, {Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "next"}}}}}
	events, errs := provider.Stream(context.Background(), req)
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "--conversation "+conversation) {
		t.Fatalf("did not resume persisted agy conversation: %s", data)
	}
}

func TestAgyProviderFakeCLIStreamsAndResumes(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agy")
	calls := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s' \"$*\" | tr '\\n' ' ' >> '" + calls + "'\nprintf '\\n' >> '" + calls + "'\nprintf '%s\\n' '{\"event\":\"init\",\"conversation_id\":\"id\"}' '{\"event\":\"step_update\",\"step_update\":{\"step_type\":\"agent_response\",\"text_delta\":\"hello\"}}' '{\"event\":\"result\",\"result\":{\"conversation_id\":\"id\",\"status\":\"SUCCESS\",\"response\":\"hello\"}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	workspace := t.TempDir()
	for _, dir := range []string{filepath.Join(home, ".gemini", "antigravity-cli"), filepath.Join(home, ".gemini", "config")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	settings := `{"permissions":{"allow":["mcp(pi-go-agent/*)"],"deny":["read_file(*)","write_file(*)","read_url(*)","execute_url(*)","command(*)","unsandboxed(*)"]}}`
	if err := os.WriteFile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"pi-go-agent": managedAgyMCPServer()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gemini", "config", "mcp_config.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_GO_AGY_HOME", home)
	p := newAgyProvider(binary)
	p.policyReady = true
	req := agent.Request{Model: "agy/gemini-3.8-flash-low", SessionID: "session-1", WorkingDirectory: workspace, ToolGuard: agyDenyGuard{}, OnToolEvent: func(agent.Event) {}, Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "hello"}}}}}
	for i := 0; i < 2; i++ {
		events, errs := p.Stream(context.Background(), req)
		var text string
		for event := range events {
			if event.Type == agent.ProviderTextDelta {
				text += event.Delta
			}
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if text != "hello" {
			t.Fatalf("text=%q", text)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "--conversation") || !strings.Contains(lines[1], "--conversation id") {
		t.Fatalf("invocations=%q", lines)
	}
	if !strings.Contains(lines[0], "pi-go-agent MCP server") || !strings.Contains(lines[0], "User request: hello") {
		t.Fatalf("initial agy tool instruction missing: %q", lines[0])
	}
	if strings.Contains(lines[1], "pi-go-agent MCP server") || !strings.Contains(lines[1], "--print=hello") {
		t.Fatalf("resumed agy turn should inherit instruction: %q", lines[1])
	}
}
