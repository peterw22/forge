package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

func TestQwenConfigurationCoexistsWithLegacyCodexAndFetchesModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/models" || request.Header.Get("Authorization") != "Bearer qwen-secret" {
			t.Fatalf("unexpected model request: %s auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": []any{map[string]string{"id": "qwen-coder-plus"}, map[string]string{"id": "qwen-coder"}}})
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "auth.json")
	auth := newCodexAuthManagerAt(path, defaultCodexAuthEndpoints, server.Client())
	auth.mu.Lock()
	if err := auth.saveLocked(codexCredential{AccessToken: testJWT("account"), RefreshToken: "refresh", AccountID: "account"}); err != nil {
		t.Fatal(err)
	}
	auth.mu.Unlock()
	config, err := auth.SetQwenConfig(qwenConfig{APIKey: "qwen-secret", Protocol: "openai", OpenAIBaseURL: server.URL + "/v1", AnthropicBaseURL: server.URL + "/anthropic"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !config.APIKeyConfigured {
		t.Fatal("Qwen key was not reported as configured")
	}
	status, err := auth.Status()
	if err != nil || !status.Authenticated || status.AccountID != "account" {
		t.Fatalf("OpenAI status=%+v err=%v", status, err)
	}
	models, err := auth.FetchQwenModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "qwen-code-plan/qwen-coder" {
		t.Fatalf("models=%+v", models)
	}
}

func TestQwenProviderStreamsTextToolsAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer key" {
			t.Fatalf("unexpected request")
		}
		var body qwenChatRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "qwen-coder" || len(body.Messages) < 2 || len(body.Tools) != 1 {
			t.Fatalf("body=%+v", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Checking\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"pa\"}}]},\"finish_reason\":null}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"th\\\":\\\"README.md\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	provider := newQwenProvider(func() (qwenConfig, error) {
		return qwenConfig{APIKey: "key", Protocol: "openai", OpenAIBaseURL: server.URL + "/v1"}, nil
	})
	provider.client = server.Client()
	events := collectProviderEvents(t, provider, agent.Request{Model: "qwen-coder", SystemPrompt: "system", Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "hello"}}}}, Tools: []agent.Tool{{Name: "read", Parameters: map[string]any{"type": "object"}}}})
	var text string
	var call agent.ContentBlock
	var done agent.ProviderEvent
	for _, event := range events {
		switch event.Type {
		case agent.ProviderTextDelta:
			text += event.Delta
		case agent.ProviderToolCall:
			call = event.ToolCall
		case agent.ProviderDone:
			done = event
		}
	}
	if text != "Checking" || call.Name != "read" || call.Arguments["path"] != "README.md" || done.StopReason != "toolUse" || done.Usage.TotalTokens != 6 {
		t.Fatalf("events=%+v", events)
	}
}

func TestQwenSSERejectsInvalidToolArguments(t *testing.T) {
	input := bytes.NewBufferString("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"id\",\"function\":{\"name\":\"read\",\"arguments\":\"{\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	events := make(chan agent.ProviderEvent, 8)
	if err := readQwenSSE(input, events); err == nil {
		t.Fatal("expected invalid arguments error")
	}
}

func TestProviderRouterRoutesNamedAPIModel(t *testing.T) {
	var got string
	provider := qwenTestProviderFunc(func(_ context.Context, request agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
		got = request.Model
		events := make(chan agent.ProviderEvent, 1)
		errs := make(chan error)
		events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "stop"}
		close(events)
		close(errs)
		return events, errs
	})
	router := &providerRouter{codex: provider, api: provider}
	collectProviderEvents(t, router, agent.Request{Model: "qwen-code-plan/qwen-coder"})
	if got != "qwen-code-plan/qwen-coder" {
		t.Fatalf("model=%q", got)
	}
}

type qwenTestProviderFunc func(context.Context, agent.Request) (<-chan agent.ProviderEvent, <-chan error)

func (provider qwenTestProviderFunc) Stream(ctx context.Context, request agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
	return provider(ctx, request)
}

func TestMultipleNamedAPIProvidersPersistAndModelsUseProviderPrefix(t *testing.T) {
	auth := newCodexAuthManagerAt(filepath.Join(t.TempDir(), "auth.json"), defaultCodexAuthEndpoints, http.DefaultClient)
	for _, item := range []struct {
		name, model string
	}{
		{"Team-Qwen", "qwen-coder"},
		{"Local_Lab", "deepseek-coder"},
	} {
		_, err := auth.SetAPIConfig(item.name, apiConfig{
			APIKey: "secret-" + item.name, Protocol: "openai",
			OpenAIBaseURL: "https://example.com/v1", AnthropicBaseURL: "https://example.com",
			Models: []string{item.model},
		}, false, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	configs, err := auth.APIConfigs()
	if err != nil || len(configs) != 2 {
		t.Fatalf("configs=%#v err=%v", configs, err)
	}
	t.Setenv("PI_GO_AGY_HOME", t.TempDir())
	models, err := configuredModels(auth)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, model := range models {
		ids[model.ID] = true
	}
	for _, want := range []string{"Team-Qwen/qwen-coder", "Local_Lab/deepseek-coder"} {
		if !ids[want] {
			t.Fatalf("missing model %q in %#v", want, models)
		}
	}
	if err := auth.DeleteAPIConfig("Team-Qwen"); err != nil {
		t.Fatal(err)
	}
	configs, err = auth.APIConfigs()
	if err != nil || len(configs) != 1 || configs[0].Name != "Local_Lab" {
		t.Fatalf("after delete configs=%#v err=%v", configs, err)
	}
}

func TestAPIProviderRouterLoadsConfigurationByModelPrefix(t *testing.T) {
	requested := ""
	provider := newAPIProvider(func(name string) (apiConfig, error) {
		requested = name
		return apiConfig{APIKey: "key", Protocol: "openai", OpenAIBaseURL: "http://localhost:1/v1"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, errs := provider.Stream(ctx, agent.Request{Model: "Team-Qwen/qwen-coder"})
	for range errs {
	}
	if requested != "Team-Qwen" {
		t.Fatalf("requested provider=%q", requested)
	}
}
