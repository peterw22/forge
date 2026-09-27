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

	"github.com/peterw22/pi-go/internal/agent"
)

// agyProvider handles agy's own internal turns. Never instantiate it without
// first establishing the MCP-only policy: agy runs its tools internally, not
// through agent.ProviderToolCall.
type agyProvider struct {
	binary        string
	policyReady   bool
	mu            sync.Mutex
	conversations map[string]agyConversation
}

type agyConversation struct{ ID, Model string }

func newAgyProvider(binary string) *agyProvider {
	return &agyProvider{binary: binary, conversations: make(map[string]agyConversation)}
}

func (p *agyProvider) CloseSession(id string) {
	p.mu.Lock()
	delete(p.conversations, id)
	p.mu.Unlock()
}
func (p *agyProvider) Stream(ctx context.Context, req agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
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

func (p *agyProvider) stream(ctx context.Context, req agent.Request, events chan<- agent.ProviderEvent) error {
	if req.SessionID == "" {
		return errors.New("agy requires a session ID for safe conversation isolation")
	}
	if !p.policyReady {
		return errors.New("agy MCP-only policy has not been verified")
	}
	home, err := agyHome()
	if err != nil {
		return err
	}
	if err := checkAgyPolicy(home, req.WorkingDirectory); err != nil {
		return err
	}
	if req.ToolGuard == nil {
		return errors.New("agy requires safety classifier")
	}
	if req.OnToolEvent == nil {
		return errors.New("agy requires tool event observer")
	}
	bridge, err := newProviderMCPTurnBridge(ctx, req, true, nil)
	if err != nil {
		return err
	}
	defer bridge.Close()
	model, ok := strings.CutPrefix(req.Model, "agy/")
	if !ok || !strings.HasPrefix(model, "gemini-") || !validClassifierModelID(model) || strings.Contains(model, "/") {
		return errors.New("invalid agy model")
	}
	p.mu.Lock()
	previous := p.conversations[req.SessionID]
	p.mu.Unlock()
	conversation := previous.ID
	if req.LoadProviderConversation != nil {
		id, err := req.LoadProviderConversation("agy", req.Model, req.WorkingDirectory)
		if err != nil {
			return err
		}
		conversation = id // persisted binding is authoritative over stale in-memory maps
	}
	if previous.Model != "" && previous.Model != req.Model {
		conversation = ""
	}
	// On a resumed CLI session the provider's own conversation is the only
	// source of prior model context. Never silently start a fresh provider
	// conversation when Pi Go already has an assistant reply on disk.
	priorAssistant := false
	for _, message := range req.Messages[:len(req.Messages)-1] {
		if message.Role == agent.RoleAssistant {
			priorAssistant = true
			break
		}
	}
	if priorAssistant && conversation == "" {
		return errors.New("agy conversation ID missing for an existing transcript; start a new session rather than silently dropping context")
	}
	args := []string{"--output-format", "stream-json", "--print-timeout", "0", "--disable-slash-commands", "--model", model}
	if conversation != "" {
		args = append(args, "--conversation", conversation)
	}
	// The adapter is only for text user turns; tool results must stay inside agy's
	// MCP turn, rather than being passed back as a new user message.
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != agent.RoleUser {
		return errors.New("agy requires a user turn")
	}
	var prompt strings.Builder
	for _, block := range req.Messages[len(req.Messages)-1].Content {
		if block.Type != "text" {
			return errors.New("agy does not support non-text input")
		}
		prompt.WriteString(block.Text)
	}
	args = append(args, "--print="+agyPromptWithToolInstruction(prompt.String(), conversation != ""))
	cmd := exec.CommandContext(ctx, p.binary, args...)
	cmd.Dir = req.WorkingDirectory
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd.Env = append(os.Environ(), "HOME="+home, agyMCPBinaryEnv+"="+executable, "PI_GO_AGY_MCP_ADDRESS="+bridge.address+"/call", "PI_GO_AGY_MCP_TOKEN="+bridge.token)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: 4096}
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("start agy: %w", err)
	}
	var completedID string
	parseErr := parseAgyStream(ctx, stdout, events, func(id string) { completedID = id })
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		p.CloseSession(req.SessionID)
		return fmt.Errorf("agy stream: %w; stderr: %s; exit: %v", parseErr, stderr.String(), waitErr)
	}
	if ctx.Err() != nil {
		p.CloseSession(req.SessionID)
		return ctx.Err()
	}
	if waitErr != nil {
		p.CloseSession(req.SessionID)
		return fmt.Errorf("agy exited: %w: %s", waitErr, stderr.String())
	}
	if req.SaveProviderConversation != nil {
		if err := req.SaveProviderConversation("agy", req.Model, req.WorkingDirectory, completedID); err != nil {
			p.CloseSession(req.SessionID)
			return err
		}
	}
	p.mu.Lock()
	p.conversations[req.SessionID] = agyConversation{ID: completedID, Model: req.Model}
	p.mu.Unlock()
	return nil
}

type limitedWriter struct {
	w         io.Writer
	remaining int
}

func (w *limitedWriter) Write(b []byte) (int, error) {
	n := len(b)
	if w.remaining > 0 {
		portion := len(b)
		if portion > w.remaining {
			portion = w.remaining
		}
		_, _ = w.w.Write(b[:portion])
		w.remaining -= portion
	}
	return n, nil
}

func parseAgyStream(ctx context.Context, input io.Reader, events chan<- agent.ProviderEvent, save func(string)) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLine)
	var answer strings.Builder
	var conversation string
	done := false
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if done {
			return errors.New("agy emitted events after result")
		}
		var line struct {
			Event          string `json:"event"`
			ConversationID string `json:"conversation_id"`
			Step           struct {
				StepType      string `json:"step_type"`
				TextDelta     string `json:"text_delta"`
				ThinkingDelta string `json:"thinking_delta"`
			} `json:"step_update"`
			Result struct {
				ConversationID string `json:"conversation_id"`
				Status         string `json:"status"`
				Response       string `json:"response"`
				Usage          struct {
					Input     int `json:"input_tokens"`
					Output    int `json:"output_tokens"`
					CacheRead int `json:"cache_read_tokens"`
					Total     int `json:"total_tokens"`
				} `json:"usage"`
			} `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return fmt.Errorf("invalid agy event: %w", err)
		}
		switch line.Event {
		case "init":
			conversation = line.ConversationID
		case "step_update":
			if line.Step.StepType == "agent_thinking" || line.Step.StepType == "thinking" {
				// Only forward actual text supplied as thinking by agy. Its
				// thinking_tokens usage metric is not a readable summary.
				delta := line.Step.ThinkingDelta
				if delta == "" {
					delta = line.Step.TextDelta
				}
				if delta != "" {
					events <- agent.ProviderEvent{Type: agent.ProviderThinkingDelta, Delta: delta}
				}
			}
			if line.Step.StepType == "agent_response" && line.Step.TextDelta != "" {
				answer.WriteString(line.Step.TextDelta)
				events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: line.Step.TextDelta}
			}
		case "result":
			if done {
				return errors.New("duplicate agy result")
			}
			done = true
			if line.Result.Status != "SUCCESS" {
				return fmt.Errorf("agy result status: %s", line.Result.Status)
			}
			if line.Result.Response == "" && answer.Len() == 0 {
				return errors.New("agy returned an empty response (possibly timed out)")
			}
			if line.Result.ConversationID == "" || (conversation != "" && line.Result.ConversationID != conversation) {
				return errors.New("agy conversation ID mismatch")
			}
			// Some agy versions emit complete answers only in result. Do not append
			// duplicate text if step_update already supplied the answer.
			if answer.Len() == 0 && line.Result.Response != "" {
				events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: line.Result.Response}
			}
			if answer.Len() > 0 && answer.String() != line.Result.Response {
				return errors.New("agy response differs from streamed answer")
			}
			save(line.Result.ConversationID)
			u := line.Result.Usage
			events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "stop", Usage: agent.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, TotalTokens: u.Total}}
		default:
			return fmt.Errorf("unknown agy event %q", line.Event)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !done {
		return errors.New("agy stream ended without result")
	}
	return nil
}
