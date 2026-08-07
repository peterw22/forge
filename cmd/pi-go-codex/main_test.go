package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInputContentIncludesUserImage(t *testing.T) {
	content := json.RawMessage(`[{"type":"text","text":"inspect"},{"type":"image","mimeType":"image/png","data":"aGVsbG8="}]`)
	result, err := inputTextContent(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("content = %#v", result)
	}
	image := result[1].(map[string]string)
	if image["type"] != "input_image" || image["image_url"] != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("image = %#v", image)
	}
}

func TestCompactionSummaryMapsToUserContext(t *testing.T) {
	messages := []BridgeMessage{{Role: "compactionSummary", Content: json.RawMessage(`[{"type":"text","text":"## Goal\\ncontinue"}]`)}}
	input, err := convertBridgeMessages(messages)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	if !strings.Contains(string(encoded), "conversation history before this point") || !strings.Contains(string(encoded), "## Goal") {
		t.Fatalf("input = %s", encoded)
	}
}

func testToken(t *testing.T) string {
	t.Helper()
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"account-123"}}`))
	return "header." + payload + ".signature"
}

func TestRunStreamsCodexTextAndUsesPiCompatibleHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken(t) {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "account-123" {
			t.Fatalf("ChatGPT-Account-ID = %q", got)
		}
		if got := r.Header.Get("OpenAI-Beta"); got != "responses=experimental" {
			t.Fatalf("OpenAI-Beta = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"model":"gpt-5.5"`) || !strings.Contains(string(body), `"stream":true`) {
			t.Fatalf("unexpected body: %s", body)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello \"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"world\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()

	var output bytes.Buffer
	err := run(context.Background(), &output, Config{
		Endpoint:  server.URL,
		Model:     "gpt-5.5",
		Prompt:    "Say hello",
		Token:     testToken(t),
		Transport: "sse",
		Timeout:   time.Second,
		Client:    server.Client(),
		SessionID: "session-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "hello world" {
		t.Fatalf("output = %q", got)
	}
}

func TestAccountIDFromJWTRejectsTokenWithoutAccount(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	if _, err := accountIDFromJWT("header." + payload + ".signature"); err == nil {
		t.Fatal("accountIDFromJWT succeeded for token without an account")
	}
}

func TestRunProviderEmitsPayloadAndPiStreamEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"type":"function"`) {
			t.Fatalf("Codex request omitted tool definition: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.content_part.added\",\"part\":{\"type\":\"output_text\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
	}))
	defer server.Close()

	request := `{"model":"gpt-5.5","systemPrompt":"Be concise.","messages":[{"role":"user","content":"Say ok"}],"tools":[{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{}}}]}`
	// The bridge pauses after it emits the generated provider payload. A client
	// can replace it through Pi's onPayload hook; this test accepts it unchanged.
	input := strings.NewReader(request + "\n{\"type\":\"payload\",\"payload\":null}\n")
	var output bytes.Buffer
	if err := runProvider(context.Background(), input, &output, Config{
		Endpoint:  server.URL,
		Token:     testToken(t),
		Transport: "sse",
		Timeout:   time.Second,
		Client:    server.Client(),
	}); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) < 6 {
		t.Fatalf("expected payload plus provider events, got %d: %s", len(lines), output.String())
	}
	if !strings.Contains(lines[0], `"type":"payload"`) {
		t.Fatalf("first event = %s", lines[0])
	}
	if !strings.Contains(output.String(), `"type":"start"`) || !strings.Contains(output.String(), `"transport":"SSE"`) {
		t.Fatalf("missing SSE transport event: %s", output.String())
	}
	if !strings.Contains(output.String(), `"type":"text_delta"`) {
		t.Fatalf("missing text delta: %s", output.String())
	}
	if !strings.Contains(output.String(), `"type":"done"`) || !strings.Contains(output.String(), `"responseId":"resp_1"`) {
		t.Fatalf("missing completed event: %s", output.String())
	}
}
