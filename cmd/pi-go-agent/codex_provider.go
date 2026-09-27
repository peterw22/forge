package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/peterw22/forge/internal/agent"
)

const (
	defaultCodexEndpoint      = "https://chatgpt.com/backend-api/codex/responses"
	codexWebSocketBeta        = "responses_websockets=2026-02-06"
	codexSSEBeta              = "responses=experimental"
	maxCodexWebSocketMessage  = 32 << 20
	codexSessionIdleTimeout   = 5 * time.Minute
	codexConnectionMaxAge     = 55 * time.Minute
	defaultCodexStreamTimeout = 90 * time.Second
)

type codexTokenSource func(model string) (string, error)

type codexProvider struct {
	endpoint    string
	tokenSource codexTokenSource
	client      *http.Client
	timeout     time.Duration

	mu       sync.Mutex
	sessions map[string]*codexSessionConnection
	closed   bool
}

type codexSessionConnection struct {
	provider     *codexProvider
	id           string
	mu           sync.Mutex
	connection   *websocket.Conn
	createdAt    time.Time
	idleTimer    *time.Timer
	continuation *codexContinuation
}

type codexContinuation struct {
	fingerprint   string
	lastInput     []any
	responseID    string
	responseItems []any
}

type codexRequestBody struct {
	Type               string   `json:"type,omitempty"`
	Model              string   `json:"model"`
	Store              bool     `json:"store"`
	Stream             bool     `json:"stream"`
	Instructions       string   `json:"instructions"`
	Input              []any    `json:"input"`
	Tools              []any    `json:"tools,omitempty"`
	ToolChoice         string   `json:"tool_choice,omitempty"`
	ParallelTools      bool     `json:"parallel_tool_calls,omitempty"`
	Reasoning          any      `json:"reasoning,omitempty"`
	Text               any      `json:"text"`
	Include            []string `json:"include,omitempty"`
	PromptCacheKey     string   `json:"prompt_cache_key,omitempty"`
	ServiceTier        string   `json:"service_tier,omitempty"`
	PreviousResponseID string   `json:"previous_response_id,omitempty"`
}

type codexResponseEvent struct {
	Type        string          `json:"type"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	Delta       string          `json:"delta"`
	ItemID      string          `json:"item_id"`
	Item        json.RawMessage `json:"item"`
	Part        json.RawMessage `json:"part"`
	OutputIndex int             `json:"output_index"`
	Error       *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Response *struct {
		ID     string      `json:"id"`
		Status string      `json:"status"`
		Usage  *codexUsage `json:"usage"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	InputDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
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

type codexStreamResult struct {
	responseID    string
	responseItems []any
}

type codexEventConsumer struct {
	events                  chan<- agent.ProviderEvent
	toolOrder               []string
	tools                   map[string]*codexToolCall
	responseItems           []any
	text                    strings.Builder
	thinking                strings.Builder
	reasoningPartNeedsBreak bool
	responseID              string
	usage                   agent.Usage
	stopReason              string
	started                 bool
	done                    bool
}

type codexToolCall struct {
	itemID    string
	callID    string
	name      string
	arguments strings.Builder
	emitted   bool
}

func newCodexProvider(endpoint string, tokenSource codexTokenSource) *codexProvider {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = defaultCodexEndpoint
	}
	return &codexProvider{
		endpoint: endpoint, tokenSource: tokenSource, timeout: defaultCodexStreamTimeout,
		client: &http.Client{}, sessions: make(map[string]*codexSessionConnection),
	}
}

func (provider *codexProvider) Stream(ctx context.Context, request agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
	events := make(chan agent.ProviderEvent, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		if err := provider.stream(ctx, request, events); err != nil {
			errs <- err
		}
	}()
	return events, errs
}

func resolveCodexFastVariant(model string) (string, string) {
	const suffix = "-fast"
	if strings.HasSuffix(model, suffix) && len(model) > len(suffix) {
		return strings.TrimSuffix(model, suffix), "priority"
	}
	return model, ""
}

func (provider *codexProvider) stream(ctx context.Context, request agent.Request, events chan<- agent.ProviderEvent) error {
	request.Model, request.ServiceTier = resolveCodexFastVariant(request.Model)
	fullBody, err := buildCodexRequest(request)
	if err != nil {
		return err
	}
	if request.SessionID == "" {
		session := &codexSessionConnection{provider: provider}
		session.mu.Lock()
		defer session.mu.Unlock()
		defer session.closeLocked()
		return session.streamLocked(ctx, fullBody, events, false)
	}

	session, err := provider.acquireSession(request.SessionID)
	if err != nil {
		return err
	}
	session.mu.Lock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	err = session.streamLocked(ctx, fullBody, events, true)
	session.mu.Unlock()
	provider.scheduleIdleClose(session)
	return err
}

func (provider *codexProvider) acquireSession(id string) (*codexSessionConnection, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.closed {
		return nil, errors.New("Codex provider is closed")
	}
	if session := provider.sessions[id]; session != nil {
		return session, nil
	}
	session := &codexSessionConnection{provider: provider, id: id}
	provider.sessions[id] = session
	return session, nil
}

func (provider *codexProvider) scheduleIdleClose(session *codexSessionConnection) {
	if session.id == "" {
		return
	}
	session.mu.Lock()
	if session.idleTimer != nil {
		session.idleTimer.Stop()
	}
	session.idleTimer = time.AfterFunc(codexSessionIdleTimeout, func() {
		provider.mu.Lock()
		if provider.sessions[session.id] == session {
			delete(provider.sessions, session.id)
		}
		provider.mu.Unlock()
		session.mu.Lock()
		session.closeLocked()
		session.mu.Unlock()
	})
	session.mu.Unlock()
}

func (provider *codexProvider) CloseSession(id string) {
	provider.mu.Lock()
	session := provider.sessions[id]
	if session != nil {
		delete(provider.sessions, id)
	}
	provider.mu.Unlock()
	if session == nil {
		return
	}
	session.mu.Lock()
	session.closeLocked()
	session.mu.Unlock()
}

func (provider *codexProvider) Close() error {
	provider.mu.Lock()
	provider.closed = true
	sessions := make([]*codexSessionConnection, 0, len(provider.sessions))
	for _, session := range provider.sessions {
		sessions = append(sessions, session)
	}
	provider.sessions = make(map[string]*codexSessionConnection)
	provider.mu.Unlock()
	for _, session := range sessions {
		session.mu.Lock()
		session.closeLocked()
		session.mu.Unlock()
	}
	return nil
}

func (session *codexSessionConnection) streamLocked(ctx context.Context, fullBody codexRequestBody, events chan<- agent.ProviderEvent, cache bool) error {
	for attempt := 0; attempt < 2; attempt++ {
		if err := session.ensureConnectionLocked(ctx, fullBody.Model); err != nil {
			if attempt == 0 {
				session.closeLocked()
				continue
			}
			return session.streamSSELocked(ctx, fullBody, events)
		}
		body := fullBody
		if cache {
			body = session.cachedRequestBodyLocked(fullBody)
		}
		consumer := newCodexEventConsumer(events)
		result, started, err := session.streamWebSocketLocked(ctx, body, consumer)
		if err == nil {
			if cache {
				session.continuation = &codexContinuation{
					fingerprint: requestFingerprint(fullBody), lastInput: cloneJSONSlice(fullBody.Input),
					responseID: result.responseID, responseItems: cloneJSONSlice(result.responseItems),
				}
			}
			return nil
		}
		var apiErr *codexAPIError
		missingContinuation := errors.As(err, &apiErr) && apiErr.code == "previous_response_not_found"
		if missingContinuation && !started && body.PreviousResponseID != "" {
			session.continuation = nil
			session.closeConnectionLocked()
			continue
		}
		if started || errors.As(err, &apiErr) || ctx.Err() != nil {
			session.closeConnectionLocked()
			return err
		}
		session.closeConnectionLocked()
	}
	return session.streamSSELocked(ctx, fullBody, events)
}

func (session *codexSessionConnection) ensureConnectionLocked(ctx context.Context, model string) error {
	if session.connection != nil && time.Since(session.createdAt) < codexConnectionMaxAge {
		return nil
	}
	session.closeConnectionLocked()
	token, err := session.provider.tokenSource(model)
	if err != nil {
		return err
	}
	accountID, err := codexAccountIDFromJWT(token)
	if err != nil {
		return err
	}
	endpoint, err := codexWebSocketURL(session.provider.endpoint)
	if err != nil {
		return err
	}
	requestID := session.id
	if requestID == "" {
		requestID = fmt.Sprintf("pi-go-%d", time.Now().UnixNano())
	}
	headers := buildCodexHeaders(token, accountID)
	headers.Set("OpenAI-Beta", codexWebSocketBeta)
	headers.Set("Session-ID", requestID)
	headers.Set("X-Client-Request-ID", requestID)
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	connection, response, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{HTTPClient: session.provider.client, HTTPHeader: headers})
	cancel()
	if err != nil {
		return codexWebSocketDialError(err, response)
	}
	connection.SetReadLimit(maxCodexWebSocketMessage)
	session.connection = connection
	session.createdAt = time.Now()
	session.continuation = nil
	return nil
}

func (session *codexSessionConnection) cachedRequestBodyLocked(full codexRequestBody) codexRequestBody {
	continuation := session.continuation
	if continuation == nil || continuation.responseID == "" || continuation.fingerprint != requestFingerprint(full) {
		return full
	}
	baseline := make([]any, 0, len(continuation.lastInput)+len(continuation.responseItems))
	baseline = append(baseline, continuation.lastInput...)
	baseline = append(baseline, continuation.responseItems...)
	if len(full.Input) < len(baseline) || !jsonValuesEqual(full.Input[:len(baseline)], baseline) {
		return full
	}
	full.Input = cloneJSONSlice(full.Input[len(baseline):])
	full.PreviousResponseID = continuation.responseID
	return full
}

func (session *codexSessionConnection) streamWebSocketLocked(ctx context.Context, body codexRequestBody, consumer *codexEventConsumer) (codexStreamResult, bool, error) {
	body.Type = "response.create"
	encoded, err := json.Marshal(body)
	if err != nil {
		return codexStreamResult{}, false, fmt.Errorf("encode Codex WebSocket request: %w", err)
	}
	writeCtx, cancelWrite := context.WithTimeout(ctx, session.provider.timeout)
	err = session.connection.Write(writeCtx, websocket.MessageText, encoded)
	cancelWrite()
	if err != nil {
		return codexStreamResult{}, false, fmt.Errorf("send Codex WebSocket request: %w", err)
	}
	for {
		readCtx, cancelRead := context.WithTimeout(ctx, session.provider.timeout)
		messageType, data, readErr := session.connection.Read(readCtx)
		cancelRead()
		if readErr != nil {
			if ctx.Err() != nil {
				return codexStreamResult{}, consumer.started, ctx.Err()
			}
			return codexStreamResult{}, consumer.started, fmt.Errorf("read Codex WebSocket stream: %w", readErr)
		}
		if messageType != websocket.MessageText {
			return codexStreamResult{}, consumer.started, errors.New("Codex WebSocket returned a non-text message")
		}
		var event codexResponseEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return codexStreamResult{}, consumer.started, fmt.Errorf("decode Codex WebSocket event: %w", err)
		}
		if event.Type == "" {
			continue
		}
		if apiErr := codexResponseError(event); apiErr != nil {
			return codexStreamResult{}, consumer.started, apiErr
		}
		if !consumer.started {
			consumer.started = true
			consumer.events <- agent.ProviderEvent{Type: agent.ProviderTransport, Transport: "WS"}
		}
		if err := consumer.consume(event); err != nil {
			return codexStreamResult{}, true, err
		}
		if consumer.done {
			return codexStreamResult{responseID: consumer.responseID, responseItems: consumer.normalizedResponseItems()}, true, nil
		}
	}
}

func (session *codexSessionConnection) streamSSELocked(ctx context.Context, body codexRequestBody, events chan<- agent.ProviderEvent) error {
	session.closeConnectionLocked()
	session.continuation = nil
	token, err := session.provider.tokenSource(body.Model)
	if err != nil {
		return err
	}
	accountID, err := codexAccountIDFromJWT(token)
	if err != nil {
		return err
	}
	body.Type = ""
	body.PreviousResponseID = ""
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode Codex SSE request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, session.provider.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create Codex SSE request: %w", err)
	}
	request.Header = buildCodexHeaders(token, accountID)
	request.Header.Set("OpenAI-Beta", codexSSEBeta)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if session.id != "" {
		request.Header.Set("Session-ID", session.id)
		request.Header.Set("X-Client-Request-ID", session.id)
	}
	response, err := session.provider.client.Do(request)
	if err != nil {
		return fmt.Errorf("send Codex SSE request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("Codex returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	events <- agent.ProviderEvent{Type: agent.ProviderTransport, Transport: "SSE"}
	consumer := newCodexEventConsumer(events)
	consumer.started = true
	return readCodexSSE(response.Body, consumer)
}

func (session *codexSessionConnection) closeConnectionLocked() {
	if session.connection != nil {
		_ = session.connection.Close(websocket.StatusNormalClosure, "done")
		session.connection = nil
	}
	session.createdAt = time.Time{}
	session.continuation = nil
}

func (session *codexSessionConnection) closeLocked() {
	if session.idleTimer != nil {
		session.idleTimer.Stop()
		session.idleTimer = nil
	}
	session.closeConnectionLocked()
}

func buildCodexRequest(request agent.Request) (codexRequestBody, error) {
	input, err := convertCodexMessages(request.Messages)
	if err != nil {
		return codexRequestBody{}, err
	}
	tools, err := convertCodexTools(request.Tools)
	if err != nil {
		return codexRequestBody{}, err
	}
	body := codexRequestBody{
		Model: request.Model, Store: false, Stream: true, Instructions: request.SystemPrompt,
		Input: input, Text: map[string]string{"verbosity": "low"}, Include: []string{"reasoning.encrypted_content"},
		PromptCacheKey: request.SessionID, ServiceTier: request.ServiceTier,
	}
	if len(tools) > 0 {
		body.Tools = tools
		body.ToolChoice = "auto"
		body.ParallelTools = true
	}
	if request.Thinking != "" && request.Thinking != "off" {
		body.Reasoning = map[string]string{"effort": request.Thinking, "summary": "auto"}
	}
	return body, nil
}

func convertCodexMessages(messages []agent.Message) ([]any, error) {
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case agent.RoleUser, agent.RoleCompactionSummary:
			content := make([]any, 0, len(message.Content)+1)
			if message.Role == agent.RoleCompactionSummary {
				content = append(content, map[string]string{"type": "input_text", "text": "The conversation history before this point was compacted into the following summary:\n\n"})
			}
			for _, block := range message.Content {
				switch block.Type {
				case "text":
					content = append(content, map[string]string{"type": "input_text", "text": block.Text})
				case "image":
					if block.Data != "" && block.MIMEType != "" {
						content = append(content, map[string]string{"type": "input_image", "detail": "auto", "image_url": "data:" + block.MIMEType + ";base64," + block.Data})
					}
				}
			}
			input = append(input, map[string]any{"type": "message", "role": "user", "content": content})
		case agent.RoleAssistant:
			for _, block := range message.Content {
				switch block.Type {
				case "text":
					input = append(input, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": block.Text}}})
				case "toolCall":
					arguments, err := json.Marshal(block.Arguments)
					if err != nil {
						return nil, fmt.Errorf("encode tool arguments: %w", err)
					}
					input = append(input, map[string]string{"type": "function_call", "call_id": block.ID, "name": block.Name, "arguments": string(arguments)})
				}
			}
		case agent.RoleToolResult:
			output := make([]any, 0, len(message.Content))
			for _, block := range message.Content {
				switch block.Type {
				case "text":
					output = append(output, map[string]any{"type": "input_text", "text": block.Text})
				case "image":
					if block.Data != "" && block.MIMEType != "" {
						output = append(output, map[string]any{"type": "input_image", "detail": "auto", "image_url": "data:" + block.MIMEType + ";base64," + block.Data})
					}
				}
			}
			var value any = "(no tool output)"
			if len(output) > 0 {
				value = output
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": value})
		}
	}
	return input, nil
}

func convertCodexTools(tools []agent.Tool) ([]any, error) {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
	}
	return result, nil
}

func newCodexEventConsumer(events chan<- agent.ProviderEvent) *codexEventConsumer {
	return &codexEventConsumer{events: events, tools: make(map[string]*codexToolCall)}
}

func (consumer *codexEventConsumer) consume(event codexResponseEvent) error {
	switch event.Type {
	case "response.reasoning_summary_part.added":
		consumer.reasoningPartNeedsBreak = consumer.thinking.Len() > 0
	case "response.reasoning_summary_text.delta":
		delta := event.Delta
		if consumer.reasoningPartNeedsBreak && delta != "" {
			delta = "\n\n" + delta
			consumer.reasoningPartNeedsBreak = false
		}
		consumer.thinking.WriteString(delta)
		consumer.events <- agent.ProviderEvent{Type: agent.ProviderThinkingDelta, Delta: delta}
	case "response.output_text.delta":
		consumer.text.WriteString(event.Delta)
		consumer.events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: event.Delta}
	case "response.output_item.added":
		var item struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		}
		if json.Unmarshal(event.Item, &item) == nil && item.Type == "function_call" {
			consumer.tools[item.ID] = &codexToolCall{itemID: item.ID, callID: item.CallID, name: item.Name}
			consumer.toolOrder = append(consumer.toolOrder, item.ID)
		}
	case "response.function_call_arguments.delta":
		itemID := event.ItemID
		if itemID == "" && len(consumer.tools) == 1 {
			for id := range consumer.tools {
				itemID = id
			}
		}
		if call := consumer.tools[itemID]; call != nil {
			call.arguments.WriteString(event.Delta)
		} else {
			return fmt.Errorf("Codex streamed function-call arguments for unknown item %q", itemID)
		}
	case "response.output_item.done":
		return consumer.consumeOutputItem(event.Item)
	case "response.completed", "response.done", "response.incomplete":
		consumer.done = true
		consumer.stopReason = "stop"
		if event.Type == "response.incomplete" || event.Response != nil && event.Response.Status == "incomplete" {
			consumer.stopReason = "length"
		} else if len(consumer.tools) > 0 {
			consumer.stopReason = "toolUse"
		}
		if event.Response != nil {
			consumer.responseID = event.Response.ID
			consumer.usage = mapCodexUsage(event.Response.Usage)
		}
		if err := consumer.emitPendingTools(); err != nil {
			return err
		}
		consumer.events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: consumer.stopReason, Usage: consumer.usage}
	}
	return nil
}

func (consumer *codexEventConsumer) consumeOutputItem(raw json.RawMessage) error {
	var item struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return nil
	}
	switch item.Type {
	case "function_call":
		call := consumer.tools[item.ID]
		if call == nil {
			call = &codexToolCall{itemID: item.ID, callID: item.CallID, name: item.Name}
			consumer.tools[item.ID] = call
			consumer.toolOrder = append(consumer.toolOrder, item.ID)
		}
		if item.CallID != "" {
			call.callID = item.CallID
		}
		if item.Name != "" {
			call.name = item.Name
		}
		if len(item.Arguments) > 0 {
			var encoded string
			if json.Unmarshal(item.Arguments, &encoded) == nil {
				call.arguments.Reset()
				call.arguments.WriteString(encoded)
			} else if json.Valid(item.Arguments) {
				call.arguments.Reset()
				call.arguments.Write(item.Arguments)
			}
		}
		return consumer.emitTool(call)
	case "message":
		var texts []string
		for _, part := range item.Content {
			if part.Type == "output_text" && part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		if len(texts) > 0 {
			consumer.responseItems = append(consumer.responseItems, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": strings.Join(texts, "")}}})
		}
	}
	return nil
}

func (consumer *codexEventConsumer) emitTool(call *codexToolCall) error {
	if call.emitted {
		return nil
	}
	arguments := map[string]any{}
	if value := strings.TrimSpace(call.arguments.String()); value != "" {
		if err := json.Unmarshal([]byte(value), &arguments); err != nil {
			return fmt.Errorf("decode Codex function-call arguments: %w", err)
		}
	}
	call.emitted = true
	consumer.events <- agent.ProviderEvent{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Type: "toolCall", ID: call.callID, Name: call.name, Arguments: arguments}}
	consumer.responseItems = append(consumer.responseItems, map[string]string{"type": "function_call", "call_id": call.callID, "name": call.name, "arguments": call.arguments.String()})
	return nil
}

func (consumer *codexEventConsumer) emitPendingTools() error {
	for _, id := range consumer.toolOrder {
		if call := consumer.tools[id]; call != nil && !call.emitted {
			if err := consumer.emitTool(call); err != nil {
				return err
			}
		}
	}
	return nil
}

func (consumer *codexEventConsumer) normalizedResponseItems() []any {
	items := cloneJSONSlice(consumer.responseItems)
	hasMessage := false
	for _, item := range items {
		if object, ok := item.(map[string]any); ok && object["type"] == "message" {
			hasMessage = true
		}
	}
	if !hasMessage && consumer.text.Len() > 0 {
		items = append([]any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": consumer.text.String()}}}}, items...)
	}
	return items
}

func mapCodexUsage(usage *codexUsage) agent.Usage {
	if usage == nil {
		return agent.Usage{}
	}
	cacheRead := 0
	if usage.InputDetails != nil {
		cacheRead = usage.InputDetails.CachedTokens
	}
	return agent.Usage{Input: usage.InputTokens, Output: usage.OutputTokens, CacheRead: cacheRead, TotalTokens: usage.TotalTokens}
}

func codexResponseError(event codexResponseEvent) error {
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
			message = "Codex returned an error"
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

func readCodexSSE(input io.Reader, consumer *codexEventConsumer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var dataLines []string
	dispatch := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if data == "[DONE]" {
			return nil
		}
		var event codexResponseEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("decode Codex SSE event: %w", err)
		}
		if apiErr := codexResponseError(event); apiErr != nil {
			return apiErr
		}
		return consumer.consume(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil || consumer.done {
				return err
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Codex SSE stream: %w", err)
	}
	if err := dispatch(); err != nil {
		return err
	}
	if !consumer.done {
		return errors.New("Codex SSE stream ended without completion")
	}
	return nil
}

func buildCodexHeaders(token, accountID string) http.Header {
	headers := make(http.Header, 6)
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("ChatGPT-Account-ID", accountID)
	headers.Set("Originator", "pi")
	headers.Set("User-Agent", "pi-go-agent/0.1")
	return headers
}

func codexAccountIDFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("Codex bearer token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode Codex bearer token: %w", err)
	}
	var claims struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("decode Codex bearer token claims: %w", err)
	}
	if claims.Auth.AccountID == "" {
		return "", errors.New("Codex bearer token does not contain chatgpt_account_id")
	}
	return claims.Auth.AccountID, nil
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

func codexWebSocketDialError(err error, response *http.Response) error {
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

func requestFingerprint(body codexRequestBody) string {
	body.Type = ""
	body.Input = nil
	body.PreviousResponseID = ""
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

func jsonValuesEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func cloneJSONSlice(values []any) []any {
	if len(values) == 0 {
		return nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return append([]any(nil), values...)
	}
	var clone []any
	if json.Unmarshal(encoded, &clone) != nil {
		return append([]any(nil), values...)
	}
	return clone
}
