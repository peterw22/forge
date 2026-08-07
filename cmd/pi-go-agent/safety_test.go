package main

import (
	"context"
	"os"
	"path/filepath"
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
	events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: provider.decision}
	events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "stop"}
	close(events)
	errs := make(chan error)
	close(errs)
	return events, errs
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

func TestSafetyClassifiesBashWithHardcodedLunaLow(t *testing.T) {
	provider := &safetyTestProvider{decision: `{"allowed":false,"reason":"deletes workspace files","authorization":"none","effectScopes":["delete ./build"]}`}
	gate := newSafetyGate(provider)
	request := agent.GuardRequest{Tool: "bash", WorkingDirectory: t.TempDir(), Arguments: map[string]any{"command": "rm -rf build"}, Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "clean it"}}}}}
	decision := gate.Check(context.Background(), request)
	if decision.Allowed || decision.Reason != "deletes workspace files" {
		t.Fatalf("decision=%#v", decision)
	}
	if len(provider.requests) != 1 || provider.requests[0].Model != safetyModel || provider.requests[0].Thinking != safetyThinking || len(provider.requests[0].Tools) != 0 {
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
