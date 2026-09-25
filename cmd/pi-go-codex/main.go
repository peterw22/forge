// pi-go-codex is a Codex streaming client and Pi provider bridge for the
// Go-port migration spike. In provider mode it reads a Pi conversation from stdin and
// emits a strict JSONL event stream that the adjacent Pi extension adapts to
// AssistantMessageEventStream.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultEndpoint = "https://chatgpt.com/backend-api/codex/responses"

// Config contains only request-scoped state. The bearer token is deliberately
// read from an environment variable rather than a command-line argument.
type Config struct {
	Endpoint  string
	Model     string
	Prompt    string
	Token     string
	SessionID string
	Transport string
	Headers   map[string]*string
	Timeout   time.Duration
	Client    *http.Client
}

type requestBody struct {
	Model          string   `json:"model"`
	Store          bool     `json:"store"`
	Stream         bool     `json:"stream"`
	Instructions   string   `json:"instructions"`
	Input          any      `json:"input"`
	Tools          any      `json:"tools,omitempty"`
	ToolChoice     string   `json:"tool_choice,omitempty"`
	ParallelTools  bool     `json:"parallel_tool_calls,omitempty"`
	Reasoning      any      `json:"reasoning,omitempty"`
	Text           any      `json:"text"`
	Include        []string `json:"include,omitempty"`
	PromptCacheKey string   `json:"prompt_cache_key,omitempty"`
}

type inputItem struct {
	Role    string         `json:"role"`
	Content []inputContent `json:"content"`
}

type inputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type textOptions struct {
	Verbosity string `json:"verbosity"`
}

type sseEvent struct {
	Type    string          `json:"type"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Delta   string          `json:"delta"`
	ItemID  string          `json:"item_id"`
	Item    json.RawMessage `json:"item"`
	Part    json.RawMessage `json:"part"`
	Error   *struct {
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
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
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

// BridgeRequest uses JSON-compatible Pi message/tool shapes. It intentionally
// has no token field: authentication comes only from PI_GO_CODEX_TOKEN.
type BridgeRequest struct {
	Model        string             `json:"model"`
	SystemPrompt string             `json:"systemPrompt"`
	Messages     []BridgeMessage    `json:"messages"`
	Tools        []BridgeTool       `json:"tools"`
	Reasoning    string             `json:"reasoning,omitempty"`
	SessionID    string             `json:"sessionId,omitempty"`
	Transport    string             `json:"transport,omitempty"`
	Headers      map[string]*string `json:"headers,omitempty"`
}

type BridgeMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
}

type BridgeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type bridgeAck struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

type bridgeEvent struct {
	Type         string          `json:"type"`
	Payload      any             `json:"payload,omitempty"`
	ContentIndex int             `json:"contentIndex,omitempty"`
	Delta        string          `json:"delta,omitempty"`
	Content      string          `json:"content,omitempty"`
	ID           string          `json:"id,omitempty"`
	Name         string          `json:"name,omitempty"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	ResponseID   string          `json:"responseId,omitempty"`
	Usage        *codexUsage     `json:"usage,omitempty"`
	Message      string          `json:"message,omitempty"`
	Status       int             `json:"status,omitempty"`
	Headers      http.Header     `json:"headers,omitempty"`
	Transport    string          `json:"transport,omitempty"`
}

func main() {
	var config Config
	var providerMode bool
	flag.StringVar(&config.Endpoint, "endpoint", defaultEndpoint, "Codex Responses endpoint")
	flag.StringVar(&config.Model, "model", "gpt-6-sol", "Codex model ID")
	flag.StringVar(&config.Prompt, "prompt", "", "standalone prompt to send")
	flag.StringVar(&config.SessionID, "session-id", "", "optional request/session identifier")
	flag.StringVar(&config.Transport, "transport", "auto", "Codex transport: auto, websocket, websocket-cached, or sse")
	flag.DurationVar(&config.Timeout, "timeout", 90*time.Second, "request timeout")
	flag.BoolVar(&providerMode, "provider", false, "run the Pi provider JSONL bridge")
	flag.Parse()

	config.Token = os.Getenv("PI_GO_CODEX_TOKEN")
	if providerMode {
		if err := runProvider(context.Background(), os.Stdin, os.Stdout, config); err != nil {
			_ = writeBridgeEvent(os.Stdout, bridgeEvent{Type: "error", Message: err.Error()})
		}
		return
	}
	if err := run(context.Background(), os.Stdout, config); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer, config Config) error {
	if strings.TrimSpace(config.Prompt) == "" {
		return errors.New("--prompt must not be empty")
	}
	body := requestBody{
		Model:        config.Model,
		Store:        false,
		Stream:       true,
		Instructions: "You are a helpful coding assistant.",
		Input: []inputItem{{
			Role:    "user",
			Content: []inputContent{{Type: "input_text", Text: config.Prompt}},
		}},
		Text: textOptions{Verbosity: "low"},
	}
	return streamCodexEvents(ctx, body, config, streamCallbacks{}, standaloneEventHandler(output))
}

func runProvider(ctx context.Context, input io.Reader, output io.Writer, config Config) error {
	decoder := json.NewDecoder(input)
	var bridgeRequest BridgeRequest
	if err := decoder.Decode(&bridgeRequest); err != nil {
		return fmt.Errorf("decode provider request: %w", err)
	}
	if bridgeRequest.Model == "" {
		bridgeRequest.Model = config.Model
	}
	body, err := buildProviderRequestBody(bridgeRequest)
	if err != nil {
		return err
	}
	var payload any = body
	if err := writeBridgeEvent(output, bridgeEvent{Type: "payload", Payload: payload}); err != nil {
		return err
	}

	var ack bridgeAck
	if err := decoder.Decode(&ack); err != nil {
		return fmt.Errorf("read provider payload acknowledgement: %w", err)
	}
	if ack.Type != "payload" {
		return fmt.Errorf("expected provider payload acknowledgement, received %q", ack.Type)
	}
	if ack.Payload != nil {
		payload = ack.Payload
	}

	config.Model = bridgeRequest.Model
	config.SessionID = bridgeRequest.SessionID
	if bridgeRequest.Transport != "" {
		config.Transport = bridgeRequest.Transport
	}
	config.Headers = bridgeRequest.Headers
	callbacks := streamCallbacks{
		response: func(status int, headers http.Header) error {
			return writeBridgeEvent(output, bridgeEvent{Type: "response", Status: status, Headers: headers})
		},
		start: func(transport string) error {
			return writeBridgeEvent(output, bridgeEvent{Type: "start", Transport: transport})
		},
	}
	return streamCodexEvents(ctx, payload, config, callbacks, providerEventHandler(output))
}

func buildProviderRequestBody(request BridgeRequest) (requestBody, error) {
	input, err := convertBridgeMessages(request.Messages)
	if err != nil {
		return requestBody{}, err
	}
	tools, err := convertBridgeTools(request.Tools)
	if err != nil {
		return requestBody{}, err
	}
	body := requestBody{
		Model:          request.Model,
		Store:          false,
		Stream:         true,
		Instructions:   request.SystemPrompt,
		Input:          input,
		Text:           textOptions{Verbosity: "low"},
		Include:        []string{"reasoning.encrypted_content"},
		PromptCacheKey: request.SessionID,
	}
	if len(tools) > 0 {
		body.Tools = tools
		body.ToolChoice = "auto"
		body.ParallelTools = true
	}
	if request.Reasoning != "" && request.Reasoning != "off" {
		body.Reasoning = map[string]string{"effort": request.Reasoning, "summary": "auto"}
	}
	return body, nil
}

func convertBridgeMessages(messages []BridgeMessage) ([]any, error) {
	input := make([]any, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "user", "compactionSummary":
			content, err := inputTextContent(message.Content)
			if err != nil {
				return nil, err
			}
			if message.Role == "compactionSummary" {
				content = append([]any{map[string]string{"type": "input_text", "text": "The conversation history before this point was compacted into the following summary:\n\n"}}, content...)
			}
			input = append(input, map[string]any{"type": "message", "role": "user", "content": content})
		case "assistant":
			blocks, err := assistantContent(message.Content)
			if err != nil {
				return nil, err
			}
			for _, block := range blocks {
				input = append(input, block)
			}
		case "toolResult":
			output, err := toolResultOutput(message.Content)
			if err != nil {
				return nil, err
			}
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": message.ToolCallID, "output": output,
			})
		default:
			// Pi custom/session-only messages do not map to Codex request input.
		}
	}
	return input, nil
}

func inputTextContent(content json.RawMessage) ([]any, error) {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return []any{map[string]string{"type": "input_text", "text": text}}, nil
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Data     string `json:"data"`
		MIMEType string `json:"mimeType"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, fmt.Errorf("decode user content: %w", err)
	}
	result := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			result = append(result, map[string]string{"type": "input_text", "text": block.Text})
		case "image":
			if block.Data != "" && block.MIMEType != "" {
				result = append(result, map[string]string{"type": "input_image", "detail": "auto", "image_url": "data:" + block.MIMEType + ";base64," + block.Data})
			}
		}
	}
	return result, nil
}

func assistantContent(content json.RawMessage) ([]any, error) {
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, fmt.Errorf("decode assistant content: %w", err)
	}
	result := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			result = append(result, map[string]any{
				"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": block.Text}},
			})
		case "toolCall":
			arguments := "{}"
			if len(block.Arguments) > 0 {
				arguments = string(block.Arguments)
			}
			result = append(result, map[string]string{
				"type": "function_call", "call_id": block.ID, "name": block.Name, "arguments": arguments,
			})
		}
	}
	return result, nil
}

func toolResultOutput(content json.RawMessage) (any, error) {
	var raw []map[string]any
	if err := json.Unmarshal(content, &raw); err != nil {
		text, textErr := contentText(content)
		return text, textErr
	}
	output := make([]any, 0, len(raw))
	for _, block := range raw {
		typeName, _ := block["type"].(string)
		switch typeName {
		case "text":
			text, _ := block["text"].(string)
			output = append(output, map[string]any{"type": "input_text", "text": text})
		case "image":
			data, _ := block["data"].(string)
			mime, _ := block["mimeType"].(string)
			if data != "" && mime != "" {
				output = append(output, map[string]any{"type": "input_image", "detail": "auto", "image_url": "data:" + mime + ";base64," + data})
			}
		}
	}
	if len(output) == 0 {
		return "(no tool output)", nil
	}
	return output, nil
}

func contentText(content json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return "", fmt.Errorf("decode content text: %w", err)
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

func convertBridgeTools(tools []BridgeTool) ([]any, error) {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		var parameters any
		if len(tool.Parameters) > 0 {
			if err := json.Unmarshal(tool.Parameters, &parameters); err != nil {
				return nil, fmt.Errorf("decode tool schema %q: %w", tool.Name, err)
			}
		}
		result = append(result, map[string]any{
			"type": "function", "name": tool.Name, "description": tool.Description, "parameters": parameters,
		})
	}
	return result, nil
}

func sendCodexRequest(ctx context.Context, body any, config Config) (*http.Response, error) {
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("PI_GO_CODEX_TOKEN is not set")
	}
	accountID, err := accountIDFromJWT(config.Token)
	if err != nil {
		return nil, err
	}
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode Codex request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.Endpoint, bytes.NewReader(encodedBody))
	if err != nil {
		return nil, fmt.Errorf("create Codex request: %w", err)
	}
	for name, value := range config.Headers {
		if value == nil {
			req.Header.Del(name)
		} else {
			req.Header.Set(name, *value)
		}
	}
	req.Header.Set("Authorization", "Bearer "+config.Token)
	req.Header.Set("ChatGPT-Account-ID", accountID)
	req.Header.Set("Originator", "pi")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pi-go-codex-bridge/0.1")
	if config.SessionID != "" {
		req.Header.Set("Session-ID", config.SessionID)
		req.Header.Set("X-Client-Request-ID", config.SessionID)
	}

	client := config.Client
	if client == nil {
		client = &http.Client{Timeout: config.Timeout}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send Codex request: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			return nil, fmt.Errorf("Codex returned HTTP %d", response.StatusCode)
		}
		return nil, fmt.Errorf("Codex returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	return response, nil
}

func accountIDFromJWT(token string) (string, error) {
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

func readSSE(input io.Reader, output io.Writer) error {
	return readEvents(input, standaloneEventHandler(output))
}

func standaloneEventHandler(output io.Writer) func(sseEvent) (bool, error) {
	return func(event sseEvent) (bool, error) {
		if event.Type == "error" && event.Error != nil {
			return false, errors.New(event.Error.Message)
		}
		if event.Type == "response.failed" && event.Response != nil && event.Response.Error != nil {
			return false, errors.New(event.Response.Error.Message)
		}
		if event.Type == "response.output_text.delta" && event.Delta != "" {
			if _, err := io.WriteString(output, event.Delta); err != nil {
				return false, fmt.Errorf("write Codex output: %w", err)
			}
		}
		return event.Type == "response.completed" || event.Type == "response.incomplete", nil
	}
}

func readProviderSSE(input io.Reader, output io.Writer) error {
	return readEvents(input, providerEventHandler(output))
}

func providerEventHandler(output io.Writer) func(sseEvent) (bool, error) {
	textIndex := -1
	reasoningIndex := -1
	nextIndex := 0
	toolIndexes := map[string]int{}
	toolArguments := map[string]string{}
	return func(event sseEvent) (bool, error) {
		switch event.Type {
		case "error":
			if event.Error != nil {
				return false, errors.New(event.Error.Message)
			}
		case "response.failed":
			if event.Response != nil && event.Response.Error != nil {
				return false, errors.New(event.Response.Error.Message)
			}
		case "response.reasoning_summary_part.added":
			if reasoningIndex < 0 {
				reasoningIndex = nextIndex
				nextIndex++
				if err := writeBridgeEvent(output, bridgeEvent{Type: "thinking_start", ContentIndex: reasoningIndex}); err != nil {
					return false, err
				}
			}
		case "response.reasoning_summary_text.delta":
			if reasoningIndex < 0 {
				reasoningIndex = nextIndex
				nextIndex++
				if err := writeBridgeEvent(output, bridgeEvent{Type: "thinking_start", ContentIndex: reasoningIndex}); err != nil {
					return false, err
				}
			}
			if err := writeBridgeEvent(output, bridgeEvent{Type: "thinking_delta", ContentIndex: reasoningIndex, Delta: event.Delta}); err != nil {
				return false, err
			}
		case "response.content_part.added":
			var part struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(event.Part, &part) == nil && part.Type == "output_text" && textIndex < 0 {
				textIndex = nextIndex
				nextIndex++
				if err := writeBridgeEvent(output, bridgeEvent{Type: "text_start", ContentIndex: textIndex}); err != nil {
					return false, err
				}
			}
		case "response.output_text.delta":
			if textIndex < 0 {
				textIndex = nextIndex
				nextIndex++
				if err := writeBridgeEvent(output, bridgeEvent{Type: "text_start", ContentIndex: textIndex}); err != nil {
					return false, err
				}
			}
			if err := writeBridgeEvent(output, bridgeEvent{Type: "text_delta", ContentIndex: textIndex, Delta: event.Delta}); err != nil {
				return false, err
			}
		case "response.output_item.added":
			var item struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				CallID string `json:"call_id"`
				Name   string `json:"name"`
			}
			if json.Unmarshal(event.Item, &item) == nil && item.Type == "function_call" {
				index := nextIndex
				nextIndex++
				toolIndexes[item.ID] = index
				toolArguments[item.ID] = ""
				if err := writeBridgeEvent(output, bridgeEvent{Type: "toolcall_start", ContentIndex: index, ID: item.CallID, Name: item.Name}); err != nil {
					return false, err
				}
			}
		case "response.function_call_arguments.delta":
			itemID := event.ItemID
			if itemID == "" && len(toolIndexes) == 1 {
				for id := range toolIndexes {
					itemID = id
				}
			}
			if index, exists := toolIndexes[itemID]; exists {
				toolArguments[itemID] += event.Delta
				if err := writeBridgeEvent(output, bridgeEvent{Type: "toolcall_delta", ContentIndex: index, Delta: event.Delta}); err != nil {
					return false, err
				}
			} else {
				return false, fmt.Errorf("Codex streamed function-call arguments for unknown item %q", itemID)
			}
		case "response.output_item.done":
			var item struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				CallID    string          `json:"call_id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(event.Item, &item) == nil && item.Type == "function_call" {
				index, exists := toolIndexes[item.ID]
				if !exists {
					index = nextIndex
					nextIndex++
					if err := writeBridgeEvent(output, bridgeEvent{Type: "toolcall_start", ContentIndex: index, ID: item.CallID, Name: item.Name}); err != nil {
						return false, err
					}
				}
				arguments := objectArguments(item.Arguments)
				if len(arguments) == 0 {
					arguments = objectArguments(json.RawMessage(toolArguments[item.ID]))
				}
				if len(arguments) == 0 {
					arguments = json.RawMessage("{}")
				}
				if err := writeBridgeEvent(output, bridgeEvent{Type: "toolcall_end", ContentIndex: index, ID: item.CallID, Name: item.Name, Arguments: arguments}); err != nil {
					return false, err
				}
			}
		case "response.completed", "response.incomplete":
			if reasoningIndex >= 0 {
				if err := writeBridgeEvent(output, bridgeEvent{Type: "thinking_end", ContentIndex: reasoningIndex}); err != nil {
					return false, err
				}
			}
			if textIndex >= 0 {
				if err := writeBridgeEvent(output, bridgeEvent{Type: "text_end", ContentIndex: textIndex}); err != nil {
					return false, err
				}
			}
			reason := "stop"
			if event.Type == "response.incomplete" {
				reason = "length"
			}
			if len(toolIndexes) > 0 {
				reason = "toolUse"
			}
			responseID := ""
			var usage *codexUsage
			if event.Response != nil {
				responseID = event.Response.ID
				usage = event.Response.Usage
			}
			if err := writeBridgeEvent(output, bridgeEvent{Type: "done", Reason: reason, ResponseID: responseID, Usage: usage}); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil
	}
}

func objectArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var value map[string]any
	if !json.Valid(raw) || json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return raw
}

func readEvents(input io.Reader, handle func(sseEvent) (bool, error)) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var dataLines []string
	dispatch := func() (bool, error) {
		if len(dataLines) == 0 {
			return false, nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if data == "[DONE]" {
			return false, nil
		}
		var event sseEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return false, fmt.Errorf("decode Codex SSE event: %w", err)
		}
		return handle(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := dispatch()
			if err != nil || done {
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
	_, err := dispatch()
	return err
}

func writeBridgeEvent(output io.Writer, event bridgeEvent) error {
	return json.NewEncoder(output).Encode(event)
}
