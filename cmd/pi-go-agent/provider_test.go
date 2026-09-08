package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/peterw22/pi-go/internal/agent"
)

func testCodexToken(accountID string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `"}}`))
	return "header." + payload + ".signature"
}

func TestCodexFastVariantUsesPriorityServiceTier(t *testing.T) {
	model, tier := resolveCodexFastVariant("gpt-5.6-terra-fast")
	if model != "gpt-5.6-terra" || tier != "priority" {
		t.Fatalf("model=%q tier=%q", model, tier)
	}
	body, err := buildCodexRequest(agent.Request{Model: model, ServiceTier: tier})
	if err != nil {
		t.Fatal(err)
	}
	if body.Model != "gpt-5.6-terra" || body.ServiceTier != "priority" {
		t.Fatalf("body=%#v", body)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"service_tier":"priority"`)) || bytes.Contains(encoded, []byte(`gpt-5.6-terra-fast`)) {
		t.Fatalf("encoded=%s", encoded)
	}
	model, tier = resolveCodexFastVariant("gpt-5.6-terra")
	if model != "gpt-5.6-terra" || tier != "" {
		t.Fatalf("normal model=%q tier=%q", model, tier)
	}
}

func TestCodexAstraModelPassesThroughExistingRequestInfra(t *testing.T) {
	model, tier := resolveCodexFastVariant("gpt-6-astra")
	if model != "gpt-6-astra" || tier != "" {
		t.Fatalf("model=%q tier=%q", model, tier)
	}
	body, err := buildCodexRequest(agent.Request{Model: model, Thinking: "max"})
	if err != nil {
		t.Fatal(err)
	}
	if body.Model != "gpt-6-astra" {
		t.Fatalf("model was rewritten: %#v", body)
	}
	reasoning, ok := body.Reasoning.(map[string]string)
	if !ok || reasoning["effort"] != "max" {
		t.Fatalf("reasoning=%#v", body.Reasoning)
	}

	fastModel, fastTier := resolveCodexFastVariant("gpt-6-astra-fast")
	if fastModel != "gpt-6-astra" || fastTier != "priority" {
		t.Fatalf("fast model=%q tier=%q", fastModel, fastTier)
	}
}

func TestConfiguredModelsIncludeCodexFastVariants(t *testing.T) {
	auth := newCodexAuthManagerAt(filepath.Join(t.TempDir(), "auth.json"), defaultCodexAuthEndpoints, http.DefaultClient)
	models, err := configuredModels(auth)
	if err != nil {
		t.Fatal(err)
	}
	foundTerraFast := false
	foundAstra := false
	foundAstraFast := false
	for _, model := range models {
		switch model.ID {
		case "gpt-5.6-terra-fast":
			foundTerraFast = model.Provider == codexProviderID && strings.Contains(model.Label, "Fast")
		case "gpt-6-astra":
			foundAstra = model.Provider == codexProviderID
		case "gpt-6-astra-fast":
			foundAstraFast = model.Provider == codexProviderID && strings.Contains(model.Label, "Fast")
		}
	}
	if !foundTerraFast || !foundAstra || !foundAstraFast {
		t.Fatalf("built-in Codex variants missing: %#v", models)
	}
}

func TestCachedRequestBodySendsOnlyDelta(t *testing.T) {
	provider := newCodexProvider(defaultCodexEndpoint, func(string) (string, error) { return testCodexToken("account"), nil })
	session := &codexSessionConnection{provider: provider, id: "session"}
	first := codexRequestBody{Model: "gpt", Store: false, Stream: true, Instructions: "test", Input: []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": "hello"}}},
	}}
	responseItem := map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "hi"}}}
	session.continuation = &codexContinuation{fingerprint: requestFingerprint(first), lastInput: cloneJSONSlice(first.Input), responseID: "resp_1", responseItems: []any{responseItem}}
	second := first
	second.Input = append(cloneJSONSlice(first.Input), responseItem, map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": "next"}}})
	delta := session.cachedRequestBodyLocked(second)
	if delta.PreviousResponseID != "resp_1" || len(delta.Input) != 1 {
		t.Fatalf("delta = %#v", delta)
	}
	encoded, _ := json.Marshal(delta.Input[0])
	if string(encoded) != `{"content":[{"text":"next","type":"input_text"}],"role":"user","type":"message"}` {
		t.Fatalf("input = %s", encoded)
	}
}

func TestBuildCodexRequestIncludesSessionAndToolResult(t *testing.T) {
	request, err := buildCodexRequest(agent.Request{
		Model: "gpt", SessionID: "session-1", SystemPrompt: "test",
		Messages: []agent.Message{
			{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "hello"}}},
			{Role: agent.RoleToolResult, ToolCallID: "call_1", Content: []agent.ContentBlock{{Type: "text", Text: "done"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.PromptCacheKey != "session-1" || len(request.Input) != 2 {
		t.Fatalf("request = %#v", request)
	}
}

func TestCodexReasoningSummaryPartsHaveParagraphBreaks(t *testing.T) {
	events := make(chan agent.ProviderEvent, 8)
	consumer := newCodexEventConsumer(events)
	for _, event := range []codexResponseEvent{
		{Type: "response.reasoning_summary_part.added"},
		{Type: "response.reasoning_summary_text.delta", Delta: "First thought"},
		{Type: "response.reasoning_summary_part.added"},
		{Type: "response.reasoning_summary_text.delta", Delta: "Second thought"},
	} {
		if err := consumer.consume(event); err != nil {
			t.Fatal(err)
		}
	}
	close(events)
	var thinking string
	for event := range events {
		if event.Type == agent.ProviderThinkingDelta {
			thinking += event.Delta
		}
	}
	if thinking != "First thought\n\nSecond thought" {
		t.Fatalf("thinking = %q", thinking)
	}
}

func TestCodexProviderReusesSessionWebSocketAndSendsDelta(t *testing.T) {
	var connections atomic.Int32
	bodies := make(chan codexRequestBody, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connections.Add(1)
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Errorf("accept WebSocket: %v", err)
			return
		}
		defer connection.CloseNow()
		for turn := 1; turn <= 2; turn++ {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, payload, readErr := connection.Read(ctx)
			cancel()
			if readErr != nil {
				t.Errorf("read turn %d: %v", turn, readErr)
				return
			}
			var body codexRequestBody
			if err := json.Unmarshal(payload, &body); err != nil {
				t.Errorf("decode turn %d: %v", turn, err)
				return
			}
			bodies <- body
			responseID := "resp_1"
			text := "hi"
			if turn == 2 {
				responseID = "resp_2"
				text = "done"
			}
			writeCodexTestEvents(t, connection,
				map[string]any{"type": "response.output_text.delta", "delta": text},
				map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "msg", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": text}}}},
				map[string]any{"type": "response.completed", "response": map[string]any{"id": responseID, "status": "completed", "usage": map[string]int{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}}},
			)
		}
	}))
	defer server.Close()

	provider := newCodexProvider(server.URL, func(string) (string, error) { return testCodexToken("account"), nil })
	provider.client = server.Client()
	provider.timeout = 3 * time.Second
	defer provider.Close()
	firstMessages := []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "hello"}}}}
	firstEvents := collectProviderEvents(t, provider, agent.Request{Model: "gpt", SessionID: "session", SystemPrompt: "test", Messages: firstMessages})
	if len(firstEvents) < 3 || firstEvents[0].Type != agent.ProviderTransport || firstEvents[0].Transport != "WS" {
		t.Fatalf("first events = %#v", firstEvents)
	}
	secondMessages := append(append([]agent.Message(nil), firstMessages...),
		agent.Message{Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Type: "text", Text: "hi"}}},
		agent.Message{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "next"}}},
	)
	collectProviderEvents(t, provider, agent.Request{Model: "gpt", SessionID: "session", SystemPrompt: "test", Messages: secondMessages})
	firstBody, secondBody := <-bodies, <-bodies
	if firstBody.PreviousResponseID != "" || len(firstBody.Input) != 1 {
		t.Fatalf("first body = %#v", firstBody)
	}
	if secondBody.PreviousResponseID != "resp_1" || len(secondBody.Input) != 1 {
		t.Fatalf("second body = %#v", secondBody)
	}
	if connections.Load() != 1 {
		t.Fatalf("connections = %d, want 1", connections.Load())
	}
}

func collectProviderEvents(t *testing.T, provider agent.Provider, request agent.Request) []agent.ProviderEvent {
	t.Helper()
	events, errs := provider.Stream(context.Background(), request)
	var result []agent.ProviderEvent
	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			result = append(result, event)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return result
}

func writeCodexTestEvents(t *testing.T, connection *websocket.Conn, events ...map[string]any) {
	t.Helper()
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err = connection.Write(ctx, websocket.MessageText, payload)
		cancel()
		if err != nil {
			t.Errorf("write event: %v", err)
			return
		}
	}
}
