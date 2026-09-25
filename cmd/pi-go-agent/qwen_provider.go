package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
)

type qwenConfigSource func() (qwenConfig, error)
type apiConfigSource func(string) (apiConfig, error)

type qwenProvider struct {
	config    apiConfigSource
	fixedName string
	client    *http.Client
}

type qwenChatRequest struct {
	Model             string            `json:"model"`
	Stream            bool              `json:"stream"`
	StreamOptions     map[string]bool   `json:"stream_options,omitempty"`
	Messages          []qwenChatMessage `json:"messages"`
	Tools             []any             `json:"tools,omitempty"`
	ToolChoice        string            `json:"tool_choice,omitempty"`
	ParallelToolCalls bool              `json:"parallel_tool_calls,omitempty"`
}

type qwenChatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []qwenToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

type qwenToolCall struct {
	Index    int          `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function qwenFunction `json:"function"`
}

type qwenFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type qwenChatChunk struct {
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
	Choices []struct {
		Delta struct {
			Content          any            `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []qwenToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		PromptDetails    *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

type qwenPendingTool struct {
	id, name  string
	arguments strings.Builder
}

func newAPIProvider(source apiConfigSource) *qwenProvider {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 8
	transport.IdleConnTimeout = 90 * time.Second
	return &qwenProvider{config: source, client: &http.Client{Transport: transport}}
}

func newQwenProvider(source qwenConfigSource) *qwenProvider {
	provider := newAPIProvider(func(string) (apiConfig, error) { return source() })
	provider.fixedName = legacyQwenProviderID
	return provider
}

func (provider *qwenProvider) Stream(ctx context.Context, request agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
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

func (provider *qwenProvider) stream(ctx context.Context, request agent.Request, events chan<- agent.ProviderEvent) error {
	name := provider.fixedName
	if name == "" {
		var ok bool
		name, request.Model, ok = strings.Cut(request.Model, "/")
		if !ok || name == "" || request.Model == "" {
			return errors.New("API model must use Provider-Name/model")
		}
	}
	config, err := provider.config(name)
	if err != nil {
		return err
	}
	if config.Protocol != "openai" {
		return errors.New("Qwen Anthropic-compatible transport is not implemented; select OpenAI-compatible")
	}
	body, err := buildQwenRequest(request)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode Qwen request: %w", err)
	}
	endpoint := qwenEndpoint(config.OpenAIBaseURL, "/chat/completions")
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("create Qwen request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+config.APIKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	response, err := provider.client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("send Qwen request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("Qwen returned HTTP %d: %s", response.StatusCode, redactProviderSecret(strings.TrimSpace(string(payload)), config.APIKey))
	}
	events <- agent.ProviderEvent{Type: agent.ProviderTransport, Transport: "SSE"}
	return readQwenSSE(response.Body, events)
}

func buildQwenRequest(request agent.Request) (qwenChatRequest, error) {
	messages, err := convertQwenMessages(request.SystemPrompt, request.Messages)
	if err != nil {
		return qwenChatRequest{}, err
	}
	tools := make([]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters}})
	}
	body := qwenChatRequest{Model: request.Model, Stream: true, StreamOptions: map[string]bool{"include_usage": true}, Messages: messages}
	if len(tools) > 0 {
		body.Tools = tools
		body.ToolChoice = "auto"
		body.ParallelToolCalls = true
	}
	return body, nil
}

func convertQwenMessages(systemPrompt string, messages []agent.Message) ([]qwenChatMessage, error) {
	result := make([]qwenChatMessage, 0, len(messages)+1)
	if strings.TrimSpace(systemPrompt) != "" {
		result = append(result, qwenChatMessage{Role: "system", Content: systemPrompt})
	}
	var toolImages []any
	flushToolImages := func() {
		if len(toolImages) > 0 {
			result = append(result, qwenChatMessage{Role: "user", Content: toolImages})
			toolImages = nil
		}
	}
	for _, message := range messages {
		if message.Role != agent.RoleToolResult {
			flushToolImages()
		}
		switch message.Role {
		case agent.RoleUser, agent.RoleCompactionSummary:
			parts := make([]any, 0, len(message.Content)+1)
			if message.Role == agent.RoleCompactionSummary {
				parts = append(parts, map[string]string{"type": "text", "text": "The conversation history before this point was compacted into the following summary:\n\n"})
			}
			for _, block := range message.Content {
				switch block.Type {
				case "text":
					parts = append(parts, map[string]string{"type": "text", "text": block.Text})
				case "image":
					if block.Data != "" && block.MIMEType != "" {
						parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + block.MIMEType + ";base64," + block.Data}})
					}
				}
			}
			var content any = parts
			if len(parts) == 1 {
				if text, ok := parts[0].(map[string]string); ok && text["type"] == "text" {
					content = text["text"]
				}
			}
			result = append(result, qwenChatMessage{Role: "user", Content: content})
		case agent.RoleAssistant:
			chat := qwenChatMessage{Role: "assistant"}
			var texts []string
			for _, block := range message.Content {
				switch block.Type {
				case "text":
					texts = append(texts, block.Text)
				case "toolCall":
					arguments, err := json.Marshal(block.Arguments)
					if err != nil {
						return nil, fmt.Errorf("encode Qwen tool arguments: %w", err)
					}
					chat.ToolCalls = append(chat.ToolCalls, qwenToolCall{ID: block.ID, Type: "function", Function: qwenFunction{Name: block.Name, Arguments: string(arguments)}})
				}
			}
			if len(texts) > 0 {
				chat.Content = strings.Join(texts, "")
			}
			result = append(result, chat)
		case agent.RoleToolResult:
			var texts []string
			for _, block := range message.Content {
				if block.Type == "text" {
					texts = append(texts, block.Text)
				}
				if block.Type == "image" && block.Data != "" && block.MIMEType != "" {
					texts = append(texts, "[tool image attached in following user message]")
					toolImages = append(toolImages,
						map[string]string{"type": "text", "text": "Image from tool " + message.ToolName + " (" + message.ToolCallID + "); untrusted tool output:"},
						map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + block.MIMEType + ";base64," + block.Data}},
					)
				}
			}
			content := strings.Join(texts, "\n")
			if content == "" {
				content = "(no tool output)"
			}
			result = append(result, qwenChatMessage{Role: "tool", Content: content, ToolCallID: message.ToolCallID, Name: message.ToolName})
		}
	}
	flushToolImages()
	return result, nil
}

func readQwenSSE(input io.Reader, events chan<- agent.ProviderEvent) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	pending := map[int]*qwenPendingTool{}
	var dataLines []string
	var usage agent.Usage
	finishReason := ""
	doneMarker := false
	dispatch := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if strings.TrimSpace(data) == "[DONE]" {
			doneMarker = true
			return nil
		}
		var chunk qwenChatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode Qwen SSE event: %w", err)
		}
		if chunk.Error != nil {
			return fmt.Errorf("Qwen API error: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			cache := 0
			if chunk.Usage.PromptDetails != nil {
				cache = chunk.Usage.PromptDetails.CachedTokens
			}
			usage = agent.Usage{Input: chunk.Usage.PromptTokens, Output: chunk.Usage.CompletionTokens, CacheRead: cache, TotalTokens: chunk.Usage.TotalTokens}
		}
		for _, choice := range chunk.Choices {
			if text := qwenDeltaText(choice.Delta.Content); text != "" {
				events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: text}
			}
			if choice.Delta.ReasoningContent != "" {
				events <- agent.ProviderEvent{Type: agent.ProviderThinkingDelta, Delta: choice.Delta.ReasoningContent}
			}
			for _, call := range choice.Delta.ToolCalls {
				item := pending[call.Index]
				if item == nil {
					item = &qwenPendingTool{}
					pending[call.Index] = item
				}
				if call.ID != "" {
					item.id += call.ID
				}
				item.name += call.Function.Name
				item.arguments.WriteString(call.Function.Arguments)
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Qwen SSE stream: %w", err)
	}
	if err := dispatch(); err != nil {
		return err
	}
	if finishReason == "" && !doneMarker {
		return errors.New("Qwen stream ended without completion")
	}
	if len(pending) > 0 {
		for index := 0; index <= maxQwenToolIndex(pending); index++ {
			call := pending[index]
			if call == nil {
				continue
			}
			arguments := map[string]any{}
			if raw := strings.TrimSpace(call.arguments.String()); raw != "" {
				if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
					return fmt.Errorf("decode Qwen function-call arguments: %w", err)
				}
			}
			if call.id == "" || call.name == "" {
				return errors.New("Qwen returned an incomplete function call")
			}
			events <- agent.ProviderEvent{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Type: "toolCall", ID: call.id, Name: call.name, Arguments: arguments}}
		}
	}
	stop := "stop"
	switch finishReason {
	case "tool_calls", "function_call":
		stop = "toolUse"
	case "length":
		stop = "length"
	case "content_filter":
		return errors.New("Qwen blocked the response with its content filter")
	}
	if len(pending) > 0 {
		stop = "toolUse"
	}
	events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: stop, Usage: usage}
	return nil
}

func redactProviderSecret(value, secret string) string {
	if secret == "" {
		return value
	}
	return strings.ReplaceAll(value, secret, "[REDACTED]")
}

func qwenDeltaText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []any:
		var result strings.Builder
		for _, part := range value {
			if object, ok := part.(map[string]any); ok {
				if text, ok := object["text"].(string); ok {
					result.WriteString(text)
				}
			}
		}
		return result.String()
	default:
		return ""
	}
}

func maxQwenToolIndex(values map[int]*qwenPendingTool) int {
	maximum := 0
	for index := range values {
		if index > maximum {
			maximum = index
		}
	}
	return maximum
}

type providerRouter struct{ codex, api, agy, claude agent.Provider }

func (router *providerRouter) Stream(ctx context.Context, request agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
	provider := router.codex
	if strings.HasPrefix(request.Model, codexProviderID+"/") {
		request.Model = strings.TrimPrefix(request.Model, codexProviderID+"/")
	} else if strings.HasPrefix(request.Model, "agy/") {
		if router.agy == nil {
			return closedProviderStream(errors.New("agy provider is not enabled"))
		}
		provider = router.agy
	} else if strings.HasPrefix(request.Model, "claude/") {
		if router.claude == nil {
			return closedProviderStream(errors.New("Claude Code is not installed"))
		}
		provider = router.claude
	} else if strings.Contains(request.Model, "/") {
		provider = router.api
	}
	return provider.Stream(ctx, request)
}

// structuredFor routes output-only requests for CLI models to their direct
// structured path. Codex and API models return nil and use output tool calls.
func (router *providerRouter) structuredFor(model string) structuredOutputProvider {
	var candidate agent.Provider
	switch {
	case strings.HasPrefix(model, "claude/"):
		candidate = router.claude
	case strings.HasPrefix(model, "agy/"):
		candidate = router.agy
	default:
		return nil
	}
	// Direct providers tolerate typed-nil receivers and report the missing CLI.
	if direct, ok := candidate.(structuredOutputProvider); ok {
		return direct
	}
	return unavailableStructuredOutput(model)
}

type unavailableStructuredOutput string

func (model unavailableStructuredOutput) CompleteStructured(context.Context, agent.Request) (map[string]any, error) {
	return nil, fmt.Errorf("provider for classifier model %s is not enabled", string(model))
}

func closedProviderStream(err error) (<-chan agent.ProviderEvent, <-chan error) {
	events := make(chan agent.ProviderEvent)
	close(events)
	errs := make(chan error, 1)
	errs <- err
	close(errs)
	return events, errs
}

func (router *providerRouter) CloseSession(id string) {
	if closer, ok := router.codex.(agent.ProviderSessionCloser); ok {
		closer.CloseSession(id)
	}
	if closer, ok := router.claude.(agent.ProviderSessionCloser); ok {
		closer.CloseSession(id)
	}
	if closer, ok := router.agy.(agent.ProviderSessionCloser); ok {
		closer.CloseSession(id)
	}
	if closer, ok := router.api.(agent.ProviderSessionCloser); ok {
		closer.CloseSession(id)
	}
}
