package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

func structuredTestRequest(model, thinking string) agent.Request {
	return agent.Request{
		Model: model, Thinking: thinking, SystemPrompt: turnSummarySystemPrompt,
		Tools:    []agent.Tool{turnSummaryTool()},
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: `{"assistantResult":"secret payload"}`}}}},
	}
}

func TestParseStructuredJSONText(t *testing.T) {
	want := map[string]any{"summary": "Updated the tests."}
	for _, input := range []string{
		`{"summary":"Updated the tests."}`,
		"  {\"summary\":\"Updated the tests.\"}\n",
		"```json\n{\"summary\":\"Updated the tests.\"}\n```",
		"```\n{\"summary\":\"Updated the tests.\"}\n```",
	} {
		got, err := parseStructuredJSONText(input)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("input=%q got=%#v err=%v", input, got, err)
		}
	}
	for _, input := range []string{
		"",
		"null",
		`["summary"]`,
		`Here it is: {"summary":"x."}`,
		`{"summary":"x."} trailing`,
		`{"summary":"x."}{"summary":"y."}`,
		"```json\n{\"summary\":\"x.\"}",
	} {
		if got, err := parseStructuredJSONText(input); err == nil {
			t.Errorf("accepted %q as %#v", input, got)
		}
	}
}

func TestParseClaudeStructuredStream(t *testing.T) {
	init := `{"type":"system","subtype":"init","session_id":"s","tools":["StructuredOutput"],"mcp_servers":[]}`
	result := `{"type":"result","subtype":"success","session_id":"s","structured_output":{"summary":"Updated the tests."}}`
	got, err := parseClaudeStructuredStream(context.Background(), strings.NewReader(init+"\n"+`{"type":"assistant"}`+"\n"+result))
	if err != nil || !reflect.DeepEqual(got, map[string]any{"summary": "Updated the tests."}) {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	for name, input := range map[string]string{
		"MCP server":      `{"type":"system","subtype":"init","session_id":"s","tools":[],"mcp_servers":[{"name":"x","status":"connected"}]}` + "\n" + result,
		"native tool":     `{"type":"system","subtype":"init","session_id":"s","tools":["Bash"],"mcp_servers":[]}` + "\n" + result,
		"no init":         result,
		"missing output":  init + "\n" + `{"type":"result","subtype":"success","session_id":"s","result":"{\"summary\":\"x.\"}"}`,
		"array output":    init + "\n" + `{"type":"result","subtype":"success","session_id":"s","structured_output":["x"]}`,
		"error result":    init + "\n" + `{"type":"result","subtype":"error_max_turns","session_id":"s","is_error":true,"structured_output":{"summary":"x."}}`,
		"session changed": init + "\n" + `{"type":"result","subtype":"success","session_id":"other","structured_output":{"summary":"x."}}`,
		"no result":       init,
		"after result":    init + "\n" + result + "\n" + `{"type":"assistant"}`,
	} {
		if _, err := parseClaudeStructuredStream(context.Background(), strings.NewReader(input)); err == nil {
			t.Errorf("%s: accepted invalid stream", name)
		}
	}
}

func TestClaudeCompleteStructuredUsesStdinPrivateDirAndNoPersistence(t *testing.T) {
	stubClaudeOnPath(t)
	config := t.TempDir()
	t.Setenv("PI_GO_CONFIG_DIR", config)
	record := t.TempDir()
	binary := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + record + "/args'\ncat > '" + record + "/stdin'\npwd -P > '" + record + "/pwd'\ncat <<'EOF'\n" +
		`{"type":"system","subtype":"init","session_id":"s","tools":["StructuredOutput"],"mcp_servers":[]}` + "\n" +
		`{"type":"result","subtype":"success","session_id":"s","structured_output":{"summary":"Updated the tests."}}` + "\nEOF\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	router := &providerRouter{claude: newClaudeCLIProvider(binary)}
	summary, err := summarizeAssistantTurn(context.Background(), router, "claude/claude-haiku-4-5-20251001", agent.Message{Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Type: "text", Text: "secret payload"}}})
	if err != nil || summary != "Updated the tests." {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	args, _ := os.ReadFile(filepath.Join(record, "args"))
	stdin, _ := os.ReadFile(filepath.Join(record, "stdin"))
	pwd, _ := os.ReadFile(filepath.Join(record, "pwd"))
	for _, flag := range []string{"--no-session-persistence", "--json-schema", "--strict-mcp-config", `{"mcpServers":{}}`, "--system-prompt", claudeStructuredInstruction} {
		if !strings.Contains(string(args), flag) {
			t.Errorf("missing %q in args:\n%s", flag, args)
		}
	}
	for _, forbidden := range []string{"secret payload", "--resume", "submit_turn_summary exactly once"} {
		if strings.Contains(string(args), forbidden) {
			t.Errorf("args contain %q", forbidden)
		}
	}
	if !strings.Contains(string(stdin), "secret payload") {
		t.Errorf("payload not sent on stdin: %q", stdin)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(config, "classifier-workdir"))
	if strings.TrimSpace(string(pwd)) != want {
		t.Errorf("pwd=%q want %q", pwd, want)
	}
}

func TestAgyCompleteStructuredRemovesConversationWithoutMCPCredentials(t *testing.T) {
	t.Setenv("PI_GO_CONFIG_DIR", t.TempDir())
	home := t.TempDir()
	t.Setenv("PI_GO_AGY_HOME", home)
	t.Setenv("PI_GO_AGY_MCP_ADDRESS", "http://127.0.0.1:1/call")
	t.Setenv("PI_GO_AGY_MCP_TOKEN", "inherited")
	t.Setenv(agyMCPBinaryEnv, "/inherited/pi-go-agent")
	if err := setupAgyHome(home); err != nil {
		t.Fatal(err)
	}
	conversation := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	store := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(store, "11111111-2222-3333-4444-555555555555.db")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := t.TempDir()
	response, _ := json.Marshal("```json\n{\"summary\":\"Updated the tests.\"}\n```")
	binary := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$HOME\" \"$PI_GO_AGY_MCP_BINARY\" \"$PI_GO_AGY_MCP_ADDRESS\" \"$PI_GO_AGY_MCP_TOKEN\" > '" + record + "/env'\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + record + "/args'\n" +
		"cd \"$HOME/.gemini/antigravity-cli\" && mkdir -p annotations brain/" + conversation + "/scratch && for f in conversations/" + conversation + ".db conversations/" + conversation + ".db-wal conversations/" + conversation + ".db-shm annotations/" + conversation + ".pbtxt; do printf keep > \"$f\"; done\ncat <<'EOF'\n" +
		`{"event":"init","conversation_id":"` + conversation + `"}` + "\n" +
		`{"event":"result","result":{"conversation_id":"` + conversation + `","status":"SUCCESS","response":` + string(response) + `}}` + "\nEOF\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	provider := newAgyProvider(binary)
	provider.policyReady = true
	router := &providerRouter{agy: provider}
	summary, err := summarizeAssistantTurn(context.Background(), router, "agy/gemini-3.8-flash-low", agent.Message{Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Type: "text", Text: "done"}}})
	if err != nil || summary != "Updated the tests." {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
	env, _ := os.ReadFile(filepath.Join(record, "env"))
	if strings.TrimSpace(string(env)) != home+"|||" {
		t.Errorf("env=%q", env)
	}
	args, _ := os.ReadFile(filepath.Join(record, "args"))
	if strings.Contains(string(args), "--conversation") || !strings.Contains(string(args), `"summary"`) {
		t.Errorf("args=%s", args)
	}
	cli := filepath.Join(home, ".gemini", "antigravity-cli")
	for _, path := range []string{filepath.Join(store, conversation+".db"), filepath.Join(store, conversation+".db-wal"), filepath.Join(store, conversation+".db-shm"), filepath.Join(cli, "annotations", conversation+".pbtxt"), filepath.Join(cli, "brain", conversation)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("conversation file %s was not removed: %v", path, err)
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("unrelated conversation removed: %v", err)
	}
}

func TestStructuredRoutingFailsClosedWithoutCLI(t *testing.T) {
	for _, router := range []*providerRouter{{}, {claude: (*claudeCLIProvider)(nil), agy: (*agyProvider)(nil)}} {
		for _, model := range []string{"claude/claude-haiku-4-5-20251001", "agy/gemini-3.8-flash-low"} {
			if _, err := collectOutputTool(context.Background(), router, structuredTestRequest(model, "low")); err == nil {
				t.Errorf("model %s succeeded without a CLI", model)
			}
		}
	}
}

func TestStreamingOutputToolAppendsCallInstruction(t *testing.T) {
	var prompt string
	provider := qwenTestProviderFunc(func(_ context.Context, request agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
		prompt = request.SystemPrompt
		events := make(chan agent.ProviderEvent, 2)
		events <- agent.ProviderEvent{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Name: "submit_turn_summary", Arguments: map[string]any{"summary": "x."}}}
		events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "toolUse"}
		close(events)
		errs := make(chan error)
		close(errs)
		return events, errs
	})
	if _, err := collectOutputTool(context.Background(), provider, structuredTestRequest("gpt-5.6-luna", "low")); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(prompt, "Call submit_turn_summary exactly once with your result in its individual arguments. Do not return the result as ordinary text, JSON text, or Markdown.") {
		t.Fatalf("prompt=%q", prompt)
	}
}
