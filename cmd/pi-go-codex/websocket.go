package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const (
	codexWebSocketBeta = "responses_websockets=2026-02-06"
	maxCodexWSMessage  = 32 << 20
)

type streamCallbacks struct {
	response func(status int, headers http.Header) error
	start    func(transport string) error
}

type codexAPIError struct {
	message string
	code    string
}

func (err *codexAPIError) Error() string {
	if err.code == "" {
		return err.message
	}
	return fmt.Sprintf("%s (%s)", err.message, err.code)
}

func streamCodexEvents(
	ctx context.Context,
	body any,
	config Config,
	callbacks streamCallbacks,
	handle func(sseEvent) (bool, error),
) error {
	transport := strings.ToLower(strings.TrimSpace(config.Transport))
	if transport == "" {
		transport = "auto"
	}
	switch transport {
	case "sse":
		return streamCodexSSE(ctx, body, config, callbacks, handle)
	case "websocket", "websocket-cached", "auto":
		started, err := streamCodexWebSocket(ctx, body, config, callbacks, handle)
		if err == nil || transport != "auto" || started {
			return err
		}
		var apiErr *codexAPIError
		if errors.As(err, &apiErr) || ctx.Err() != nil {
			return err
		}
		return streamCodexSSE(ctx, body, config, callbacks, handle)
	default:
		return fmt.Errorf("unsupported Codex transport %q", config.Transport)
	}
}

func streamCodexSSE(
	ctx context.Context,
	body any,
	config Config,
	callbacks streamCallbacks,
	handle func(sseEvent) (bool, error),
) error {
	response, err := sendCodexRequest(ctx, body, config)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if callbacks.response != nil {
		if err := callbacks.response(response.StatusCode, response.Header); err != nil {
			return err
		}
	}
	if callbacks.start != nil {
		if err := callbacks.start("SSE"); err != nil {
			return err
		}
	}
	return readEvents(response.Body, handle)
}

func streamCodexWebSocket(
	ctx context.Context,
	body any,
	config Config,
	callbacks streamCallbacks,
	handle func(sseEvent) (bool, error),
) (bool, error) {
	if strings.TrimSpace(config.Token) == "" {
		return false, errors.New("PI_GO_CODEX_TOKEN is not set")
	}
	accountID, err := accountIDFromJWT(config.Token)
	if err != nil {
		return false, err
	}
	endpoint, err := codexWebSocketURL(config.Endpoint)
	if err != nil {
		return false, err
	}
	requestID := config.SessionID
	if requestID == "" {
		requestID, err = newRequestID()
		if err != nil {
			return false, err
		}
	}
	headers := buildCodexHeaders(config, accountID)
	headers.Del("Accept")
	headers.Del("Content-Type")
	headers.Del("OpenAI-Beta")
	headers.Set("OpenAI-Beta", codexWebSocketBeta)
	headers.Set("Session-ID", requestID)
	headers.Set("X-Client-Request-ID", requestID)

	dialCtx, cancelDial := withOptionalTimeout(ctx, config.Timeout)
	connection, response, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{
		HTTPClient: config.Client,
		HTTPHeader: headers,
	})
	cancelDial()
	if err != nil {
		return false, webSocketDialError(err, response)
	}
	connection.SetReadLimit(maxCodexWSMessage)
	defer connection.CloseNow()

	message, err := responseCreateMessage(body)
	if err != nil {
		return false, err
	}
	writeCtx, cancelWrite := withOptionalTimeout(ctx, config.Timeout)
	err = connection.Write(writeCtx, websocket.MessageText, message)
	cancelWrite()
	if err != nil {
		return false, fmt.Errorf("send Codex WebSocket request: %w", err)
	}

	started := false
	for {
		readCtx, cancelRead := withOptionalTimeout(ctx, config.Timeout)
		messageType, data, readErr := connection.Read(readCtx)
		cancelRead()
		if readErr != nil {
			if ctx.Err() != nil {
				return started, ctx.Err()
			}
			return started, fmt.Errorf("read Codex WebSocket stream: %w", readErr)
		}
		if messageType != websocket.MessageText {
			return started, errors.New("Codex WebSocket returned a non-text message")
		}
		var event sseEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return started, fmt.Errorf("decode Codex WebSocket event: %w", err)
		}
		if event.Type == "" {
			continue
		}
		if apiErr := codexEventError(event); apiErr != nil {
			return started, apiErr
		}
		if !started {
			started = true
			if callbacks.start != nil {
				if err := callbacks.start("WS"); err != nil {
					return true, err
				}
			}
		}
		normalizeWebSocketTerminalEvent(&event)
		done, err := handle(event)
		if err != nil {
			return true, err
		}
		if done {
			_ = connection.Close(websocket.StatusNormalClosure, "done")
			return true, nil
		}
	}
}

func buildCodexHeaders(config Config, accountID string) http.Header {
	headers := make(http.Header, len(config.Headers)+6)
	for name, value := range config.Headers {
		if value != nil {
			headers.Set(name, *value)
		}
	}
	headers.Set("Authorization", "Bearer "+config.Token)
	headers.Set("ChatGPT-Account-ID", accountID)
	headers.Set("Originator", "pi")
	headers.Set("User-Agent", "pi-go-codex-bridge/0.1")
	return headers
}

func codexWebSocketURL(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse Codex endpoint: %w", err)
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("unsupported Codex endpoint scheme %q", parsed.Scheme)
	}
	return parsed.String(), nil
}

func responseCreateMessage(body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode Codex WebSocket request: %w", err)
	}
	var request map[string]any
	if err := json.Unmarshal(encoded, &request); err != nil {
		return nil, errors.New("Codex WebSocket payload must be a JSON object")
	}
	request["type"] = "response.create"
	encoded, err = json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Codex WebSocket request: %w", err)
	}
	return encoded, nil
}

func codexEventError(event sseEvent) error {
	if event.Type == "error" {
		message, code := event.Message, event.Code
		if event.Error != nil {
			if event.Error.Message != "" {
				message = event.Error.Message
			}
			if event.Error.Code != "" {
				code = event.Error.Code
			}
		}
		if message == "" {
			message = "Codex WebSocket returned an error"
		}
		return &codexAPIError{message: message, code: code}
	}
	if event.Type == "response.failed" {
		message, code := "Codex response failed", ""
		if event.Response != nil && event.Response.Error != nil {
			if event.Response.Error.Message != "" {
				message = event.Response.Error.Message
			}
			code = event.Response.Error.Code
		}
		return &codexAPIError{message: message, code: code}
	}
	return nil
}

func normalizeWebSocketTerminalEvent(event *sseEvent) {
	if event.Type != "response.done" {
		return
	}
	if event.Response != nil && event.Response.Status == "incomplete" {
		event.Type = "response.incomplete"
	} else {
		event.Type = "response.completed"
	}
}

func webSocketDialError(err error, response *http.Response) error {
	if response == nil {
		return fmt.Errorf("connect to Codex WebSocket: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	message := strings.TrimSpace(string(body))
	if message == "" {
		return fmt.Errorf("connect to Codex WebSocket: HTTP %d: %w", response.StatusCode, err)
	}
	return fmt.Errorf("connect to Codex WebSocket: HTTP %d: %s: %w", response.StatusCode, message, err)
}

func withOptionalTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate Codex request ID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
