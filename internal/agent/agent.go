// Package agent contains the Go-owned, provider-neutral agent loop. It has no
// terminal, extension, or provider SDK dependency: frontends subscribe to the
// events it emits and providers/tools are injected through small interfaces.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Role string

const (
	RoleUser              Role = "user"
	RoleAssistant         Role = "assistant"
	RoleToolResult        Role = "toolResult"
	RoleCompactionSummary Role = "compactionSummary"
)

type Message struct {
	Role       Role           `json:"role"`
	Content    []ContentBlock `json:"content"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	ToolName   string         `json:"toolName,omitempty"`
	IsError    bool           `json:"isError,omitempty"`
	Timestamp  int64          `json:"timestamp"`
}

type ContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Data      string         `json:"data,omitempty"`
	MIMEType  string         `json:"mimeType,omitempty"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Execute     ToolExecutor
}

type ToolExecutor func(context.Context, map[string]any, func(ToolResult)) (ToolResult, error)

type GuardRequest struct {
	Tool             string
	Arguments        map[string]any
	WorkingDirectory string
	Messages         []Message
}
type GuardDecision struct {
	Allowed     bool
	Reason      string
	Description string
	Scopes      []string
	Remember    func()
}
type ToolGuard interface {
	Check(context.Context, GuardRequest) GuardDecision
}

type ToolResult struct {
	Content []ContentBlock `json:"content"`
	Details any            `json:"details,omitempty"`
	IsError bool           `json:"isError,omitempty"`
}

type Request struct {
	Model        string
	Thinking     string
	SystemPrompt string
	Messages     []Message
	Tools        []Tool
}

type ProviderEventType string

const (
	ProviderTextDelta     ProviderEventType = "text_delta"
	ProviderThinkingDelta ProviderEventType = "thinking_delta"
	ProviderTransport     ProviderEventType = "transport"
	ProviderToolCall      ProviderEventType = "toolcall_end"
	ProviderDone          ProviderEventType = "done"
	ProviderError         ProviderEventType = "error"
)

type ProviderEvent struct {
	Type       ProviderEventType
	Delta      string
	ToolCall   ContentBlock
	StopReason string
	Transport  string
	Usage      Usage
	Err        error
}

type Usage struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CacheRead   int `json:"cacheRead"`
	CacheWrite  int `json:"cacheWrite"`
	TotalTokens int `json:"totalTokens"`
}

type Provider interface {
	Stream(context.Context, Request) (<-chan ProviderEvent, <-chan error)
}

type EventType string

const (
	EventAgentStart          EventType = "agent_start"
	EventAgentEnd            EventType = "agent_end"
	EventTurnStart           EventType = "turn_start"
	EventTurnEnd             EventType = "turn_end"
	EventMessageStart        EventType = "message_start"
	EventMessageUpdate       EventType = "message_update"
	EventMessageEnd          EventType = "message_end"
	EventToolExecutionStart  EventType = "tool_execution_start"
	EventToolExecutionUpdate EventType = "tool_execution_update"
	EventToolExecutionEnd    EventType = "tool_execution_end"
	EventCompactionStart     EventType = "compaction_start"
	EventCompactionEnd       EventType = "compaction_end"
	EventApprovalRequired    EventType = "approval_required"
	EventToolSafetyUpdate    EventType = "tool_safety_update"
	EventUpstreamTransport   EventType = "upstream_transport"
)

type Event struct {
	Type              EventType      `json:"type"`
	Message           *Message       `json:"message,omitempty"`
	ToolCallID        string         `json:"toolCallId,omitempty"`
	ToolName          string         `json:"toolName,omitempty"`
	Arguments         map[string]any `json:"arguments,omitempty"`
	Result            *ToolResult    `json:"result,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
	Usage             Usage          `json:"usage,omitempty"`
	StopReason        string         `json:"stopReason,omitempty"`
	Error             string         `json:"error,omitempty"`
	ApprovalID        string         `json:"approvalId,omitempty"`
	Reason            string         `json:"reason,omitempty"`
	Description       string         `json:"description,omitempty"`
	SafetyStatus      string         `json:"safetyStatus,omitempty"`
	SafetyMessage     string         `json:"safetyMessage,omitempty"`
	UpstreamTransport string         `json:"upstreamTransport,omitempty"`
}

type Config struct {
	Model            string
	Thinking         string
	SystemPrompt     string
	WorkingDirectory string
	Provider         Provider
	Tools            []Tool
	// ParallelTools applies to tool calls in one assistant response. Pi defaults
	// to parallel scheduling; callers may select false while bringing up tools.
	ParallelTools bool
	ToolGuard     ToolGuard
	AllowApproval bool
	YOLO          bool
}

type Agent struct {
	config         Config
	mu             sync.RWMutex
	state          State
	approvalMu     sync.Mutex
	approvalSerial sync.Mutex
	approvals      map[string]chan bool
	approvalSeq    uint64
}

type State struct {
	Messages          []Message `json:"messages"`
	Streaming         bool      `json:"streaming"`
	Usage             Usage     `json:"usage"`
	ContextTokens     int       `json:"contextTokens"`
	UpstreamTransport string    `json:"upstreamTransport,omitempty"`
	YOLO              bool      `json:"yolo"`
}

func New(config Config) (*Agent, error) {
	if config.Provider == nil {
		return nil, errors.New("agent provider is required")
	}
	if config.Model == "" {
		return nil, errors.New("agent model is required")
	}
	return &Agent{config: config, state: State{YOLO: config.YOLO}, approvals: make(map[string]chan bool)}, nil
}

func (a *Agent) Restore(messages []Message, model, thinking string, usage Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Messages = append([]Message(nil), messages...)
	a.state.Usage = usage
	a.state.ContextTokens = estimateMessages(messages)
	if model != "" {
		a.config.Model = model
	}
	if thinking != "" {
		a.config.Thinking = thinking
	}
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.config.Model = model
}

func (a *Agent) Settings() (model, thinking, cwd string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.config.Model, a.config.Thinking, a.config.WorkingDirectory
}

func (a *Agent) SetWorkingDirectory(cwd, systemPrompt string, tools []Tool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.config.WorkingDirectory = cwd
	a.config.SystemPrompt = systemPrompt
	a.config.Tools = append([]Tool(nil), tools...)
}

func (a *Agent) SetThinkingLevel(level string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.config.Thinking = level
}

func (a *Agent) SetYOLO(enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.config.YOLO = enabled
	a.state.YOLO = enabled
}

func (a *Agent) YOLOEnabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.config.YOLO
}

func (a *Agent) Snapshot() State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	state := a.state
	state.Messages = append([]Message(nil), a.state.Messages...)
	return state
}

func (a *Agent) Run(ctx context.Context, prompt string, emit func(Event)) error {
	if prompt == "" {
		return errors.New("prompt must not be empty")
	}
	return a.RunContent(ctx, []ContentBlock{{Type: "text", Text: prompt}}, emit)
}

func (a *Agent) RunContent(ctx context.Context, content []ContentBlock, emit func(Event)) error {
	if len(content) == 0 {
		return errors.New("prompt content must not be empty")
	}
	a.mu.Lock()
	if a.state.Streaming {
		a.mu.Unlock()
		return errors.New("agent is already running")
	}
	a.state.Streaming = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.state.Streaming = false
		a.mu.Unlock()
	}()

	emit(Event{Type: EventAgentStart})
	user := Message{Role: RoleUser, Content: append([]ContentBlock(nil), content...), Timestamp: time.Now().UnixMilli()}
	a.appendMessage(user)
	emit(Event{Type: EventMessageStart, Message: &user})
	emit(Event{Type: EventMessageEnd, Message: &user})

	for {
		if err := ctx.Err(); err != nil {
			emit(Event{Type: EventAgentEnd, Error: err.Error()})
			return err
		}
		emit(Event{Type: EventTurnStart})
		assistant, toolCalls, stopReason, usage, err := a.runProvider(ctx, emit)
		if err != nil {
			emit(Event{Type: EventAgentEnd, Error: err.Error()})
			return err
		}
		a.appendMessage(assistant)
		a.addUsage(usage)
		contextTokens := usage.TotalTokens
		if contextTokens == 0 {
			contextTokens = usage.Input
		}
		a.setContextTokens(contextTokens)
		emit(Event{Type: EventMessageEnd, Message: &assistant, Usage: usage, StopReason: stopReason})

		toolResults, err := a.executeTools(ctx, toolCalls, emit)
		if err != nil {
			emit(Event{Type: EventAgentEnd, Error: err.Error()})
			return err
		}
		for _, result := range toolResults {
			a.appendMessage(result)
			emit(Event{Type: EventMessageStart, Message: &result})
			emit(Event{Type: EventMessageEnd, Message: &result})
		}
		emit(Event{Type: EventTurnEnd, Message: &assistant, StopReason: stopReason})
		if len(toolCalls) == 0 {
			emit(Event{Type: EventAgentEnd})
			return nil
		}
	}
}

func (a *Agent) runProvider(ctx context.Context, emit func(Event)) (Message, []ContentBlock, string, Usage, error) {
	a.mu.RLock()
	model, thinking, systemPrompt, tools := a.config.Model, a.config.Thinking, a.config.SystemPrompt, append([]Tool(nil), a.config.Tools...)
	a.mu.RUnlock()
	request := Request{Model: model, Thinking: thinking, SystemPrompt: systemPrompt, Messages: a.Snapshot().Messages, Tools: tools}
	events, providerErr := a.config.Provider.Stream(ctx, request)
	assistant := Message{Role: RoleAssistant, Timestamp: time.Now().UnixMilli()}
	emit(Event{Type: EventMessageStart, Message: &assistant})
	var calls []ContentBlock
	var stopReason string
	var usage Usage
	for events != nil || providerErr != nil {
		select {
		case <-ctx.Done():
			return Message{}, nil, "aborted", Usage{}, ctx.Err()
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch event.Type {
			case ProviderThinkingDelta:
				if len(assistant.Content) == 0 || assistant.Content[len(assistant.Content)-1].Type != "thinking" {
					assistant.Content = append(assistant.Content, ContentBlock{Type: "thinking"})
				}
				last := len(assistant.Content) - 1
				assistant.Content[last].Text += event.Delta
				emit(Event{Type: EventMessageUpdate, Message: &assistant})
			case ProviderTransport:
				a.setUpstreamTransport(event.Transport)
				emit(Event{Type: EventUpstreamTransport, UpstreamTransport: event.Transport})
			case ProviderTextDelta:
				if len(assistant.Content) == 0 || assistant.Content[len(assistant.Content)-1].Type != "text" {
					assistant.Content = append(assistant.Content, ContentBlock{Type: "text"})
				}
				last := len(assistant.Content) - 1
				assistant.Content[last].Text += event.Delta
				emit(Event{Type: EventMessageUpdate, Message: &assistant})
			case ProviderToolCall:
				if event.ToolCall.Type != "toolCall" || event.ToolCall.ID == "" || event.ToolCall.Name == "" {
					return Message{}, nil, "error", Usage{}, errors.New("provider emitted invalid tool call")
				}
				assistant.Content = append(assistant.Content, event.ToolCall)
				calls = append(calls, event.ToolCall)
				emit(Event{Type: EventMessageUpdate, Message: &assistant})
			case ProviderDone:
				stopReason, usage = event.StopReason, event.Usage
			case ProviderError:
				if event.Err != nil {
					return Message{}, nil, "error", Usage{}, event.Err
				}
				return Message{}, nil, "error", Usage{}, errors.New("provider stream failed")
			}
		case err, ok := <-providerErr:
			if !ok {
				providerErr = nil
				continue
			}
			if err != nil {
				return Message{}, nil, "error", Usage{}, err
			}
		}
	}
	if stopReason == "" {
		return Message{}, nil, "error", Usage{}, errors.New("provider ended without done event")
	}
	return assistant, calls, stopReason, usage, nil
}

func (a *Agent) executeTools(ctx context.Context, calls []ContentBlock, emit func(Event)) ([]Message, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	results := make([]Message, len(calls))
	run := func(index int) error {
		call := calls[index]
		tool, ok := a.tool(call.Name)
		if !ok {
			result := ToolResult{Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Unknown tool: %s", call.Name)}}, IsError: true}
			results[index] = toolResultMessage(call, result)
			emit(Event{Type: EventToolExecutionEnd, ToolCallID: call.ID, ToolName: call.Name, Result: &result, IsError: true})
			return nil
		}
		emit(Event{Type: EventToolExecutionStart, ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})
		if decision := a.guardTool(ctx, call, emit); !decision.Allowed {
			result := ToolResult{Content: []ContentBlock{{Type: "text", Text: "Blocked by Bash Safety: " + decision.Reason}}, IsError: true}
			results[index] = toolResultMessage(call, result)
			emit(Event{Type: EventToolExecutionEnd, ToolCallID: call.ID, ToolName: call.Name, Result: &result, IsError: true})
			return nil
		}
		result, err := executeToolSafely(ctx, tool, call.Arguments, func(update ToolResult) {
			emit(Event{Type: EventToolExecutionUpdate, ToolCallID: call.ID, ToolName: call.Name, Arguments: call.Arguments, Result: &update, IsError: update.IsError})
		})
		if err != nil {
			result = ToolResult{Content: []ContentBlock{{Type: "text", Text: "Tool failed: " + err.Error()}}, IsError: true}
		}
		if len(result.Content) == 0 {
			result.Content = []ContentBlock{{Type: "text", Text: "Tool failed without returning a result."}}
			result.IsError = true
		}
		results[index] = toolResultMessage(call, result)
		emit(Event{Type: EventToolExecutionEnd, ToolCallID: call.ID, ToolName: call.Name, Result: &result, IsError: result.IsError})
		return nil
	}
	if !a.config.ParallelTools || len(calls) == 1 {
		for index := range calls {
			if err := run(index); err != nil {
				return nil, err
			}
		}
		return results, nil
	}
	var group sync.WaitGroup
	errs := make(chan error, len(calls))
	for index := range calls {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			errs <- run(index)
		}(index)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (a *Agent) guardTool(ctx context.Context, call ContentBlock, emit func(Event)) GuardDecision {
	a.mu.RLock()
	guard, allowApproval, yolo, cwd := a.config.ToolGuard, a.config.AllowApproval, a.config.YOLO, a.config.WorkingDirectory
	messages := append([]Message(nil), a.state.Messages...)
	a.mu.RUnlock()
	if yolo || guard == nil {
		return GuardDecision{Allowed: true}
	}
	showLuna := call.Name == "bash"
	if showLuna {
		emit(Event{Type: EventToolSafetyUpdate, ToolCallID: call.ID, ToolName: call.Name, SafetyStatus: "classifying", SafetyMessage: "Luna is classifying this command…"})
	}
	decision := guard.Check(ctx, GuardRequest{Tool: call.Name, Arguments: call.Arguments, WorkingDirectory: cwd, Messages: messages})
	if decision.Allowed {
		if showLuna {
			emit(Event{Type: EventToolSafetyUpdate, ToolCallID: call.ID, ToolName: call.Name, SafetyStatus: "approved", SafetyMessage: "Luna approved this command"})
		}
		return decision
	}
	if showLuna {
		emit(Event{Type: EventToolSafetyUpdate, ToolCallID: call.ID, ToolName: call.Name, SafetyStatus: "rejected", SafetyMessage: "Luna rejected this command: " + decision.Reason})
	}
	if !allowApproval {
		return decision
	}
	a.approvalSerial.Lock()
	defer a.approvalSerial.Unlock()
	id, answer := a.newApproval()
	emit(Event{Type: EventApprovalRequired, ToolCallID: call.ID, ToolName: call.Name, ApprovalID: id, Reason: decision.Reason, Description: decision.Description})
	select {
	case approved := <-answer:
		a.removeApproval(id)
		if approved {
			decision.Allowed = true
			if showLuna {
				emit(Event{Type: EventToolSafetyUpdate, ToolCallID: call.ID, ToolName: call.Name, SafetyStatus: "approved", SafetyMessage: "Manually approved after Luna rejection"})
			}
			if decision.Remember != nil {
				decision.Remember()
			}
		} else {
			decision.Reason = "User rejected the operation. " + decision.Reason
			if showLuna {
				emit(Event{Type: EventToolSafetyUpdate, ToolCallID: call.ID, ToolName: call.Name, SafetyStatus: "rejected", SafetyMessage: "Luna rejected this command; manual approval was declined"})
			}
		}
	case <-ctx.Done():
		a.removeApproval(id)
		decision.Reason = "Approval cancelled: " + ctx.Err().Error()
		if showLuna {
			emit(Event{Type: EventToolSafetyUpdate, ToolCallID: call.ID, ToolName: call.Name, SafetyStatus: "rejected", SafetyMessage: "Luna safety approval was cancelled"})
		}
	}
	return decision
}

func (a *Agent) newApproval() (string, chan bool) {
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	a.approvalSeq++
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	id := fmt.Sprintf("approval-%s-%d", hex.EncodeToString(nonce[:]), a.approvalSeq)
	answer := make(chan bool, 1)
	a.approvals[id] = answer
	return id, answer
}
func (a *Agent) removeApproval(id string) {
	a.approvalMu.Lock()
	delete(a.approvals, id)
	a.approvalMu.Unlock()
}
func (a *Agent) ResolveApproval(id string, approved bool) bool {
	a.approvalMu.Lock()
	answer, ok := a.approvals[id]
	if ok {
		delete(a.approvals, id)
	}
	a.approvalMu.Unlock()
	if !ok {
		return false
	}
	answer <- approved
	return true
}

func executeToolSafely(ctx context.Context, tool Tool, arguments map[string]any, update func(ToolResult)) (result ToolResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%s panicked: %v", tool.Name, recovered)
		}
	}()
	return tool.Execute(ctx, arguments, update)
}

func (a *Agent) tool(name string) (Tool, bool) {
	for _, tool := range a.config.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

func (a *Agent) appendMessage(message Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Messages = append(a.state.Messages, message)
}

func (a *Agent) setContextTokens(tokens int) {
	if tokens <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.ContextTokens = tokens
}

func (a *Agent) setUpstreamTransport(transport string) {
	if transport == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.UpstreamTransport = transport
}

func (a *Agent) addUsage(usage Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Usage.Input += usage.Input
	a.state.Usage.Output += usage.Output
	a.state.Usage.CacheRead += usage.CacheRead
	a.state.Usage.CacheWrite += usage.CacheWrite
	a.state.Usage.TotalTokens += usage.TotalTokens
}

func toolResultMessage(call ContentBlock, result ToolResult) Message {
	return Message{Role: RoleToolResult, ToolCallID: call.ID, ToolName: call.Name, Content: result.Content, IsError: result.IsError, Timestamp: time.Now().UnixMilli()}
}
