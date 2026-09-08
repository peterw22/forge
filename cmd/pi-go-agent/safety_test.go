package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/peterw22/pi-go/internal/agent"
)

type safetyTestProvider struct {
	mu       sync.Mutex
	requests []agent.Request
	decision string
}

func (provider *safetyTestProvider) Stream(_ context.Context, request agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
	provider.mu.Lock()
	provider.requests = append(provider.requests, request)
	provider.mu.Unlock()
	events := make(chan agent.ProviderEvent, 2)
	var arguments map[string]any
	if err := json.Unmarshal([]byte(provider.decision), &arguments); err != nil {
		events <- agent.ProviderEvent{Type: agent.ProviderError, Err: err}
	} else if len(request.Tools) != 1 {
		events <- agent.ProviderEvent{Type: agent.ProviderError}
	} else {
		events <- agent.ProviderEvent{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{
			Type: "toolCall", ID: "output", Name: request.Tools[0].Name, Arguments: arguments,
		}}
	}
	events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "toolUse"}
	close(events)
	errs := make(chan error)
	close(errs)
	return events, errs
}

func TestSafetyRejectionIncludesWriteAndReplacePreviewDetails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.php")
	if err := os.WriteFile(path, []byte("<?php\nreturn 'old';\n"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &safetyTestProvider{decision: `{"allowed":false,"reason":"persistent source mutation","notificationSummary":"This operation may modify persistent source code.","authorization":"none","effectScopes":["modify workspace source"]}`}
	gate := newSafetyGate(provider)
	write := gate.Check(context.Background(), agent.GuardRequest{
		Tool: "write", WorkingDirectory: dir,
		Arguments: map[string]any{"path": "new.php", "content": "<?php\necho 'new';\n"},
	})
	replace := gate.Check(context.Background(), agent.GuardRequest{
		Tool: "replace", WorkingDirectory: dir,
		Arguments: map[string]any{"path": "main.php", "oldText": "return 'old';", "newText": "return 'new';"},
	})
	writeDetails, writeOK := write.Details.(map[string]any)
	replaceDetails, replaceOK := replace.Details.(map[string]any)
	if !writeOK || writeDetails["content"] != "<?php\necho 'new';\n" {
		t.Fatalf("write details=%#v", write.Details)
	}
	if !replaceOK || replaceDetails["oldText"] != "return 'old';" || replaceDetails["newText"] != "return 'new';" || replaceDetails["startLine"] != 2 {
		t.Fatalf("replace details=%#v", replace.Details)
	}
}

func TestSafetyClassifiesWriteAndReplaceButNotOrdinaryRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &safetyTestProvider{decision: `{"allowed":true,"reason":"bounded workspace source update","notificationSummary":"This source update is confined to the workspace.","authorization":"none","effectScopes":["modify workspace source"]}`}
	gate := newSafetyGate(provider)
	write := gate.Check(context.Background(), agent.GuardRequest{
		Tool: "write", WorkingDirectory: dir,
		Arguments: map[string]any{"path": "new.go", "content": "package demo\n"},
	})
	replace := gate.Check(context.Background(), agent.GuardRequest{
		Tool: "replace", WorkingDirectory: dir,
		Arguments: map[string]any{"path": "main.go", "oldText": "package main", "newText": "package demo"},
	})
	read := gate.Check(context.Background(), agent.GuardRequest{
		Tool: "read", WorkingDirectory: dir, Arguments: map[string]any{"path": "main.go"},
	})
	if !write.Allowed || !replace.Allowed || !read.Allowed || len(provider.requests) != 2 {
		t.Fatalf("write=%#v replace=%#v read=%#v requests=%d", write, replace, read, len(provider.requests))
	}
	var operations []map[string]any
	for _, request := range provider.requests {
		var payload struct {
			Operation map[string]any `json:"operation"`
		}
		if err := json.Unmarshal([]byte(request.Messages[0].Content[0].Text), &payload); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, payload.Operation)
	}
	if operations[0]["type"] != "write" || operations[0]["content"] != "package demo\n" {
		t.Fatalf("write operation=%#v", operations[0])
	}
	if operations[1]["type"] != "replace" || operations[1]["newText"] != "package demo" || operations[1]["oldText"] != "package main" {
		t.Fatalf("replace operation=%#v", operations[1])
	}
}

func TestSafetyOmitsSensitiveWriteContentFromClassifier(t *testing.T) {
	dir := t.TempDir()
	provider := &safetyTestProvider{decision: `{"allowed":false,"reason":"sensitive configuration mutation","notificationSummary":"This operation may overwrite sensitive configuration.","authorization":"none","effectScopes":["modify sensitive configuration"]}`}
	gate := newSafetyGate(provider)
	decision := gate.Check(context.Background(), agent.GuardRequest{
		Tool: "write", WorkingDirectory: dir,
		Arguments: map[string]any{"path": ".env", "content": "API_TOKEN=private-value"},
	})
	if decision.Allowed || len(provider.requests) != 1 {
		t.Fatalf("decision=%#v requests=%d", decision, len(provider.requests))
	}
	payload := provider.requests[0].Messages[0].Content[0].Text
	if strings.Contains(payload, "private-value") || !strings.Contains(payload, "contentOmitted") {
		t.Fatalf("sensitive payload=%s", payload)
	}
}

func TestSafetyAllowsOrdinaryWorkspaceReadWithoutClassifier(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &safetyTestProvider{}
	decision := newSafetyGate(provider).Check(context.Background(), agent.GuardRequest{Tool: "read", WorkingDirectory: dir, Arguments: map[string]any{"path": "main.go"}})
	if !decision.Allowed || len(provider.requests) != 0 {
		t.Fatalf("decision=%#v requests=%d", decision, len(provider.requests))
	}
}

func TestSafetyClassifiesBashWithConfiguredModelLow(t *testing.T) {
	provider := &safetyTestProvider{decision: `{"allowed":false,"reason":"deletes workspace files","notificationSummary":"This operation may delete workspace build files.","authorization":"none","effectScopes":["delete ./build"]}`}
	gate := newSafetyGate(provider)
	request := agent.GuardRequest{Tool: "bash", WorkingDirectory: t.TempDir(), Arguments: map[string]any{"command": "rm -rf build"}, Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "clean it"}}}}}
	decision := gate.Check(context.Background(), request)
	if decision.Allowed || decision.Reason != "deletes workspace files" {
		t.Fatalf("decision=%#v", decision)
	}
	if len(provider.requests) != 1 || provider.requests[0].Model != defaultClassifierModel || provider.requests[0].Thinking != safetyThinking || len(provider.requests[0].Tools) != 1 || provider.requests[0].Tools[0].Name != "submit_safety_decision" {
		t.Fatalf("request=%#v", provider.requests)
	}
}

func TestSafetyDoesNotSendSecretScriptSourceToClassifier(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.py"), []byte(`token = "123456789-secret"`), 0600); err != nil {
		t.Fatal(err)
	}
	op, forced := prepareSafetyBash("python deploy.py", dir)
	if !forced || op["scriptSource"] != nil {
		t.Fatalf("operation=%#v forced=%v", op, forced)
	}
}

func TestSafetyUsesSelectedClassifierModel(t *testing.T) {
	provider := &safetyTestProvider{decision: `{"allowed":false,"reason":"unsafe","notificationSummary":"This operation may delete files.","authorization":"none","effectScopes":["delete files"]}`}
	gate := newSafetyGate(provider, func() string { return "gpt-5.6-sol" })
	decision := gate.Check(context.Background(), agent.GuardRequest{
		Tool: "bash", WorkingDirectory: t.TempDir(),
		Arguments: map[string]any{"command": "rm -rf build"},
	})
	if decision.Allowed || len(provider.requests) != 1 || provider.requests[0].Model != "gpt-5.6-sol" {
		t.Fatalf("decision=%#v requests=%#v", decision, provider.requests)
	}
}

func TestClassifierSettingsPersistSecurely(t *testing.T) {
	t.Setenv("PI_GO_CONFIG_DIR", t.TempDir())
	settings, err := newClassifierSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model() != defaultClassifierModel {
		t.Fatalf("default = %q", settings.Model())
	}
	models := []modelInfo{{ID: "gpt-5.6-sol"}}
	if err := settings.SetModel("gpt-5.6-sol", models); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(settings.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	reloaded, err := newClassifierSettings()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Model() != "gpt-5.6-sol" {
		t.Fatalf("reloaded = %q", reloaded.Model())
	}
	if err := reloaded.SetModel("unconfigured-model", models); err == nil {
		t.Fatal("unconfigured classifier accepted")
	}
}

func TestSafetyRejectsInvalidNotificationSummaries(t *testing.T) {
	latest := map[string]any{"text": "approve", "truncated": false}
	base := safetyDecision{Reason: "unsafe", Authorization: "none", EffectScopes: []string{"write data"}}
	for _, summary := range []string{"", "two sentences. not allowed.", "contains\nnewline.", strings.Repeat("a", 221) + ".", "not a sentence", "The token=super-secret was uploaded.", "Ran `rm -rf build`.", "Updated /Users/alice/private/file."} {
		decision := base
		decision.NotificationSummary = summary
		if err := validateSafetyDecision(decision, latest, false); err == nil {
			t.Fatalf("invalid summary accepted: %q", summary)
		}
	}
	base.NotificationSummary = "This operation may write persistent data."
	if err := validateSafetyDecision(base, latest, false); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}
}
