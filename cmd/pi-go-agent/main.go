// pi-go-agent is the first runnable Go agent-core backend. It owns the agent
// loop, built-in tools, and persistent per-session Codex WebSocket transport.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/peterw22/forge/internal/agent"
	systemprompt "github.com/peterw22/forge/internal/prompt"
	"github.com/peterw22/forge/internal/session"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--agy-mcp-stdio" {
		if err := runAgyMCPChild(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	var prompt, model, thinking, systemPrompt, cwd string
	var serve, unixPeerAuth bool
	var unixBridge string
	var sessionPath string
	var resume bool
	var listenAddress string
	var allowRemote bool
	var authLogin, authLogout, printIdentity, authorize bool
	var authorizedDevices string
	flag.StringVar(&prompt, "prompt", "", "prompt to run")
	flag.BoolVar(&serve, "serve", false, "run the JSONL agent-backend server")
	flag.StringVar(&sessionPath, "session", "", "session JSONL path")
	flag.BoolVar(&resume, "resume", false, "restore the session JSONL path")
	flag.StringVar(&listenAddress, "listen", "", "serve JSONL over unix:///path, tcp://host:port, or ws://host:port/path")
	flag.BoolVar(&allowRemote, "allow-remote", false, "allow binding TCP/WebSocket to a non-loopback address (unsafe without a trusted network)")
	flag.BoolVar(&authLogin, "login", false, "sign in to OpenAI Codex using the OAuth device flow")
	flag.BoolVar(&authLogout, "logout", false, "remove stored OpenAI Codex credentials")
	flag.BoolVar(&printIdentity, "print-identity", false, "print the server P-256 identity and exit")
	flag.BoolVar(&authorize, "authorize-device", false, "add the device entry that Forge copies, read from standard input, to the device whitelist and exit")
	flag.StringVar(&authorizedDevices, "authorized-devices", "", "device whitelist for network listeners (default: $PI_GO_CONFIG_DIR/authorized-devices.json or ~/.pi-go/authorized-devices.json)")
	flag.StringVar(&model, "model", "gpt-5.6-terra", "Codex model ID")
	flag.StringVar(&thinking, "thinking", "high", "thinking level: off, minimal, low, medium, high, xhigh, max")
	flag.StringVar(&systemPrompt, "system-prompt", "", "system prompt (defaults to the Pi Go coding-agent prompt)")
	flag.StringVar(&cwd, "cwd", "", "working directory for built-in tools")
	flag.BoolVar(&unixPeerAuth, "unix-peer-auth", false, "trust kernel-verified same-user Unix clients instead of device pairing (Unix listeners only)")
	flag.StringVar(&unixBridge, "connect-unix", "", "bridge stdin/stdout to a kernel-verified same-user Unix socket")
	flag.Parse()
	if unixBridge != "" {
		if err := bridgeUnix(unixBridge, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := validateUnixPeerMode(listenAddress, authorizedDevices, unixPeerAuth); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if authorize {
		if err := runAuthorizeDevice(authorizedDevices, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if prompt == "" && !serve && listenAddress == "" && !authLogin && !authLogout && !printIdentity {
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
	if printIdentity {

		identity, identityErr := loadServerIdentity()
		if identityErr != nil {
			fmt.Fprintln(os.Stderr, identityErr)
			os.Exit(1)
		}
		encoded, _ := json.Marshal(identity.PublicKey)
		fmt.Printf("Agent ID: %s\nP-256 fingerprint: %s\nPublic JWK: %s\n", identity.AgentID, identity.Fingerprint, encoded)
		return
	}
	var authPolicy *clientAuthPolicy
	// Network listeners require device authentication. An explicitly opted-in
	// Unix listener uses kernel-verified same-user credentials instead.
	// Stdio remains trusted parent/child IPC.
	if !unixPeerAuth && strings.TrimSpace(listenAddress) != "" && strings.TrimSpace(authorizedDevices) == "" {
		defaultPath, pathErr := defaultAuthorizedDevicesPath()
		if pathErr != nil {
			fmt.Fprintln(os.Stderr, "remote listener authentication:", pathErr)
			os.Exit(1)
		}
		authorizedDevices = defaultPath
	}
	if strings.TrimSpace(authorizedDevices) != "" {
		var policyErr error
		authPolicy, policyErr = loadClientAuthPolicy(authorizedDevices)
		if policyErr != nil {
			fmt.Fprintf(os.Stderr, "remote listener requires a valid device whitelist at %s: %v\n", authorizedDevices, policyErr)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "mutual P-256 authentication and AES-GCM encryption required; server fingerprint %s\n", authPolicy.identity.Fingerprint)
	}
	authManager, err := newCodexAuthManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runtimeAuthManager = authManager
	if authLogout {
		if err := authManager.Logout(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("Signed out of OpenAI Codex")
		return
	}
	if authLogin {
		ctx, beginErr := authManager.BeginLogin()
		if beginErr != nil {
			fmt.Fprintln(os.Stderr, beginErr)
			os.Exit(1)
		}
		defer authManager.EndLogin()
		status, loginErr := authManager.LoginDeviceCode(ctx, func(code codexDeviceCode) {
			fmt.Printf("Open %s and enter code %s\n", code.VerificationURI, code.UserCode)
		})
		if loginErr != nil {
			fmt.Fprintln(os.Stderr, loginErr)
			os.Exit(1)
		}
		fmt.Printf("Signed in to OpenAI Codex account %s\n", status.AccountID)
		return
	}
	codexProvider := newCodexProvider(defaultCodexEndpoint, authManager.Token)
	defer codexProvider.Close()
	apiProvider := newAPIProvider(authManager.APIRuntimeConfig)
	var agyProviderInstance *agyProvider
	if binary, lookupErr := agyBinary(); lookupErr == nil {
		agyProviderInstance = newAgyProvider(binary)
		agyProviderInstance.policyReady = true
	}
	var claudeProviderInstance *claudeCLIProvider
	if binary, err := exec.LookPath("claude"); err == nil {
		claudeProviderInstance = newClaudeCLIProvider(binary)
	}
	runtimeClaudeProvider = claudeProviderInstance
	runtimeAgyProvider = agyProviderInstance
	provider := &providerRouter{codex: codexProvider, api: apiProvider, agy: agyProviderInstance, claude: claudeProviderInstance}
	classifierConfig, err := newClassifierSettings()
	if err != nil {
		fmt.Fprintln(os.Stderr, "classifier settings:", err)
		os.Exit(1)
	}
	runtimeClassifierSettings = classifierConfig
	runtimeClassifierProvider = provider
	allowApproval := serve || listenAddress != ""
	factory := func(workspace, selectedModel, selectedThinking string, messages []agent.Message, usage agent.Usage) (*agent.Agent, error) {
		if selectedModel == "" {
			selectedModel = model
		}
		if selectedThinking == "" {
			selectedThinking = thinking
		}
		if claudeModel, ok := strings.CutPrefix(selectedModel, "claude/"); ok {
			if err := claudeModelEffortError(claudeModel, selectedThinking); err != nil {
				return nil, err
			}
		}
		if strings.HasPrefix(selectedModel, "agy/") {
			if agyProviderInstance == nil {
				_, err := agyBinary()
				return nil, err
			}
			if _, err := readyAgyModels(context.Background(), workspace); err != nil {
				return nil, err
			}

		}
		if strings.HasPrefix(selectedModel, "claude/") {
			if claudeProviderInstance == nil {
				return nil, errors.New("Claude Code is not installed; install `claude` and restart pi-go-agent")
			}
			if !claudeModelAllowed(strings.TrimPrefix(selectedModel, "claude/")) {
				return nil, errors.New("unsupported Claude model")
			}

		}
		promptForWorkspace := systemprompt.Default(workspace)
		if customSystemPrompt {
			promptForWorkspace = systemPrompt
		}
		core, createErr := agent.New(agent.Config{
			Model: selectedModel, Thinking: selectedThinking,
			SystemPrompt: promptForWorkspace, WorkingDirectory: workspace,
			Provider: provider, Tools: builtInTools(workspace), ParallelTools: true,
			ToolGuard: newSafetyGate(provider, classifierConfig.Model), AllowApproval: allowApproval,
		})
		if createErr == nil && (len(messages) > 0 || usage.TotalTokens > 0) {
			core.Restore(messages, selectedModel, selectedThinking, usage)
		}
		return core, createErr
	}
	if serve || listenAddress != "" {
		var err error
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
		}
		core, err := factory(workspace, selectedModel, selectedThinking, messages, usage)
		if err != nil {
			if store != nil {
				_ = store.Close()
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		core.SetYOLO(selectedYOLO)
		var initial *sessionRuntime
		if store != nil {
			initial = newSessionRuntime(store.ID(), core, session.NewController(cwd, store))
		} else {
			draftID, idErr := session.NewID()
			if idErr != nil {
				fmt.Fprintln(os.Stderr, idErr)
				os.Exit(1)
			}
			initial = newDraftSessionRuntime(draftID, cwd, core)
		}
		registry := newRuntimeRegistry(cwd, initial, factory)
		if err := registry.enableCron(); err != nil {
			registry.Shutdown()
			fmt.Fprintln(os.Stderr, "cron scheduler:", err)
			os.Exit(1)
		}
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
				err = serveWebSocket(registry, listenAddress, allowRemote, authPolicy)
			} else {
				err = serveSocketWithPeerAuth(registry, listenAddress, allowRemote, authPolicy, unixPeerAuth)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		} else if err := serveClient(registry, os.Stdin, os.Stdout, nil, nil); err != nil && !errors.Is(err, errBackendShutdown) {
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
	core.SetSessionID("standalone")
	browser := &browserSession{}
	core.SetTools(builtInTools(cwd, browser.tools()...))
	defer browser.close()
	defer core.CloseProviderSession()
	encoder := json.NewEncoder(os.Stdout)
	if err := core.Run(context.Background(), prompt, func(event agent.Event) { _ = encoder.Encode(event) }); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type backendCommand struct {
	BrowserInstance     string               `json:"browserInstance,omitempty"`
	ControlToken        string               `json:"controlToken,omitempty"`
	FrameID             uint64               `json:"frameId,omitempty"`
	BrowserAction       string               `json:"browserAction,omitempty"`
	X                   float64              `json:"x,omitempty"`
	Y                   float64              `json:"y,omitempty"`
	Text                string               `json:"text,omitempty"`
	Key                 string               `json:"key,omitempty"`
	Button              string               `json:"button,omitempty"`
	Direction           string               `json:"direction,omitempty"`
	ID                  string               `json:"id,omitempty"`
	Type                string               `json:"type"`
	Message             string               `json:"message,omitempty"`
	Level               string               `json:"level,omitempty"`
	Model               string               `json:"model,omitempty"`
	ClassifierModel     string               `json:"classifierModel,omitempty"`
	Name                *string              `json:"name"`
	Session             string               `json:"session,omitempty"`
	CWD                 string               `json:"cwd,omitempty"`
	Directory           string               `json:"directory,omitempty"`
	Content             []agent.ContentBlock `json:"content,omitempty"`
	CustomInstructions  string               `json:"customInstructions,omitempty"`
	ApprovalID          string               `json:"approvalId,omitempty"`
	Approved            bool                 `json:"approved,omitempty"`
	Enabled             bool                 `json:"enabled"`
	Provider            string               `json:"provider,omitempty"`
	ProviderName        string               `json:"providerName,omitempty"`
	DeleteProvider      bool                 `json:"deleteProvider,omitempty"`
	APIKey              string               `json:"apiKey,omitempty"`
	ClearAPIKey         bool                 `json:"clearApiKey,omitempty"`
	Protocol            string               `json:"protocol,omitempty"`
	OpenAIBaseURL       string               `json:"openaiBaseUrl,omitempty"`
	AnthropicBaseURL    string               `json:"anthropicBaseUrl,omitempty"`
	DefaultModel        string               `json:"defaultModel,omitempty"`
	Models              []string             `json:"models,omitempty"`
	Platform            string               `json:"platform,omitempty"`
	DeviceID            string               `json:"deviceId,omitempty"`
	DeviceFingerprint   string               `json:"deviceFingerprint,omitempty"`
	ConnectionChallenge string               `json:"connectionChallenge,omitempty"`
	PairingID           string               `json:"pairingId,omitempty"`
	Before              int                  `json:"before,omitempty"`
	Limit               int                  `json:"limit,omitempty"`
	AuthProtocol        string               `json:"authProtocol,omitempty"`
	ConnectionID        string               `json:"connectionId,omitempty"`
	ClientNonce         string               `json:"clientNonce,omitempty"`
	ServerNonce         string               `json:"serverNonce,omitempty"`
	DeviceSignature     string               `json:"deviceSignature,omitempty"`
	Signature           string               `json:"signature,omitempty"`
	ExpiresAt           int64                `json:"authExpiresAt,omitempty"`
	ClientEphemeralKey  pushJWK              `json:"clientEphemeralKey,omitempty"`
	ServerEphemeralKey  pushJWK              `json:"serverEphemeralKey,omitempty"`
	Cipher              string               `json:"cipher,omitempty"`
	EncryptedVersion    int                  `json:"version,omitempty"`
	EncryptedSequence   uint64               `json:"sequence"`
	Ciphertext          string               `json:"ciphertext,omitempty"`
}

type backendResponse struct {
	Browser            *browserViewState   `json:"browser,omitempty"`
	BrowserFrame       *browserViewFrame   `json:"browserFrame,omitempty"`
	ControlToken       string              `json:"controlToken,omitempty"`
	ID                 string              `json:"id,omitempty"`
	Type               string              `json:"type"`
	Command            string              `json:"command,omitempty"`
	Success            bool                `json:"success,omitempty"`
	Active             bool                `json:"active,omitempty"`
	WaitingInput       bool                `json:"waitingInput,omitempty"`
	Event              *agent.Event        `json:"event,omitempty"`
	Error              string              `json:"error,omitempty"`
	State              *agent.State        `json:"state,omitempty"`
	Sessions           []session.Entry     `json:"sessions,omitempty"`
	Model              string              `json:"model,omitempty"`
	Thinking           string              `json:"thinking,omitempty"`
	ClassifierModel    string              `json:"classifierModel,omitempty"`
	Session            string              `json:"session,omitempty"`
	CWD                string              `json:"cwd,omitempty"`
	Directory          string              `json:"directory,omitempty"`
	Parent             string              `json:"parent,omitempty"`
	Home               string              `json:"home,omitempty"`
	Directories        []string            `json:"directories,omitempty"`
	DirectoriesCut     bool                `json:"directoriesCut,omitempty"`
	YOLO               *bool               `json:"yolo,omitempty"`
	Authenticated      *bool               `json:"authenticated,omitempty"`
	AccountID          string              `json:"accountId,omitempty"`
	ExpiresAt          int64               `json:"expiresAt,omitempty"`
	UserCode           string              `json:"userCode,omitempty"`
	VerificationURI    string              `json:"verificationUri,omitempty"`
	ClaudeInstalled    *bool               `json:"claudeInstalled,omitempty"`
	ClaudeReady        *bool               `json:"claudeReady,omitempty"`
	ClaudeSetupCommand string              `json:"claudeSetupCommand,omitempty"`
	ClaudeSetupMessage string              `json:"claudeSetupMessage,omitempty"`
	AgyInstalled       *bool               `json:"agyInstalled,omitempty"`
	AgyReady           *bool               `json:"agyReady,omitempty"`
	AgySetupCommand    string              `json:"agySetupCommand,omitempty"`
	AgySetupMessage    string              `json:"agySetupMessage,omitempty"`
	Provider           string              `json:"provider,omitempty"`
	ProviderConfigs    []apiPublicConfig   `json:"providerConfigs,omitempty"`
	ProviderConfig     *apiPublicConfig    `json:"providerConfig,omitempty"`
	Models             []modelInfo         `json:"models,omitempty"`
	AgentID            string              `json:"agentId,omitempty"`
	AgentPublicKey     *pushJWK            `json:"agentPublicKey,omitempty"`
	AgentFingerprint   string              `json:"agentFingerprint,omitempty"`
	ConnectionProof    string              `json:"connectionProof,omitempty"`
	DeviceID           string              `json:"deviceId,omitempty"`
	PairingID          string              `json:"pairingId,omitempty"`
	VerificationCode   string              `json:"verificationCode,omitempty"`
	Scopes             []string            `json:"scopes,omitempty"`
	PushAuthorizations []pushAuthorization `json:"pushAuthorizations,omitempty"`
	PairingExpiresAt   int64               `json:"pairingExpiresAt,omitempty"`
	PushAuthorized     bool                `json:"pushAuthorized,omitempty"`
	PushKeyID          string              `json:"pushKeyId,omitempty"`
	PushKey            string              `json:"pushKey,omitempty"`
	PushKeySignature   string              `json:"pushKeySignature,omitempty"`
	HistoryMessages    []agent.Message     `json:"historyMessages,omitempty"`
	HistoryBefore      int                 `json:"historyBefore,omitempty"`
	HistoryHasMore     bool                `json:"historyHasMore,omitempty"`
	SessionsOffset     int                 `json:"sessionsOffset,omitempty"`
	SessionsHasMore    bool                `json:"sessionsHasMore,omitempty"`
	ProtocolVersion    string              `json:"authProtocol,omitempty"`
	ConnectionID       string              `json:"connectionId,omitempty"`
	ClientNonce        string              `json:"clientNonce,omitempty"`
	ServerNonce        string              `json:"serverNonce,omitempty"`
	DeviceFingerprint  string              `json:"deviceFingerprint,omitempty"`
	ServerSignature    string              `json:"serverSignature,omitempty"`
	ServerEphemeralKey *pushJWK            `json:"serverEphemeralKey,omitempty"`
	Cipher             string              `json:"cipher,omitempty"`
	EncryptedVersion   int                 `json:"version,omitempty"`
	EncryptedSequence  uint64              `json:"sequence"`
	Ciphertext         string              `json:"ciphertext,omitempty"`
}

var errBackendShutdown = errors.New("agent backend shutdown")

func serveSocket(registry *runtimeRegistry, endpoint string, allowRemote bool, authPolicy *clientAuthPolicy) error {
	return serveSocketWithPeerAuth(registry, endpoint, allowRemote, authPolicy, false)
}

func serveSocketWithPeerAuth(registry *runtimeRegistry, endpoint string, allowRemote bool, authPolicy *clientAuthPolicy, peerAuth bool) error {
	if err := validateUnixPeerMode(endpoint, "", peerAuth); err != nil {
		return err
	}
	if peerAuth && authPolicy != nil {
		return errors.New("Unix peer authentication cannot be combined with device authentication")
	}
	if authPolicy == nil && !peerAuth {
		return errors.New("TCP and Unix listeners require an authorized device whitelist or explicit Unix peer authentication")
	}
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
		if peerAuth {
			unixConnection, ok := connection.(*net.UnixConn)
			if !ok {
				_ = connection.Close()
				continue
			}
			if err := verifyUnixPeer(unixConnection); err != nil {
				fmt.Fprintln(os.Stderr, "rejected Unix client:", err)
				_ = connection.Close()
				continue
			}
		}
		clients.Add(1)
		go func(connection net.Conn) {
			defer clients.Done()
			err := serveClient(registry, connection, connection, connection, authPolicy)
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
