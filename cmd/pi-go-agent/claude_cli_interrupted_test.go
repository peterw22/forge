package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

// A turn that dies after Claude starts has already persisted tool calls and
// commentary, so its conversation ID must be saved too; otherwise every later
// turn fails with "Claude conversation ID missing".
func TestClaudeInterruptedTurnSavesConversation(t *testing.T) {
	stubClaudeOnPath(t)
	binary := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nprintf '%s\\n' '" + claudeTestInit + "' '{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"I will look.\"}}}'\nexit 1\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var saved []string
	req := agent.Request{Model: "claude/claude-sonnet-5", Thinking: "high", SessionID: "pi-session", WorkingDirectory: t.TempDir(), ToolGuard: agyDenyGuard{}, OnToolEvent: func(agent.Event) {},
		SaveProviderConversation: func(provider, model, cwd, id string) error {
			saved = append(saved, provider+" "+id)
			return nil
		},
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "hello"}}}}}
	events, errs := newClaudeCLIProvider(binary).Stream(context.Background(), req)
	for range events {
	}
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "ended without a result") {
		t.Fatalf("err = %v", err)
	}
	if len(saved) != 1 || saved[0] != "claude session-1" {
		t.Fatalf("saved = %q", saved)
	}
}
