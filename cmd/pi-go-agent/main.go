// pi-go-agent is the first runnable Go agent-core backend. It owns the agent
// loop and built-in tools; it currently uses the existing Go Codex bridge as a
// provider subprocess while that transport is being extracted into internal/providers.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/peterw22/pi-go/internal/agent"
	systemprompt "github.com/peterw22/pi-go/internal/prompt"
	"github.com/peterw22/pi-go/internal/session"
)

type codexProvider struct {
	binary string
	token  string
}

func main() {
	var prompt, model, thinking, systemPrompt, cwd string
	var serve bool
	var sessionPath string
	var resume bool
	var listenAddress string
	var allowRemote bool
	flag.StringVar(&prompt, "prompt", "", "prompt to run")
	flag.BoolVar(&serve, "serve", false, "run the JSONL agent-backend server")
	flag.StringVar(&sessionPath, "session", "", "session JSONL path")
	flag.BoolVar(&resume, "resume", false, "restore the session JSONL path")
	flag.StringVar(&listenAddress, "listen", "", "serve JSONL over unix:///path, tcp://host:port, or ws://host:port/path")
	flag.BoolVar(&allowRemote, "allow-remote", false, "allow binding TCP/WebSocket to a non-loopback address (unsafe without a trusted network)")
	flag.StringVar(&model, "model", "gpt-5.6-terra", "Codex model ID")
	flag.StringVar(&thinking, "thinking", "high", "thinking level: off, minimal, low, medium, high, xhigh, max")
	flag.StringVar(&systemPrompt, "system-prompt", "", "system prompt (defaults to the Pi Go coding-agent prompt)")
	flag.StringVar(&cwd, "cwd", "", "working directory for built-in tools")
	flag.Parse()
	if prompt == "" && !serve && listenAddress == "" {
		fmt.Fprintln(os.Stderr, "--prompt must not be empty unless --serve is used")
		os.Exit(2)
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	customSystemPrompt := strings.TrimSpace(systemPrompt) != ""
	if !customSystemPrompt {
		systemPrompt = systemprompt.Default(cwd)
	}
	token, err := codexToken(model)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	provider := codexProvider{binary: codexBinary(), token: token}
	allowApproval := serve || listenAddress != ""
	factory := func(workspace, selectedModel, selectedThinking string, messages []agent.Message, usage agent.Usage) (*agent.Agent, error) {
		if selectedModel == "" {
			selectedModel = model
		}
		if selectedThinking == "" {
			selectedThinking = thinking
		}
		promptForWorkspace := systemprompt.Default(workspace)
		if customSystemPrompt {
			promptForWorkspace = systemPrompt
		}
		core, createErr := agent.New(agent.Config{
			Model: selectedModel, Thinking: selectedThinking,
			SystemPrompt: promptForWorkspace, WorkingDirectory: workspace,
			Provider: provider, Tools: builtInTools(workspace), ParallelTools: true,
			ToolGuard: newSafetyGate(provider), AllowApproval: allowApproval,
		})
		if createErr == nil && (len(messages) > 0 || usage.TotalTokens > 0) {
			core.Restore(messages, selectedModel, selectedThinking, usage)
		}
		return core, createErr
	}
	if serve || listenAddress != "" {
		var store *session.Store
		var messages []agent.Message
		var usage agent.Usage
		workspace, selectedModel, selectedThinking := cwd, model, thinking
		selectedYOLO := false
		if resume && sessionPath != "" {
			var header session.Header
			store, header, messages, usage, err = session.Resume(sessionPath)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			if header.CWD != "" {
				workspace = header.CWD
			}
			if header.Model != "" {
				selectedModel = header.Model
			}
			if header.Thinking != "" {
				selectedThinking = header.Thinking
			}
			selectedYOLO = header.YOLO
		} else {
			store, err = session.New(cwd, model, thinking)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		core, err := factory(workspace, selectedModel, selectedThinking, messages, usage)
		if err != nil {
			_ = store.Close()
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		core.SetYOLO(selectedYOLO)
		initial := newSessionRuntime(store.ID(), core, session.NewController(cwd, store))
		registry := newRuntimeRegistry(cwd, initial, factory)
		defer registry.Shutdown()
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(signals)
		go func() {
			select {
			case <-signals:
				registry.Shutdown()
				if listenAddress == "" {
					_ = os.Stdin.Close()
				}
			case <-registry.done:
			}
		}()
		if listenAddress != "" {
			if strings.HasPrefix(listenAddress, "ws://") {
				err = serveWebSocket(registry, listenAddress, allowRemote)
			} else {
				err = serveSocket(registry, listenAddress, allowRemote)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		} else if err := serveClient(registry, os.Stdin, os.Stdout, nil); err != nil && !errors.Is(err, errBackendShutdown) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	core, err := factory(cwd, model, thinking, nil, agent.Usage{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	if err := core.Run(context.Background(), prompt, func(event agent.Event) { _ = encoder.Encode(event) }); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// codexToken uses an explicitly supplied token when present. Otherwise it
// delegates OAuth refresh to the installed Pi CLI, keeping credentials out of
// this agent's flags, files, and JSONL output.
type backendCommand struct {
	ID                 string               `json:"id,omitempty"`
	Type               string               `json:"type"`
	Message            string               `json:"message,omitempty"`
	Level              string               `json:"level,omitempty"`
	Model              string               `json:"model,omitempty"`
	Name               *string              `json:"name"`
	Session            string               `json:"session,omitempty"`
	CWD                string               `json:"cwd,omitempty"`
	Content            []agent.ContentBlock `json:"content,omitempty"`
	CustomInstructions string               `json:"customInstructions,omitempty"`
	ApprovalID         string               `json:"approvalId,omitempty"`
	Approved           bool                 `json:"approved,omitempty"`
	Enabled            bool                 `json:"enabled"`
}

type backendResponse struct {
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type"`
	Command  string          `json:"command,omitempty"`
	Success  bool            `json:"success,omitempty"`
	Active   bool            `json:"active,omitempty"`
	Event    *agent.Event    `json:"event,omitempty"`
	Error    string          `json:"error,omitempty"`
	State    *agent.State    `json:"state,omitempty"`
	Sessions []session.Entry `json:"sessions,omitempty"`
	Model    string          `json:"model,omitempty"`
	Thinking string          `json:"thinking,omitempty"`
	Session  string          `json:"session,omitempty"`
	CWD      string          `json:"cwd,omitempty"`
	YOLO     *bool           `json:"yolo,omitempty"`
}

var errBackendShutdown = errors.New("agent backend shutdown")

func serveSocket(registry *runtimeRegistry, endpoint string, allowRemote bool) error {
	network, address, cleanup, err := socketEndpoint(endpoint, allowRemote)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return fmt.Errorf("listen %s: %w", endpoint, err)
	}
	defer listener.Close()
	if network == "unix" {
		if err := os.Chmod(address, 0600); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stderr, "pi-go-agent listening on", listener.Addr())
	go func() { <-registry.done; _ = listener.Close() }()
	var clients sync.WaitGroup
	defer clients.Wait()
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			select {
			case <-registry.done:
				return nil
			default:
				return acceptErr
			}
		}
		clients.Add(1)
		go func(connection net.Conn) {
			defer clients.Done()
			err := serveClient(registry, connection, connection, connection)
			if err != nil && !errors.Is(err, errBackendShutdown) {
				fmt.Fprintln(os.Stderr, "agent client disconnected:", err)
			}
		}(connection)
	}
}

func socketEndpoint(endpoint string, allowRemote bool) (network, address string, cleanup func(), err error) {
	switch {
	case strings.HasPrefix(endpoint, "unix://"):
		address = strings.TrimPrefix(endpoint, "unix://")
		if address == "" {
			return "", "", nil, errors.New("unix socket path is required")
		}
		if err = os.MkdirAll(filepath.Dir(address), 0700); err != nil {
			return "", "", nil, err
		}
		if err = os.Remove(address); err != nil && !os.IsNotExist(err) {
			return "", "", nil, err
		}
		return "unix", address, func() { _ = os.Remove(address) }, nil
	case strings.HasPrefix(endpoint, "tcp://"):
		address = strings.TrimPrefix(endpoint, "tcp://")
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return "", "", nil, fmt.Errorf("invalid TCP address: %w", splitErr)
		}
		ip := net.ParseIP(host)
		if !allowRemote && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", "", nil, errors.New("TCP listener must use a loopback address unless --allow-remote is set")
		}
		return "tcp", address, nil, nil
	default:
		return "", "", nil, errors.New("listen address must start with tcp:// or unix://")
	}
}

// codexToken uses an explicitly supplied token when present. Otherwise it
// delegates OAuth refresh to the installed Pi CLI, keeping credentials out of
// this agent's flags, files, and JSONL output.
func promptContent(message string, attachments []agent.ContentBlock) ([]agent.ContentBlock, error) {
	var content []agent.ContentBlock
	if strings.TrimSpace(message) != "" {
		content = append(content, agent.ContentBlock{Type: "text", Text: message})
	}
	if len(attachments) > 4 {
		return nil, errors.New("at most 4 images may be attached")
	}
	const maxImageBytes = 10 << 20
	total := 0
	for _, attachment := range attachments {
		if attachment.Type != "image" || attachment.Data == "" {
			return nil, errors.New("only non-empty image attachments are supported")
		}
		switch attachment.MIMEType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return nil, fmt.Errorf("unsupported image type: %s", attachment.MIMEType)
		}
		decoded, err := base64.StdEncoding.DecodeString(attachment.Data)
		if err != nil {
			return nil, errors.New("image attachment is not valid base64")
		}
		total += len(decoded)
		if total > maxImageBytes {
			return nil, fmt.Errorf("image attachments exceed %d MiB total limit", maxImageBytes>>20)
		}
		content = append(content, agent.ContentBlock{Type: "image", Data: attachment.Data, MIMEType: attachment.MIMEType})
	}
	if len(content) == 0 {
		return nil, errors.New("message or image attachment is required")
	}
	return content, nil
}

func validWorkingDirectory(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("working directory must not be empty")
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory is not a directory: %s", path)
	}
	return path, nil
}

func validThinking(level string) bool {
	for _, candidate := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if level == candidate {
			return true
		}
	}
	return false
}

func codexToken(model string) (string, error) {
	if token := strings.TrimSpace(os.Getenv("PI_GO_CODEX_TOKEN")); token != "" {
		return token, nil
	}
	command := exec.Command("pi", "auth", "print-bearer-token", "--provider", "openai-codex", "--model", model, "--min-expiry", "5m")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("get Codex credential from Pi CLI: %w", err)
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New("Pi CLI returned an empty Codex credential")
	}
	return token, nil
}

func codexBinary() string {
	if path := os.Getenv("PI_GO_CODEX_BIN"); path != "" {
		return path
	}
	if executable, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(executable), "pi-go-codex")
	}
	return "pi-go-codex"
}

func (p codexProvider) Stream(ctx context.Context, request agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
	events := make(chan agent.ProviderEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		cmd := exec.CommandContext(ctx, p.binary, "--provider")
		cmd.Env = append(os.Environ(), "PI_GO_CODEX_TOKEN="+p.token)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			errs <- err
			return
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errs <- err
			return
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			errs <- err
			return
		}
		if err := json.NewEncoder(stdin).Encode(providerRequest(request)); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			errs <- err
			return
		}
		if err := consumeBridge(stdout, stdin, events); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			errs <- err
			return
		}
		if err := cmd.Wait(); err != nil {
			if ctx.Err() != nil {
				errs <- ctx.Err()
			} else {
				errs <- fmt.Errorf("Codex bridge exited: %w: %s", err, strings.TrimSpace(stderr.String()))
			}
		}
	}()
	return events, errs
}

func providerRequest(request agent.Request) map[string]any {
	messages := make([]map[string]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		messages = append(messages, map[string]any{
			"role": string(message.Role), "content": message.Content, "toolCallId": message.ToolCallID,
			"toolName": message.ToolName, "isError": message.IsError,
		})
	}
	tools := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
	}
	return map[string]any{"model": request.Model, "systemPrompt": request.SystemPrompt, "messages": messages, "tools": tools, "reasoning": request.Thinking}
}

func consumeBridge(output io.Reader, input io.WriteCloser, events chan<- agent.ProviderEvent) error {
	defer input.Close()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type      string         `json:"type"`
			Payload   any            `json:"payload"`
			Delta     string         `json:"delta"`
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
			Reason    string         `json:"reason"`
			Message   string         `json:"message"`
			Transport string         `json:"transport"`
			Usage     struct {
				Input        int `json:"input_tokens"`
				Output       int `json:"output_tokens"`
				Total        int `json:"total_tokens"`
				InputDetails struct {
					Cached int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("decode Codex bridge event: %w", err)
		}
		switch event.Type {
		case "payload":
			if err := json.NewEncoder(input).Encode(map[string]any{"type": "payload", "payload": event.Payload}); err != nil {
				return err
			}
		case "start":
			if event.Transport != "" {
				events <- agent.ProviderEvent{Type: agent.ProviderTransport, Transport: event.Transport}
			}
		case "thinking_delta":
			events <- agent.ProviderEvent{Type: agent.ProviderThinkingDelta, Delta: event.Delta}
		case "text_delta":
			events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: event.Delta}
		case "toolcall_end":
			events <- agent.ProviderEvent{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Type: "toolCall", ID: event.ID, Name: event.Name, Arguments: event.Arguments}}
		case "done":
			events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: event.Reason, Usage: agent.Usage{Input: event.Usage.Input, Output: event.Usage.Output, CacheRead: event.Usage.InputDetails.Cached, TotalTokens: event.Usage.Total}}
		case "error":
			return errors.New(event.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}
