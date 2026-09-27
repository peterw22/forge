package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
	"github.com/peterw22/forge/internal/session"
)

func TestClaudeCatalogAndRouting(t *testing.T) {
	t.Setenv("PI_GO_AGY_HOME", t.TempDir())
	binary := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(binary))
	auth := newCodexAuthManagerAt(filepath.Join(t.TempDir(), "auth.json"), defaultCodexAuthEndpoints, http.DefaultClient)
	models, err := configuredModels(auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-opus-5-5", "claude-sonnet-5", "claude-haiku-4-5-20251001"} {
		found := false
		for _, item := range models {
			if item.ID == "claude/"+name && item.Provider == "claude" {
				found = true
			}
		}
		if !found {
			t.Fatalf("Claude model %s missing", name)
		}
	}
	got := ""
	router := &providerRouter{codex: qwenTestProviderFunc(func(_ context.Context, r agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
		got = "codex"
		return closedProviderStream(nil)
	}), claude: qwenTestProviderFunc(func(_ context.Context, r agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
		got = r.Model
		return closedProviderStream(nil)
	})}
	events, errs := router.Stream(context.Background(), agent.Request{Model: "claude/claude-sonnet-5"})
	for range events {
	}
	for range errs {
	}
	if got != "claude/claude-sonnet-5" {
		t.Fatalf("routed to %q", got)
	}
}

func TestClaudeResumesPersistedConversationID(t *testing.T) {
	stubClaudeOnPath(t)
	binary := filepath.Join(t.TempDir(), "claude")
	argsFile := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > '" + argsFile + "'\nprintf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"11111111-2222-3333-4444-555555555555\",\"tools\":[\"mcp__pi-go-agent__bash\",\"mcp__pi-go-agent__read\",\"mcp__pi-go-agent__replace\",\"mcp__pi-go-agent__write\"],\"mcp_servers\":[{\"name\":\"pi-go-agent\",\"status\":\"connected\"}]}' '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"11111111-2222-3333-4444-555555555555\",\"result\":\"ok\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	model := "claude/claude-sonnet-5"
	store, err := session.NewAt(root, root, model, "high")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id := store.ID()
	if err := store.SetProviderBinding(session.ProviderBinding{Provider: "claude", Model: model, CWD: root, ConversationID: "11111111-2222-3333-4444-555555555555"}); err != nil {
		t.Fatal(err)
	}
	p := newClaudeCLIProvider(binary)
	req := agent.Request{Model: model, Thinking: "high", SessionID: id, WorkingDirectory: root, ToolGuard: agyDenyGuard{}, OnToolEvent: func(agent.Event) {}, LoadProviderConversation: func(provider, model, cwd string) (string, error) {
		binding, err := session.ProviderBindingForSession(root, id, provider, model, cwd)
		return binding.ConversationID, err
	}, SaveProviderConversation: func(provider, model, cwd, conversation string) error {
		return store.SetProviderBinding(session.ProviderBinding{Provider: provider, Model: model, CWD: cwd, ConversationID: conversation})
	}, Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "first"}}}, {Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Type: "text", Text: "old"}}}, {Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "next"}}}}}
	events, errs := p.Stream(context.Background(), req)
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "--resume 11111111-2222-3333-4444-555555555555") {
		t.Fatalf("did not resume persisted ID: %s", data)
	}
}

func TestClaudeFakeCLIResumesWithMCPOnlyFlags(t *testing.T) {
	stubClaudeOnPath(t)
	binary := filepath.Join(t.TempDir(), "claude")
	calls := filepath.Join(t.TempDir(), "args")
	script := "#!/bin/sh\nprintf '%s' \"$*\" | tr '\\n' ' ' >> '" + calls + "'\nprintf '\\n' >> '" + calls + "'\nprintf '%s\\n' '" + claudeTestInit + "' '{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}}' '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"session-1\",\"result\":\"ok\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := newClaudeCLIProvider(binary)
	workspace := t.TempDir()
	req := agent.Request{Model: "claude/claude-sonnet-5", Thinking: "xhigh", SessionID: "pi-session", WorkingDirectory: workspace, ToolGuard: agyDenyGuard{}, OnToolEvent: func(agent.Event) {}, Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "hello"}}}}}
	for i := 0; i < 2; i++ {
		events, errs := p.Stream(context.Background(), req)
		var output strings.Builder
		for e := range events {
			if e.Type == agent.ProviderTextDelta {
				output.WriteString(e.Delta)
			}
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if output.String() != "ok" {
			t.Fatalf("output=%q", output.String())
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "--resume") || !strings.Contains(lines[1], "--resume session-1") {
		t.Fatalf("resumption=%q", lines)
	}
	for _, flag := range []string{"--tools", "--restricted", "--strict-mcp-config", "--allowedTools", "--permission-mode dontAsk", "--effort xhigh", "--append-system-prompt", "pi-go-agent MCP server"} {
		if !strings.Contains(lines[0], flag) {
			t.Fatalf("missing %s in %q", flag, lines[0])
		}
	}
}
