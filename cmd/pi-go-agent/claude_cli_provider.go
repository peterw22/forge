package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/peterw22/forge/internal/agent"
)

// claudeCLIProvider runs Claude Code with no built-in tools, no inherited MCP
// servers or settings, and only the authenticated Pi Go MCP server. A tool
// call and its result finish inside the same Claude turn.
type claudeCLIProvider struct {
	binary        string
	mu            sync.Mutex
	conversations map[string]agyConversation
}

func newClaudeCLIProvider(binary string) *claudeCLIProvider {
	return &claudeCLIProvider{binary: binary, conversations: make(map[string]agyConversation)}
}
func (p *claudeCLIProvider) CloseSession(id string) {
	p.mu.Lock()
	delete(p.conversations, id)
	p.mu.Unlock()
}
func (p *claudeCLIProvider) Stream(ctx context.Context, req agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
	events := make(chan agent.ProviderEvent, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		if err := p.stream(ctx, req, events); err != nil {
			errs <- err
		}
	}()
	return events, errs
}
func (p *claudeCLIProvider) stream(ctx context.Context, req agent.Request, events chan<- agent.ProviderEvent) error {
	if req.SessionID == "" || req.ToolGuard == nil || req.OnToolEvent == nil {
		return errors.New("Claude MCP turn requires a session, classifier, and tool event observer")
	}
	model, ok := strings.CutPrefix(req.Model, "claude/")
	if !ok || !claudeModelAllowed(model) {
		return errors.New("unsupported Claude model")
	}
	if err := claudeModelEffortError(model, req.Thinking); err != nil {
		return err
	}
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != agent.RoleUser {
		return errors.New("Claude requires a user turn")
	}
	if req.WorkingDirectory == "" {
		return errors.New("Claude requires a working directory")
	}
	if info, err := os.Stat(req.WorkingDirectory); err != nil || !info.IsDir() {
		return errors.New("Claude working directory is invalid")
	}
	prompt, err := claudeCLIUserInput(req.Messages[len(req.Messages)-1].Content)
	if err != nil {
		return err
	}
	var verified atomic.Bool
	bridge, err := newProviderMCPTurnBridge(ctx, req, false, verified.Load)
	if err != nil {
		return err
	}
	defer bridge.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"pi-go-agent": map[string]any{"command": executable, "args": []string{"--agy-mcp-stdio"}, "env": map[string]string{"PI_GO_AGY_MCP_ADDRESS": bridge.address + "/call", "PI_GO_AGY_MCP_TOKEN": bridge.token}}}})
	if err != nil {
		return err
	}
	p.mu.Lock()
	previous := p.conversations[req.SessionID]
	p.mu.Unlock()
	conversation := previous.ID
	if req.LoadProviderConversation != nil {
		id, err := req.LoadProviderConversation("claude", req.Model, req.WorkingDirectory)
		if err != nil {
			return err
		}
		conversation = id // persisted binding is authoritative over stale in-memory maps
	}
	priorAssistant := false
	for _, message := range req.Messages[:len(req.Messages)-1] {
		if message.Role == agent.RoleAssistant {
			priorAssistant = true
			break
		}
	}
	if priorAssistant && conversation == "" {
		return errors.New("Claude conversation ID missing for an existing transcript; start a new session rather than silently dropping context")
	}
	args := []string{"--print", "--model", model, "--effort", req.Thinking, "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--tools", "", "--restricted", "--strict-mcp-config", "--mcp-config", string(config), "--setting-sources", "", "--permission-mode", "dontAsk", "--allowedTools", "mcp__pi-go-agent__*", "--append-system-prompt", mcpOnlyToolInstruction, "--disable-slash-commands"}
	if conversation != "" && (previous.Model == "" || previous.Model == req.Model) {
		args = append(args, "--resume", conversation)
	}
	args = append(args, "--input-format", "stream-json")
	cmd := exec.CommandContext(ctx, p.binary, args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Dir = req.WorkingDirectory
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: 4096}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Claude: %w", err)
	}
	// Persist the conversation as soon as Claude reports it. Tool calls and
	// commentary are saved while the turn runs, so a turn that is aborted or
	// fails afterwards must still leave a resumable binding.
	var completedID string
	var saveErr error
	save := func(id string) {
		if id == completedID {
			return
		}
		completedID = id
		if req.SaveProviderConversation != nil && saveErr == nil {
			saveErr = req.SaveProviderConversation("claude", req.Model, req.WorkingDirectory, id)
		}
	}
	parseErr := parseClaudeCLIStream(ctx, stdout, events, save, verified.Store)
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		p.CloseSession(req.SessionID)
		return fmt.Errorf("Claude stream: %w; stderr: %s; exit: %v", parseErr, stderr.String(), waitErr)
	}
	if err := ctx.Err(); err != nil {
		p.CloseSession(req.SessionID)
		return err
	}
	if waitErr != nil {
		p.CloseSession(req.SessionID)
		return fmt.Errorf("Claude exited: %w: %s", waitErr, stderr.String())
	}
	if saveErr != nil {
		p.CloseSession(req.SessionID)
		return saveErr
	}
	p.mu.Lock()
	p.conversations[req.SessionID] = agyConversation{ID: completedID, Model: req.Model}
	p.mu.Unlock()
	return nil
}
func claudeEffortAllowed(level string) bool {
	switch level {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

// maxStreamLine bounds one JSON line from a CLI provider or its MCP child.
// Claude echoes each tool result, including base64 images, as a single
// stream-json line, and write arguments can be large. bufio.Scanner grows its
// buffer only as needed, so ordinary lines stay small.
const maxStreamLine = 256 << 20

// claudeUsage is Anthropic usage, whose input_tokens exclude cache reads and
// writes. A result's usage sums every API call of the turn.
type claudeUsage struct {
	Input      int `json:"input_tokens"`
	Output     int `json:"output_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
}

// agentUsage uses the OpenAI convention of the other providers: input
// includes cached and cache-written tokens.
func (u claudeUsage) agentUsage() agent.Usage {
	input := u.Input + u.CacheRead + u.CacheWrite
	return agent.Usage{Input: input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, TotalTokens: input + u.Output}
}

func parseClaudeCLIStream(ctx context.Context, reader io.Reader, events chan<- agent.ProviderEvent, save func(string), onVerified ...func(bool)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLine)
	var session string
	var text strings.Builder
	var finalSegment strings.Builder
	// Claude emits several assistant messages around MCP calls in one turn.
	// Keep their display boundaries without changing the raw result comparison.
	pendingBoundary := false
	emitText := func(delta string) {
		if pendingBoundary && text.Len() > 0 {
			separator := "\n\n"
			if strings.HasSuffix(text.String(), "\n\n") {
				separator = ""
			} else if strings.HasSuffix(text.String(), "\n") {
				separator = "\n"
			}
			delta = separator + delta
		}
		pendingBoundary = false
		text.WriteString(delta)
		events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: delta}
	}
	// The context after the turn is the last API call's prompt plus output,
	// not the turn-wide sum in the result.
	var lastCall claudeUsage
	initialized, done := false, false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if done {
			return errors.New("Claude emitted events after result")
		}
		var msg struct {
			Type       string   `json:"type"`
			Subtype    string   `json:"subtype"`
			SessionID  string   `json:"session_id"`
			Tools      []string `json:"tools"`
			MCPServers []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"mcp_servers"`
			Event struct {
				Type         string `json:"type"`
				ContentBlock struct {
					Type string `json:"type"`
				} `json:"content_block"`
				Delta struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
				Message struct {
					Usage claudeUsage `json:"usage"`
				} `json:"message"`
				Usage *claudeUsage `json:"usage"`
			} `json:"event"`
			Result            string            `json:"result"`
			IsError           bool              `json:"is_error"`
			PermissionDenials []json.RawMessage `json:"permission_denials"`
			Usage             claudeUsage       `json:"usage"`
			ModelUsage        map[string]struct {
				ContextWindow int `json:"contextWindow"`
			} `json:"modelUsage"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return fmt.Errorf("decode Claude event: %w", err)
		}
		switch msg.Type {
		case "system":
			if msg.Subtype == "init" {
				if initialized || msg.SessionID == "" || len(msg.Tools) != 4 || len(msg.MCPServers) != 1 || msg.MCPServers[0].Name != "pi-go-agent" || msg.MCPServers[0].Status != "connected" {
					return errors.New("Claude MCP-only tool boundary was not established")
				}
				expected := map[string]bool{"mcp__pi-go-agent__read": true, "mcp__pi-go-agent__write": true, "mcp__pi-go-agent__replace": true, "mcp__pi-go-agent__bash": true}
				for _, name := range msg.Tools {
					if !expected[name] {
						return errors.New("Claude exposed unexpected tool: " + name)
					}
					delete(expected, name)
				}
				if len(expected) != 0 {
					return errors.New("Claude did not expose the expected MCP tools")
				}
				initialized = true
				session = msg.SessionID
				save(session)
				if len(onVerified) > 0 && onVerified[0] != nil {
					onVerified[0](true)
				}
			} else if msg.Subtype == "permission_denied" {
				return errors.New("Claude denied an MCP tool call")
			}
		case "stream_event":
			if !initialized {
				return errors.New("Claude streamed before tool boundary verification")
			}
			if msg.Event.Type == "message_start" {
				lastCall = msg.Event.Message.Usage
			}
			if msg.Event.Type == "message_delta" && msg.Event.Usage != nil {
				lastCall = *msg.Event.Usage
			}
			if msg.Event.Type == "message_start" || (msg.Event.Type == "content_block_start" && msg.Event.ContentBlock.Type == "tool_use") {
				finalSegment.Reset()
				pendingBoundary = true
			}
			if msg.Event.Type == "content_block_delta" && msg.Event.Delta.Type == "thinking_delta" && msg.Event.Delta.Thinking != "" {
				events <- agent.ProviderEvent{Type: agent.ProviderThinkingDelta, Delta: msg.Event.Delta.Thinking}
			}
			if msg.Event.Type == "content_block_delta" && msg.Event.Delta.Type == "text_delta" && msg.Event.Delta.Text != "" {
				finalSegment.WriteString(msg.Event.Delta.Text)
				emitText(msg.Event.Delta.Text)
			}
		case "result":
			if !initialized || msg.SessionID != session || msg.IsError || msg.Subtype != "success" || len(msg.PermissionDenials) > 0 {
				return errors.New("Claude turn failed or permission was denied")
			}
			// Claude's result contains only the final answer, not commentary
			// streamed before an MCP tool. Compare just the post-tool segment.
			if finalSegment.Len() > 0 && !strings.HasPrefix(msg.Result, finalSegment.String()) {
				return errors.New("Claude result differs from final streamed text")
			}
			if finalSegment.Len() == 0 && msg.Result == "" && text.Len() == 0 {
				return errors.New("Claude returned no response")
			}
			if suffix := strings.TrimPrefix(msg.Result, finalSegment.String()); suffix != "" {
				emitText(suffix)
			}
			done = true
			save(session)
			usage := msg.Usage.agentUsage()
			usage.ContextTokens = lastCall.agentUsage().TotalTokens
			for _, model := range msg.ModelUsage {
				usage.ContextWindow = max(usage.ContextWindow, model.ContextWindow)
			}
			events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "stop", Usage: usage}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !done {
		return errors.New("Claude ended without a result")
	}
	return nil
}
