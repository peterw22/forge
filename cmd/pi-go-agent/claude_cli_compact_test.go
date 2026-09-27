package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

const claudeCompactTestID = "1d7f9d2d-52cb-47cb-901b-795aac4e62f4"

// Shape observed from Claude Code 2.1.282 for `--print --resume <id> /compact`.
var claudeCompactTestStream = strings.Join([]string{
	`{"type":"system","subtype":"status","status":"compacting","session_id":"` + claudeCompactTestID + `"}`,
	`{"type":"rate_limit_event","session_id":"` + claudeCompactTestID + `"}`,
	`{"type":"system","subtype":"status","status":null,"session_id":"` + claudeCompactTestID + `"}`,
	`{"type":"system","subtype":"init","session_id":"` + claudeCompactTestID + `","tools":[],"mcp_servers":[]}`,
	`{"type":"system","subtype":"compact_boundary","session_id":"` + claudeCompactTestID + `","compact_metadata":{"trigger":"manual","pre_tokens":7238,"post_tokens":800}}`,
	`{"type":"user","message":{"role":"user","content":"This session is being continued from a previous conversation. Summary: codeword PELICAN-42."},"session_id":"` + claudeCompactTestID + `"}`,
	`{"type":"user","message":{"role":"user","content":"<local-command-stdout>Compacted </local-command-stdout>"},"session_id":"` + claudeCompactTestID + `"}`,
	`{"type":"result","subtype":"success","is_error":false,"result":"","session_id":"` + claudeCompactTestID + `","usage":{"input_tokens":10,"output_tokens":5}}`,
}, "\n")

func TestParseClaudeCompactStream(t *testing.T) {
	result, err := parseClaudeCompactStream(context.Background(), strings.NewReader(claudeCompactTestStream), claudeCompactTestID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "PELICAN-42") || result.TokensBefore != 7238 || result.TokensAfter != 800 || result.Usage.TotalTokens != 15 {
		t.Fatalf("result=%#v", result)
	}
	lines := strings.Split(claudeCompactTestStream, "\n")
	without := func(index int) string {
		return strings.Join(append(append([]string(nil), lines[:index]...), lines[index+1:]...), "\n")
	}
	for name, input := range map[string]string{
		"other session":  strings.ReplaceAll(claudeCompactTestStream, claudeCompactTestID, "other"),
		"no boundary":    without(4),
		"no summary":     without(5),
		"no result":      without(7),
		"answered":       strings.Replace(claudeCompactTestStream, `{"type":"rate_limit_event"`, `{"type":"assistant"`, 1),
		"tools exposed":  strings.Replace(claudeCompactTestStream, `"tools":[]`, `"tools":["Bash"]`, 1),
		"auto trigger":   strings.Replace(claudeCompactTestStream, `"trigger":"manual"`, `"trigger":"auto"`, 1),
		"failed result":  strings.Replace(claudeCompactTestStream, `"is_error":false`, `"is_error":true`, 1),
		"after result":   claudeCompactTestStream + "\n" + lines[1],
		"not JSON":       claudeCompactTestStream + "\nnot json",
		"double compact": strings.Replace(claudeCompactTestStream, lines[5], lines[4]+"\n"+lines[5], 1),
	} {
		if _, err := parseClaudeCompactStream(context.Background(), strings.NewReader(input), claudeCompactTestID); err == nil {
			t.Errorf("%s: accepted invalid stream", name)
		}
	}
}

func TestAgentCompactForwardsToClaude(t *testing.T) {
	stubClaudeOnPath(t)
	workspace := t.TempDir()
	record := t.TempDir()
	binary := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + record + "/args'\npwd -P > '" + record + "/pwd'\ncat <<'EOF'\n" + claudeCompactTestStream + "\nEOF\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	model := "claude/claude-sonnet-5"
	core, err := agent.New(agent.Config{Model: model, Thinking: "high", WorkingDirectory: workspace, SessionID: "session", Provider: &providerRouter{claude: newClaudeCLIProvider(binary)}})
	if err != nil {
		t.Fatal(err)
	}
	core.ConfigureProviderSession(nil, func(provider, selected, cwd string) (string, error) {
		if provider != "claude" || selected != model || cwd != workspace {
			t.Errorf("load(%q, %q, %q)", provider, selected, cwd)
		}
		return claudeCompactTestID, nil
	}, nil)
	core.Restore([]agent.Message{
		{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "Remember PELICAN-42."}}},
		{Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Type: "text", Text: "OK."}}},
	}, model, "high", agent.Usage{})
	var persisted agent.CompactionResult
	result, err := core.Compact(context.Background(), "keep the codeword", func(value agent.CompactionResult) error { persisted = value; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(persisted.Summary, "PELICAN-42") || persisted.TokensBefore != 7238 || persisted.EstimatedTokensAfter != 800 || len(result.RetainedTail) != 0 {
		t.Fatalf("persisted=%#v", persisted)
	}
	messages := core.Snapshot().Messages
	if len(messages) != 1 || messages[0].Role != agent.RoleCompactionSummary {
		t.Fatalf("active context=%#v", messages)
	}
	args, _ := os.ReadFile(filepath.Join(record, "args"))
	for _, want := range []string{"--resume\n" + claudeCompactTestID + "\n", "\n/compact keep the codeword\n", "--effort\nhigh\n", "--tools\n\n"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("missing %q in args:\n%s", want, args)
		}
	}
	if strings.Contains(string(args), "--disable-slash-commands") {
		t.Error("slash commands disabled for /compact")
	}
	pwd, _ := os.ReadFile(filepath.Join(record, "pwd"))
	want, _ := filepath.EvalSymlinks(workspace)
	if strings.TrimSpace(string(pwd)) != want {
		t.Errorf("pwd=%q want %q", pwd, want)
	}
}

func TestCompactConversationRouting(t *testing.T) {
	stubClaudeOnPath(t)
	router := &providerRouter{claude: (*claudeCLIProvider)(nil)}
	for model, wantHandled := range map[string]bool{"gpt-5.6-luna": false, "qwen/qwen-coder": false, "claude/claude-sonnet-5": true, "agy/gemini-3.8-flash-low": true} {
		_, handled, err := router.CompactConversation(context.Background(), agent.Request{Model: model}, "")
		if handled != wantHandled || (handled && err == nil) {
			t.Errorf("%s: handled=%v err=%v", model, handled, err)
		}
	}
	provider := newClaudeCLIProvider("unused")
	router.claude = provider
	_, _, err := router.CompactConversation(context.Background(), agent.Request{Model: "claude/claude-sonnet-5", SessionID: "session", WorkingDirectory: t.TempDir()}, "")
	if err == nil || !strings.Contains(err.Error(), "no Claude conversation") {
		t.Fatalf("unbound session: %v", err)
	}
}
