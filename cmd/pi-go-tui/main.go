// pi-go-tui is a dependency-free (apart from terminal raw-mode support) Go
// terminal frontend for pi-go-agent. It speaks only the agent-backend JSONL
// protocol; no TypeScript Pi runtime is involved.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/peterw22/pi-go/internal/session"
	"github.com/peterw22/pi-go/internal/tui"
	"golang.org/x/term"
)

const (
	reset  = "\x1b[0m"
	cyan   = "\x1b[36m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	red    = "\x1b[31m"
	dim    = "\x1b[2m"
)

var errInterrupt = errors.New("terminal interrupt")

var models = []string{"gpt-5.3-codex-spark", "gpt-5.5", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra"}
var commands = []string{"/abort", "/clear", "/compact", "/cwd", "/exit", "/model", "/name", "/new", "/quit", "/sessions", "/thinking"}

type backendCommand struct {
	ID                 string  `json:"id,omitempty"`
	Type               string  `json:"type"`
	Message            string  `json:"message,omitempty"`
	Level              string  `json:"level,omitempty"`
	Model              string  `json:"model,omitempty"`
	Name               *string `json:"name"`
	Session            string  `json:"session,omitempty"`
	CWD                string  `json:"cwd,omitempty"`
	CustomInstructions string  `json:"customInstructions,omitempty"`
	ApprovalID         string  `json:"approvalId,omitempty"`
	Approved           bool    `json:"approved,omitempty"`
}

type contentBlock struct {
	Type, Text, ID, Name, Data, MIMEType string
	Arguments                            map[string]any `json:"arguments,omitempty"`
}
type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}
type message struct {
	Role       string         `json:"role"`
	Content    []contentBlock `json:"content"`
	ToolName   string         `json:"toolName,omitempty"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	IsError    bool           `json:"isError,omitempty"`
}
type agentEvent struct {
	Type          string         `json:"type"`
	Message       *message       `json:"message,omitempty"`
	ToolName      string         `json:"toolName,omitempty"`
	ToolCallID    string         `json:"toolCallId,omitempty"`
	Result        *toolResult    `json:"result,omitempty"`
	Arguments     map[string]any `json:"arguments,omitempty"`
	IsError       bool           `json:"isError,omitempty"`
	Error         string         `json:"error,omitempty"`
	ApprovalID    string         `json:"approvalId,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Description   string         `json:"description,omitempty"`
	SafetyStatus  string         `json:"safetyStatus,omitempty"`
	SafetyMessage string         `json:"safetyMessage,omitempty"`
	Usage         struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		CacheRead int `json:"cacheRead"`
		Total     int `json:"totalTokens"`
	} `json:"usage,omitempty"`
}
type backendState struct {
	Messages      []message `json:"messages"`
	Streaming     bool      `json:"streaming"`
	ContextTokens int       `json:"contextTokens"`
	Usage         struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		CacheRead int `json:"cacheRead"`
		Total     int `json:"totalTokens"`
	} `json:"usage"`
}
type backendResponse struct {
	ID, Type, Command, Error, Model, Thinking, Session, CWD string
	Success                                                 bool
	Event                                                   *agentEvent     `json:"event,omitempty"`
	State                                                   *backendState   `json:"state,omitempty"`
	Sessions                                                []session.Entry `json:"sessions,omitempty"`
}

type app struct {
	model, thinking, cwd, binary string
	process                      *exec.Cmd
	stdin                        io.WriteCloser
	writeMu, printMu, turnMu     sync.Mutex
	turn                         int
	streaming                    bool
	turnDone                     chan struct{}
	lastText                     string
	lineScanner                  *bufio.Scanner
	program                      *tea.Program
	resumeID, sessionPath        string
	toolOutput                   map[string]string
}

func main() {
	var model, thinking, cwd, agentBinary string
	var rich bool
	var resumeID string
	flag.StringVar(&model, "model", "gpt-5.6-terra", "Codex model ID")
	flag.StringVar(&thinking, "thinking", "high", "thinking level")
	flag.StringVar(&cwd, "cwd", "", "working directory")
	flag.StringVar(&agentBinary, "agent-bin", "", "path to pi-go-agent")
	flag.BoolVar(&rich, "rich", true, "use rich alternate-screen UI")
	flag.StringVar(&resumeID, "r", "", "resume session UUIDv7, latest, or select when no value is given")
	if err := flag.CommandLine.Parse(normalizeResumeArgs(os.Args[1:])); err != nil {
		os.Exit(2)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if agentBinary == "" {
		agentBinary = defaultAgentBinary()
	}
	if resumeID == resumeSelector {
		selected, ok, err := selectSession(cwd)
		if err != nil {
			fmt.Fprintln(os.Stderr, "resume session:", err)
			os.Exit(1)
		}
		if !ok {
			return
		}
		resumeID = selected
	}
	ui := &app{model: model, thinking: thinking, cwd: cwd, binary: agentBinary, resumeID: resumeID, lineScanner: bufio.NewScanner(os.Stdin), toolOutput: make(map[string]string)}
	if resumeID != "" {
		path, err := session.Resolve(cwd, resumeID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "resume session:", err)
			os.Exit(1)
		}
		ui.sessionPath = path
	}
	ui.lineScanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if rich {
		ui.runRich()
		return
	}
	if err := ui.start(); err != nil {
		fmt.Fprintln(os.Stderr, red+"Failed to start Go agent: "+err.Error()+reset)
		os.Exit(1)
	}
	defer ui.stop()
	ui.banner()
	ui.runInput()
}

func defaultAgentBinary() string {
	if value := os.Getenv("PI_GO_AGENT_BIN"); value != "" {
		return value
	}
	if executable, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(executable), "pi-go-agent")
	}
	return "pi-go-agent"
}

func (a *app) start() error {
	args := []string{"--serve", "--model", a.model, "--thinking", a.thinking, "--cwd", a.cwd}
	if a.sessionPath != "" {
		args = append(args, "--resume", "--session", a.sessionPath)
	}
	command := exec.Command(a.binary, args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return err
	}
	a.process, a.stdin = command, stdin
	go a.readEvents(stdout)
	go func() {
		if err := command.Wait(); err != nil {
			a.printStatus("agent stopped: " + err.Error())
			a.finishTurn()
		}
	}()
	return nil
}

func (a *app) stop() {
	_ = a.send(backendCommand{ID: "shutdown", Type: "shutdown"})
	if a.process != nil && a.process.Process != nil {
		_ = a.process.Process.Kill()
	}
}
func (a *app) send(command backendCommand) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if a.stdin == nil {
		return errors.New("agent is not running")
	}
	return json.NewEncoder(a.stdin).Encode(command)
}
func (a *app) banner() {
	a.printMu.Lock()
	defer a.printMu.Unlock()
	fmt.Printf("%sPi Go%s  %s%s%s\n", cyan, reset, dim, a.model, reset)
	fmt.Printf("%sEnter a prompt. Commands: /abort, /model <id>, /clear, /exit. Tab completes commands and models.%s\n\n", dim, reset)
}

func (a *app) runInput() {
	termination := make(chan os.Signal, 1)
	signal.Notify(termination, syscall.SIGTERM)
	go func() { <-termination; a.stop(); os.Exit(0) }()
	for {
		line, err := a.readLine()
		if errors.Is(err, errInterrupt) {
			if a.isStreaming() {
				a.printStatus("aborting current turn")
				_ = a.send(backendCommand{ID: "abort", Type: "abort"})
				<-a.currentTurnDone()
			}
			continue
		}
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if a.command(line) {
				return
			}
			continue
		}
		if a.isStreaming() {
			a.printStatus("agent is busy; use /abort first")
			continue
		}
		done := a.beginTurn()
		a.turn++
		a.lastText = ""
		if err := a.send(backendCommand{ID: fmt.Sprintf("turn-%d", a.turn), Type: "prompt", Message: line}); err != nil {
			a.printStatus(err.Error())
			a.finishTurn()
		}
		<-done // Prompt is rendered only after agent_end, not while it is streaming.
	}
}

func (a *app) beginTurn() chan struct{} {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	a.streaming = true
	a.turnDone = make(chan struct{})
	return a.turnDone
}
func (a *app) isStreaming() bool { a.turnMu.Lock(); defer a.turnMu.Unlock(); return a.streaming }
func (a *app) currentTurnDone() chan struct{} {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	return a.turnDone
}
func (a *app) finishTurn() {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if a.streaming {
		a.streaming = false
		close(a.turnDone)
	}
}

func (a *app) command(line string) bool {
	parts := strings.Fields(line)
	switch parts[0] {
	case "/exit", "/quit":
		return true
	case "/abort":
		_ = a.send(backendCommand{ID: "abort", Type: "abort"})
	case "/compact":
		if a.isStreaming() {
			a.printStatus("agent is already running")
			return false
		}
		done := a.beginTurn()
		instructions := strings.TrimSpace(strings.TrimPrefix(line, "/compact"))
		if err := a.send(backendCommand{ID: "compact", Type: "compact", CustomInstructions: instructions}); err != nil {
			a.printStatus(err.Error())
			a.finishTurn()
		}
		<-done
	case "/clear":
		a.printMu.Lock()
		fmt.Print("\x1b[2J\x1b[H")
		a.printMu.Unlock()
		a.banner()
	case "/model":
		if len(parts) != 2 {
			a.printStatus("usage: /model <model-id>")
			return false
		}
		if a.isStreaming() {
			a.printStatus("abort the current turn before changing model")
			return false
		}
		a.stop()
		a.model = parts[1]
		if err := a.start(); err != nil {
			a.printStatus("failed to change model: " + err.Error())
		} else {
			a.printStatus("model changed to " + a.model + " (new session)")
		}
	default:
		a.printStatus("unknown command: " + parts[0])
	}
	return false
}

func (a *app) readEvents(output io.Reader) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var response backendResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			a.printStatus("invalid backend event: " + err.Error())
			continue
		}
		if response.Type == "error" || response.Error != "" {
			a.printStatus(response.Error)
			a.finishTurn()
			continue
		}
		if a.program != nil && response.Command == "list_sessions" {
			choices := []tui.SessionChoice{{ID: "__new_session__", Name: "＋ New session", Preview: "Start a blank session with the current model and thinking level"}}
			for _, entry := range response.Sessions {
				choices = append(choices, tui.SessionChoice{ID: entry.ID, Name: entry.LastMessageTime.Local().Format("2006-01-02 15:04"), Preview: entry.Summary, LastMessageTime: entry.LastMessageTime})
			}
			a.program.Send(tui.ShowSessions{Sessions: choices})
		}
		if a.program != nil && response.State != nil {
			a.restoreRichState(response)
		}
		if a.program != nil && response.Command == "set_cwd" && response.Success {
			a.cwd = response.CWD
			a.program.Send(tui.SetStatus{Value: "workspace: " + response.CWD})
		}
		if response.Event != nil {
			if response.Event.Type == "approval_required" && a.program == nil {
				a.printStatus("Bash Safety blocked the operation; manual approval requires rich TUI mode")
				_ = a.send(backendCommand{ID: "approval", Type: "approval_response", ApprovalID: response.Event.ApprovalID, Approved: false})
			}
			if response.Event.Type == "compaction_end" {
				a.finishTurn()
			}
			if a.program != nil {
				a.renderRich(*response.Event)
			} else {
				a.render(*response.Event)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		a.printStatus("backend stream error: " + err.Error())
	}
	a.finishTurn()
}

func (a *app) render(event agentEvent) {
	a.printMu.Lock()
	defer a.printMu.Unlock()
	switch event.Type {
	case "tool_safety_update":
		color := dim
		if event.SafetyStatus == "approved" {
			color = green
		}
		if event.SafetyStatus == "rejected" {
			color = red
		}
		fmt.Printf("%sBash Safety · %s%s\n", color, event.SafetyMessage, reset)
	case "compaction_start":
		fmt.Printf("%sCompacting context…%s\n", dim, reset)
	case "compaction_end":
		if event.Error != "" {
			fmt.Printf("%sCompaction failed: %s%s\n", red, event.Error, reset)
		} else {
			fmt.Printf("%sContext compacted.%s\n", green, reset)
		}
	case "message_start":
		if event.Message != nil && event.Message.Role == "user" {
			fmt.Printf("%sYou%s\n", green, reset)
		}
	case "message_update":
		if event.Message == nil || event.Message.Role != "assistant" {
			return
		}
		text := messageText(event.Message)
		if strings.HasPrefix(text, a.lastText) {
			fmt.Print(text[len(a.lastText):])
		} else {
			fmt.Print("\n" + text)
		}
		a.lastText = text
	case "tool_execution_start":
		encoded, _ := json.Marshal(event.Arguments)
		a.toolOutput[event.ToolCallID] = ""
		fmt.Printf("\n%s→ %s%s %s%s\n", yellow, event.ToolName, reset, dim, encoded)
		fmt.Printf("%s(streaming; Ctrl-C aborts and kills the process)%s\n", dim, reset)
	case "tool_execution_update":
		if event.Result != nil {
			a.printToolDelta(event.ToolCallID, toolResultText(event.Result))
		}
	case "tool_execution_end":
		if event.Result != nil {
			a.printToolDelta(event.ToolCallID, toolResultText(event.Result))
		}
		delete(a.toolOutput, event.ToolCallID)
		if event.IsError {
			fmt.Printf("%sTool failed%s\n", red, reset)
		} else {
			fmt.Printf("%sTool complete%s\n", dim, reset)
		}
	case "agent_end":
		if event.Error != "" {
			fmt.Printf("\n%sError: %s%s\n", red, event.Error, reset)
		} else {
			fmt.Print("\n\n")
		}
		a.finishTurn()
	}
}

func (a *app) printToolDelta(id, text string) {
	previous := a.toolOutput[id]
	if strings.HasPrefix(text, previous) {
		fmt.Print(text[len(previous):])
	} else {
		fmt.Print("\n" + text)
	}
	a.toolOutput[id] = text
}

func (a *app) readLine() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		if !a.lineScanner.Scan() {
			return "", io.EOF
		}
		return a.lineScanner.Text(), nil
	}
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return "", err
	}
	defer term.Restore(int(os.Stdin.Fd()), state)
	reader := bufio.NewReader(os.Stdin)
	buffer := ""
	a.printMu.Lock()
	fmt.Print(green + "> " + reset)
	a.printMu.Unlock()
	for {
		runeValue, _, err := reader.ReadRune()
		if err != nil {
			return "", err
		}
		switch runeValue {
		case '\r', '\n':
			fmt.Print("\r\n")
			return buffer, nil
		case 3:
			fmt.Print("^C\r\n")
			return "", errInterrupt
		case 4:
			if buffer == "" {
				return "", io.EOF
			}
		case 127, 8:
			if len(buffer) > 0 {
				_, size := lastRune(buffer)
				buffer = buffer[:len(buffer)-size]
				fmt.Print("\b \b")
			}
		case '\t':
			matches := completions(buffer)
			if len(matches) == 1 {
				buffer = matches[0]
				redrawInput(buffer)
			} else if len(matches) > 1 {
				fmt.Print("\r\n" + dim + strings.Join(matches, "  ") + reset + "\r\n")
				redrawInput(buffer)
			}
		default:
			buffer += string(runeValue)
			fmt.Print(string(runeValue))
		}
	}
}
func lastRune(value string) (rune, int) {
	for index := len(value) - 1; index >= 0; index-- {
		if value[index]&0xc0 != 0x80 {
			r := []rune(value[index:])[0]
			return r, len(string(r))
		}
	}
	return 0, 0
}
func redrawInput(value string) { fmt.Print("\r\x1b[2K" + green + "> " + reset + value) }
func completions(value string) []string {
	if !strings.HasPrefix(value, "/") {
		return nil
	}
	if strings.HasPrefix(value, "/model ") {
		return prefixMatches("/model ", strings.TrimPrefix(value, "/model "), models)
	}
	if strings.HasPrefix(value, "/model") {
		return prefixMatches("", value, commands)
	}
	return prefixMatches("", value, commands)
}
func prefixMatches(prefix, value string, choices []string) []string {
	var matches []string
	for _, choice := range choices {
		if strings.HasPrefix(choice, value) {
			matches = append(matches, prefix+choice)
		}
	}
	return matches
}
func messageText(message *message) string {
	var text strings.Builder
	for _, block := range message.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
		if block.Type == "image" {
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString("[image attachment]")
		}
	}
	return text.String()
}
func (a *app) printStatus(text string) {
	a.printMu.Lock()
	defer a.printMu.Unlock()
	fmt.Printf("\n%s%s%s\n", dim, text, reset)
}
