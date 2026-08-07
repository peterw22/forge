package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRunStreamsCodexWebSocket(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
		if got := httpRequest.Header.Get("Authorization"); got != "Bearer "+testToken(t) {
			t.Errorf("Authorization = %q", got)
		}
		if got := httpRequest.Header.Get("ChatGPT-Account-ID"); got != "account-123" {
			t.Errorf("ChatGPT-Account-ID = %q", got)
		}
		if got := httpRequest.Header.Get("OpenAI-Beta"); got != codexWebSocketBeta {
			t.Errorf("OpenAI-Beta = %q", got)
		}
		if got := httpRequest.Header.Get("Session-ID"); got != "session-123" {
			t.Errorf("Session-ID = %q", got)
		}
		connection, err := websocket.Accept(writer, httpRequest, nil)
		if err != nil {
			t.Errorf("accept WebSocket: %v", err)
			return
		}
		defer connection.CloseNow()
		_, payload, err := connection.Read(httpRequest.Context())
		if err != nil {
			t.Errorf("read WebSocket request: %v", err)
			return
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			t.Errorf("decode WebSocket request: %v", err)
			return
		}
		writeWebSocketEvents(t, httpRequest.Context(), connection,
			map[string]any{"type": "response.output_text.delta", "delta": "hello "},
			map[string]any{"type": "response.output_text.delta", "delta": "world"},
			map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed"}},
		)
	}))
	defer server.Close()

	var output bytes.Buffer
	err := run(context.Background(), &output, Config{
		Endpoint:  server.URL,
		Model:     "gpt-5.5",
		Prompt:    "Say hello",
		Token:     testToken(t),
		Transport: "websocket",
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
	if request["type"] != "response.create" || request["model"] != "gpt-5.5" || request["stream"] != true {
		t.Fatalf("request = %#v", request)
	}
}

func TestRunProviderStreamsWebSocketBridgeEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Errorf("accept WebSocket: %v", err)
			return
		}
		defer connection.CloseNow()
		_, payload, err := connection.Read(request.Context())
		if err != nil {
			t.Errorf("read WebSocket request: %v", err)
			return
		}
		if !strings.Contains(string(payload), `"type":"function"`) || !strings.Contains(string(payload), `"type":"response.create"`) {
			t.Errorf("unexpected WebSocket request: %s", payload)
			return
		}
		writeWebSocketEvents(t, request.Context(), connection,
			map[string]any{"type": "response.content_part.added", "part": map[string]any{"type": "output_text"}},
			map[string]any{"type": "response.output_text.delta", "delta": "ok"},
			map[string]any{"type": "response.completed", "response": map[string]any{
				"id": "resp_1", "status": "completed",
				"usage": map[string]any{"input_tokens": 3, "output_tokens": 2, "total_tokens": 5},
			}},
		)
	}))
	defer server.Close()

	request := `{"model":"gpt-5.5","transport":"websocket","systemPrompt":"Be concise.","messages":[{"role":"user","content":"Say ok"}],"tools":[{"name":"echo","description":"Echo text","parameters":{"type":"object","properties":{}}}]}`
	input := strings.NewReader(request + "\n{\"type\":\"payload\",\"payload\":null}\n")
	var output bytes.Buffer
	if err := runProvider(context.Background(), input, &output, Config{
		Endpoint: server.URL,
		Token:    testToken(t),
		Timeout:  time.Second,
		Client:   server.Client(),
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"type":"start"`) || !strings.Contains(output.String(), `"transport":"WS"`) || !strings.Contains(output.String(), `"type":"text_delta"`) {
		t.Fatalf("missing WebSocket stream events: %s", output.String())
	}
	if !strings.Contains(output.String(), `"type":"done"`) || !strings.Contains(output.String(), `"responseId":"resp_1"`) {
		t.Fatalf("missing terminal event: %s", output.String())
	}
}

func TestAutoFallsBackToSSEBeforeWebSocketEvents(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("Upgrade") != "" {
			http.Error(writer, "WebSocket unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"fallback\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	}))
	defer server.Close()

	var output bytes.Buffer
	err := run(context.Background(), &output, Config{
		Endpoint: server.URL, Model: "gpt-5.5", Prompt: "hello", Token: testToken(t),
		Transport: "auto", Timeout: time.Second, Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || output.String() != "fallback" {
		t.Fatalf("requests = %d, output = %q", requests, output.String())
	}
}

func TestAutoDoesNotFallBackOnCodexAPIErrorBeforeStreamStart(t *testing.T) {
	httpRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Upgrade") == "" {
			httpRequests++
			http.Error(writer, "unexpected SSE fallback", http.StatusInternalServerError)
			return
		}
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Errorf("accept WebSocket: %v", err)
			return
		}
		defer connection.CloseNow()
		if _, _, err := connection.Read(request.Context()); err != nil {
			t.Errorf("read WebSocket request: %v", err)
			return
		}
		writeWebSocketEvents(t, request.Context(), connection,
			map[string]any{"type": "error", "code": "invalid_request", "message": "bad request"},
		)
	}))
	defer server.Close()

	var output bytes.Buffer
	err := run(context.Background(), &output, Config{
		Endpoint: server.URL, Model: "gpt-5.5", Prompt: "hello", Token: testToken(t),
		Transport: "auto", Timeout: time.Second, Client: server.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "bad request") || !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("error = %v", err)
	}
	if httpRequests != 0 {
		t.Fatalf("HTTP requests = %d", httpRequests)
	}
}

func TestWebSocketDoesNotFallBackAfterStreamingStarts(t *testing.T) {
	httpRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Upgrade") == "" {
			httpRequests++
			http.Error(writer, "unexpected SSE fallback", http.StatusInternalServerError)
			return
		}
		connection, err := websocket.Accept(writer, request, nil)
		if err != nil {
			t.Errorf("accept WebSocket: %v", err)
			return
		}
		defer connection.CloseNow()
		if _, _, err := connection.Read(request.Context()); err != nil {
			t.Errorf("read WebSocket request: %v", err)
			return
		}
		writeWebSocketEvents(t, request.Context(), connection,
			map[string]any{"type": "response.output_text.delta", "delta": "partial"},
		)
	}))
	defer server.Close()

	var output bytes.Buffer
	err := run(context.Background(), &output, Config{
		Endpoint: server.URL, Model: "gpt-5.5", Prompt: "hello", Token: testToken(t),
		Transport: "auto", Timeout: time.Second, Client: server.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "WebSocket") {
		t.Fatalf("error = %v", err)
	}
	if httpRequests != 0 || output.String() != "partial" {
		t.Fatalf("HTTP requests = %d, output = %q", httpRequests, output.String())
	}
}

func writeWebSocketEvents(t *testing.T, ctx context.Context, connection *websocket.Conn, events ...map[string]any) {
	t.Helper()
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			t.Errorf("encode WebSocket event: %v", err)
			return
		}
		if err := connection.Write(ctx, websocket.MessageText, payload); err != nil {
			t.Errorf("write WebSocket event: %v", err)
			return
		}
	}
}
