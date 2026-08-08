package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/peterw22/pi-go/internal/agent"
	systemprompt "github.com/peterw22/pi-go/internal/prompt"
	"github.com/peterw22/pi-go/internal/session"
)

const subscriberQueueSize = 1024

type outboundResponse struct {
	response   backendResponse
	written    chan error
	generation uint64
}

type subscriber struct {
	responses  chan outboundResponse
	done       chan struct{}
	closeOnce  sync.Once
	closeIO    func()
	generation atomic.Uint64
}

func newSubscriber(closeIO func()) *subscriber {
	subscriber := &subscriber{responses: make(chan outboundResponse, subscriberQueueSize), done: make(chan struct{}), closeIO: closeIO}
	subscriber.generation.Store(1)
	return subscriber
}
func (subscriber *subscriber) nextGeneration() { subscriber.generation.Add(1) }
func (subscriber *subscriber) enqueue(response backendResponse) bool {
	select {
	case <-subscriber.done:
		return false
	default:
	}
	select {
	case <-subscriber.done:
		return false
	case subscriber.responses <- outboundResponse{response: response, generation: subscriber.generation.Load()}:
		return true
	default:
		subscriber.close()
		return false
	}
}
func (subscriber *subscriber) enqueueAndWait(response backendResponse) error {
	written := make(chan error, 1)
	select {
	case subscriber.responses <- outboundResponse{response: response, written: written, generation: subscriber.generation.Load()}:
	case <-subscriber.done:
		return io.ErrClosedPipe
	}
	select {
	case err := <-written:
		return err
	case <-subscriber.done:
		return io.ErrClosedPipe
	}
}
func (subscriber *subscriber) close() {
	subscriber.closeOnce.Do(func() {
		close(subscriber.done)
		if subscriber.closeIO != nil {
			go subscriber.closeIO()
		}
	})
}

type activeToolState struct {
	start  agent.Event
	safety *agent.Event
	update *agent.Event
}
type inFlightState struct {
	runID, kind string
	assistant   *agent.Message
	tools       map[string]*activeToolState
	toolOrder   []string
	approval    *agent.Event
	ending      bool
}

type sessionRuntime struct {
	id       string
	core     *agent.Agent
	sessions *session.Controller

	mu          sync.Mutex
	active      bool
	cancel      context.CancelFunc
	inFlight    inFlightState
	subscribers map[*subscriber]struct{}
	tasks       sync.WaitGroup
	closed      bool
}

func newSessionRuntime(id string, core *agent.Agent, sessions *session.Controller) *sessionRuntime {
	return &sessionRuntime{id: id, core: core, sessions: sessions, subscribers: make(map[*subscriber]struct{})}
}
func (runtime *sessionRuntime) subscribe(subscriber *subscriber) {
	runtime.subscribeFor(subscriber, "attached", "get_state")
}
func (runtime *sessionRuntime) subscribeFor(subscriber *subscriber, id, command string) {
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		subscriber.close()
		return
	}
	// Queue a coherent initial snapshot and catch-up before live publication can
	// enqueue anything for this subscriber.
	runtime.enqueueSnapshotLocked(subscriber, id, command)
	runtime.subscribers[subscriber] = struct{}{}
	runtime.mu.Unlock()
}
func (runtime *sessionRuntime) unsubscribe(subscriber *subscriber) {
	runtime.mu.Lock()
	delete(runtime.subscribers, subscriber)
	runtime.mu.Unlock()
}
func (runtime *sessionRuntime) sendSnapshot(subscriber *subscriber, id, command string) {
	runtime.mu.Lock()
	runtime.enqueueSnapshotLocked(subscriber, id, command)
	runtime.mu.Unlock()
}
func (runtime *sessionRuntime) enqueueSnapshotLocked(subscriber *subscriber, id, command string) {
	state := runtime.core.Snapshot()
	model, thinking, cwd := runtime.core.Settings()
	subscriber.enqueue(backendResponse{ID: id, Type: "response", Command: command, Success: true, State: &state, Model: model, Thinking: thinking, CWD: cwd, Session: runtime.id})
	if !runtime.active || runtime.inFlight.ending {
		return
	}
	events := runtime.catchUpEventsLocked(state)
	for _, event := range events {
		copy := event
		subscriber.enqueue(backendResponse{ID: runtime.inFlight.runID, Type: "event", Event: &copy, Session: runtime.id})
	}
}
func (runtime *sessionRuntime) catchUpEventsLocked(state agent.State) []agent.Event {
	if runtime.inFlight.kind == "compact" {
		return []agent.Event{{Type: agent.EventCompactionStart}}
	}
	events := []agent.Event{{Type: agent.EventAgentStart}}
	assistantCommitted := false
	if runtime.inFlight.assistant != nil && len(state.Messages) > 0 {
		last := state.Messages[len(state.Messages)-1]
		assistantCommitted = last.Role == agent.RoleAssistant && last.Timestamp == runtime.inFlight.assistant.Timestamp
	}
	if runtime.inFlight.assistant != nil && !assistantCommitted {
		message := cloneMessage(*runtime.inFlight.assistant)
		events = append(events, agent.Event{Type: agent.EventMessageStart, Message: &message}, agent.Event{Type: agent.EventMessageUpdate, Message: &message})
	}
	for _, id := range runtime.inFlight.toolOrder {
		committed := false
		for _, message := range state.Messages {
			if message.Role == agent.RoleToolResult && message.ToolCallID == id {
				committed = true
				break
			}
		}
		if committed {
			continue
		}
		tool := runtime.inFlight.tools[id]
		if tool == nil {
			continue
		}
		events = append(events, cloneEvent(tool.start))
		if tool.safety != nil {
			events = append(events, cloneEvent(*tool.safety))
		}
		if tool.update != nil {
			events = append(events, cloneEvent(*tool.update))
		}
	}
	if runtime.inFlight.approval != nil {
		events = append(events, cloneEvent(*runtime.inFlight.approval))
	}
	return events
}
func (runtime *sessionRuntime) publish(runID string, event agent.Event) {
	runtime.mu.Lock()
	runtime.updateInFlightLocked(event)
	response := backendResponse{ID: runID, Type: "event", Event: &event, Session: runtime.id}
	for subscriber := range runtime.subscribers {
		if !subscriber.enqueue(response) {
			delete(runtime.subscribers, subscriber)
			log.Printf("removed slow agent subscriber for session %s", runtime.id)
		}
	}
	runtime.mu.Unlock()
}
func (runtime *sessionRuntime) publishStateEvent(runID string, event agent.Event) {
	runtime.mu.Lock()
	runtime.updateInFlightLocked(event)
	state := runtime.core.Snapshot()
	model, thinking, cwd := runtime.core.Settings()
	response := backendResponse{ID: runID, Type: "event", Event: &event, State: &state, Model: model, Thinking: thinking, CWD: cwd, Session: runtime.id}
	for subscriber := range runtime.subscribers {
		if !subscriber.enqueue(response) {
			delete(runtime.subscribers, subscriber)
		}
	}
	runtime.mu.Unlock()
}
func (runtime *sessionRuntime) publishError(runID string, err error) {
	response := backendResponse{ID: runID, Type: "error", Error: err.Error(), Session: runtime.id}
	runtime.mu.Lock()
	for subscriber := range runtime.subscribers {
		if !subscriber.enqueue(response) {
			delete(runtime.subscribers, subscriber)
		}
	}
	runtime.mu.Unlock()
}
func (runtime *sessionRuntime) updateInFlightLocked(event agent.Event) {
	switch event.Type {
	case agent.EventMessageStart, agent.EventMessageUpdate:
		if event.Message != nil && event.Message.Role == agent.RoleAssistant {
			message := cloneMessage(*event.Message)
			runtime.inFlight.assistant = &message
		}
		if event.Message != nil && event.Message.Role == agent.RoleToolResult {
			delete(runtime.inFlight.tools, event.Message.ToolCallID)
		}
	case agent.EventMessageEnd:
		if event.Message != nil {
			if event.Message.Role == agent.RoleAssistant {
				runtime.inFlight.assistant = nil
			}
			if event.Message.Role == agent.RoleToolResult {
				delete(runtime.inFlight.tools, event.Message.ToolCallID)
			}
		}
	case agent.EventToolExecutionStart:
		if runtime.inFlight.tools == nil {
			runtime.inFlight.tools = make(map[string]*activeToolState)
		}
		if _, exists := runtime.inFlight.tools[event.ToolCallID]; !exists {
			runtime.inFlight.toolOrder = append(runtime.inFlight.toolOrder, event.ToolCallID)
		}
		runtime.inFlight.tools[event.ToolCallID] = &activeToolState{start: cloneEvent(event)}
	case agent.EventToolSafetyUpdate:
		if tool := runtime.inFlight.tools[event.ToolCallID]; tool != nil {
			copy := cloneEvent(event)
			tool.safety = &copy
		}
	case agent.EventToolExecutionUpdate, agent.EventToolExecutionEnd:
		if tool := runtime.inFlight.tools[event.ToolCallID]; tool != nil {
			copy := cloneEvent(event)
			tool.update = &copy
		}
	case agent.EventApprovalRequired:
		copy := cloneEvent(event)
		runtime.inFlight.approval = &copy
	case agent.EventAgentEnd:
		runtime.inFlight.approval = nil
		runtime.inFlight.ending = true
	}
}
func (runtime *sessionRuntime) StartPrompt(id string, content []agent.ContentBlock) error {
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return errors.New("session runtime is closed")
	}
	if runtime.active {
		runtime.mu.Unlock()
		return errors.New("agent is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime.active = true
	runtime.cancel = cancel
	runtime.inFlight = inFlightState{runID: id, kind: "prompt", tools: make(map[string]*activeToolState)}
	runtime.tasks.Add(1)
	runtime.mu.Unlock()
	go func() {
		defer runtime.tasks.Done()
		err := runtime.core.RunContent(ctx, content, func(event agent.Event) {
			if event.Type == agent.EventMessageEnd && event.Message != nil {
				if appendErr := runtime.sessions.Append(*event.Message, event.Usage); appendErr != nil {
					runtime.publishError(id, fmt.Errorf("persist session message: %w", appendErr))
					cancel()
				}
			}
			runtime.publish(id, event)
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			runtime.publishError(id, err)
		}
		runtime.finishRun()
	}()
	return nil
}
func (runtime *sessionRuntime) StartCompaction(id, instructions string) error {
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return errors.New("session runtime is closed")
	}
	if runtime.active {
		runtime.mu.Unlock()
		return errors.New("agent is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime.active = true
	runtime.cancel = cancel
	runtime.inFlight = inFlightState{runID: id, kind: "compact"}
	runtime.tasks.Add(1)
	runtime.mu.Unlock()
	go func() {
		defer runtime.tasks.Done()
		runtime.publish(id, agent.Event{Type: agent.EventCompactionStart})
		_, err := runtime.core.Compact(ctx, instructions, runtime.sessions.AppendCompaction)
		end := agent.Event{Type: agent.EventCompactionEnd}
		if err != nil {
			end.Error = err.Error()
		}
		runtime.publishStateEvent(id, end)
		runtime.finishRun()
	}()
	return nil
}
func (runtime *sessionRuntime) finishRun() {
	runtime.mu.Lock()
	runtime.active = false
	runtime.cancel = nil
	runtime.inFlight = inFlightState{}
	runtime.mu.Unlock()
}
func (runtime *sessionRuntime) Busy() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.active
}
func (runtime *sessionRuntime) setCWD(value string) error {
	workspace, err := validWorkingDirectory(value)
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active {
		return errors.New("abort the current turn before changing the working directory")
	}
	if err := runtime.sessions.SetCWD(workspace); err != nil {
		return err
	}
	runtime.core.SetWorkingDirectory(workspace, systemprompt.Default(workspace), builtInTools(workspace))
	return nil
}
func (runtime *sessionRuntime) setModel(model string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model must not be empty")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active {
		return errors.New("abort the current turn before changing model")
	}
	if err := runtime.sessions.SetModel(model); err != nil {
		return err
	}
	runtime.core.SetModel(model)
	return nil
}
func (runtime *sessionRuntime) setThinking(level string) error {
	if !validThinking(level) {
		return errors.New("invalid thinking level")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active {
		return errors.New("abort the current turn before changing thinking level")
	}
	if err := runtime.sessions.SetThinking(level); err != nil {
		return err
	}
	runtime.core.SetThinkingLevel(level)
	return nil
}
func (runtime *sessionRuntime) setYOLO(enabled bool) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active {
		return errors.New("abort the current turn before changing YOLO mode")
	}
	if err := runtime.sessions.SetYOLO(enabled); err != nil {
		return err
	}
	runtime.core.SetYOLO(enabled)
	return nil
}
func (runtime *sessionRuntime) Abort() bool {
	runtime.mu.Lock()
	cancel, active := runtime.cancel, runtime.active
	runtime.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return active
}
func (runtime *sessionRuntime) ResolveApproval(id string, approved bool) bool {
	if !runtime.core.ResolveApproval(id, approved) {
		return false
	}
	runtime.mu.Lock()
	if runtime.inFlight.approval != nil && runtime.inFlight.approval.ApprovalID == id {
		runtime.inFlight.approval = nil
	}
	runtime.mu.Unlock()
	return true
}
func (runtime *sessionRuntime) close() {
	runtime.mu.Lock()
	runtime.closed = true
	if runtime.cancel != nil {
		runtime.cancel()
	}
	subscribers := make([]*subscriber, 0, len(runtime.subscribers))
	for s := range runtime.subscribers {
		subscribers = append(subscribers, s)
	}
	runtime.subscribers = make(map[*subscriber]struct{})
	runtime.mu.Unlock()
	for _, s := range subscribers {
		s.close()
	}
	runtime.tasks.Wait()
	_ = runtime.sessions.Close()
}

func cloneMessage(message agent.Message) agent.Message {
	copy := message
	copy.Content = append([]agent.ContentBlock(nil), message.Content...)
	return copy
}
func cloneEvent(event agent.Event) agent.Event {
	copy := event
	if event.Message != nil {
		message := cloneMessage(*event.Message)
		copy.Message = &message
	}
	if event.Result != nil {
		result := *event.Result
		result.Content = append([]agent.ContentBlock(nil), event.Result.Content...)
		copy.Result = &result
	}
	return copy
}

type agentFactory func(workspace, model, thinking string, messages []agent.Message, usage agent.Usage) (*agent.Agent, error)
type runtimeRegistry struct {
	mu               sync.Mutex
	root             string
	factory          agentFactory
	runtimes         map[string]*sessionRuntime
	defaultID        string
	closed           bool
	done             chan struct{}
	shutdownComplete chan struct{}
	shutdownOnce     sync.Once
}

func newRuntimeRegistry(root string, initial *sessionRuntime, factory agentFactory) *runtimeRegistry {
	return &runtimeRegistry{root: root, factory: factory, runtimes: map[string]*sessionRuntime{initial.id: initial}, defaultID: initial.id, done: make(chan struct{}), shutdownComplete: make(chan struct{})}
}
func (registry *runtimeRegistry) Default() *sessionRuntime {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.runtimes[registry.defaultID]
}
func (registry *runtimeRegistry) List() ([]session.Entry, error) { return session.List(registry.root) }
func (registry *runtimeRegistry) Attach(id string) (*sessionRuntime, error) {
	registry.mu.Lock()
	if runtime := registry.runtimes[id]; runtime != nil {
		registry.mu.Unlock()
		return runtime, nil
	}
	if registry.closed {
		registry.mu.Unlock()
		return nil, errors.New("server is shutting down")
	}
	registry.mu.Unlock()
	path, err := session.Resolve(registry.root, id)
	if err != nil {
		return nil, err
	}
	store, header, messages, usage, err := session.Resume(path)
	if err != nil {
		return nil, err
	}
	workspace := header.CWD
	if workspace == "" {
		workspace = registry.root
	}
	core, err := registry.factory(workspace, header.Model, header.Thinking, messages, usage)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	core.SetYOLO(header.YOLO)
	runtime := newSessionRuntime(id, core, session.NewController(registry.root, store))
	registry.mu.Lock()
	if existing := registry.runtimes[id]; existing != nil {
		registry.mu.Unlock()
		runtime.close()
		return existing, nil
	}
	if registry.closed {
		registry.mu.Unlock()
		runtime.close()
		return nil, errors.New("server is shutting down")
	}
	registry.runtimes[id] = runtime
	registry.mu.Unlock()
	return runtime, nil
}
func (registry *runtimeRegistry) New(from *sessionRuntime) (*sessionRuntime, error) {
	model, thinking, workspace := from.core.Settings()
	yolo := from.core.YOLOEnabled()
	store, err := session.NewAt(registry.root, workspace, model, thinking)
	if err != nil {
		return nil, err
	}
	if err := store.SetYOLO(yolo); err != nil {
		_ = store.Close()
		return nil, err
	}
	core, err := registry.factory(workspace, model, thinking, nil, agent.Usage{})
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	core.SetYOLO(yolo)
	runtime := newSessionRuntime(store.ID(), core, session.NewController(registry.root, store))
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		runtime.close()
		return nil, errors.New("server is shutting down")
	}
	registry.runtimes[runtime.id] = runtime
	registry.defaultID = runtime.id
	registry.mu.Unlock()
	return runtime, nil
}
func (registry *runtimeRegistry) Shutdown() {
	registry.shutdownOnce.Do(func() {
		close(registry.done)
		registry.mu.Lock()
		registry.closed = true
		runtimes := make([]*sessionRuntime, 0, len(registry.runtimes))
		for _, runtime := range registry.runtimes {
			runtimes = append(runtimes, runtime)
		}
		registry.mu.Unlock()
		for _, runtime := range runtimes {
			runtime.close()
		}
		close(registry.shutdownComplete)
	})
	<-registry.shutdownComplete
}

// clientConnection owns only attachment and transport state. Runs belong to a
// sessionRuntime and survive this function returning.
type clientConnection struct {
	registry   *runtimeRegistry
	runtime    *sessionRuntime
	subscriber *subscriber
}

func (client *clientConnection) attach(runtime *sessionRuntime) {
	client.attachFor(runtime, "attached", "get_state")
}
func (client *clientConnection) attachFor(runtime *sessionRuntime, id, command string) {
	if client.runtime != nil {
		client.runtime.unsubscribe(client.subscriber)
		client.subscriber.nextGeneration()
	}
	client.runtime = runtime
	runtime.subscribeFor(client.subscriber, id, command)
}
func (client *clientConnection) close() {
	if client.runtime != nil {
		client.runtime.unsubscribe(client.subscriber)
	}
	client.subscriber.close()
}

func serveClient(registry *runtimeRegistry, input io.Reader, output io.Writer, closer io.Closer) error {
	var closeIO func()
	if closer != nil {
		closeIO = func() { _ = closer.Close() }
	}
	subscriber := newSubscriber(closeIO)
	client := &clientConnection{registry: registry, subscriber: subscriber}
	client.attach(registry.Default())
	writerDone := make(chan struct{})
	defer func() { client.close(); <-writerDone }()
	go func() {
		defer close(writerDone)
		encoder := jsonEncoder(output)
		for {
			select {
			case outbound := <-subscriber.responses:
				if outbound.generation != subscriber.generation.Load() {
					if outbound.written != nil {
						outbound.written <- nil
					}
					continue
				}
				err := encoder(outbound.response)
				if outbound.written != nil {
					outbound.written <- err
				}
				if err != nil {
					subscriber.close()
					return
				}
			case <-subscriber.done:
				return
			}
		}
	}()
	decoder := jsonDecoder(input)
	for {
		var command backendCommand
		if err := decoder(&command); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode agent-backend command: %w", err)
		}
		if err := client.handle(command); errors.Is(err, errBackendShutdown) {
			return err
		} else if err != nil {
			client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Error: err.Error()})
		}
	}
}

// Small wrappers are variables so runtime tests can use the exact protocol path
// without constructing concrete encoders around every assertion.
var jsonEncoder = func(output io.Writer) func(backendResponse) error {
	encoder := json.NewEncoder(output)
	return func(response backendResponse) error { return encoder.Encode(response) }
}
var jsonDecoder = func(input io.Reader) func(*backendCommand) error {
	decoder := json.NewDecoder(input)
	return func(command *backendCommand) error { return decoder.Decode(command) }
}

func (client *clientConnection) reply(response backendResponse) { client.subscriber.enqueue(response) }
func (client *clientConnection) handle(command backendCommand) error {
	runtime := client.runtime
	switch command.Type {
	case "get_state":
		runtime.sendSnapshot(client.subscriber, command.ID, command.Type)
	case "list_sessions":
		entries, err := client.registry.List()
		if err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Sessions: entries, Session: runtime.id})
	case "new_session":
		next, err := client.registry.New(runtime)
		if err != nil {
			return err
		}
		client.attachFor(next, command.ID, command.Type)
	case "switch_session":
		next, err := client.registry.Attach(command.Session)
		if err != nil {
			return err
		}
		client.attachFor(next, command.ID, command.Type)
	case "prompt":
		content, err := promptContent(command.Message, command.Content)
		if err != nil {
			return err
		}
		if err = runtime.StartPrompt(command.ID, content); err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Session: runtime.id})
	case "compact":
		if err := runtime.StartCompaction(command.ID, command.CustomInstructions); err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Session: runtime.id})
	case "abort":
		active := runtime.Abort()
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Session: runtime.id, Active: active})
	case "approval_response":
		if strings.TrimSpace(command.ApprovalID) == "" {
			return errors.New("approvalId must not be empty")
		}
		if !runtime.ResolveApproval(command.ApprovalID, command.Approved) {
			return errors.New("approval request is no longer pending")
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Session: runtime.id})
	case "set_cwd":
		if err := runtime.setCWD(command.CWD); err != nil {
			return err
		}
		model, thinking, workspace := runtime.core.Settings()
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Model: model, Thinking: thinking, CWD: workspace, Session: runtime.id})
	case "set_model":
		if err := runtime.setModel(command.Model); err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Model: command.Model, Session: runtime.id})
	case "set_thinking_level":
		if err := runtime.setThinking(command.Level); err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Thinking: command.Level, Session: runtime.id})
	case "set_yolo":
		if err := runtime.setYOLO(command.Enabled); err != nil {
			return err
		}
		enabled := command.Enabled
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, YOLO: &enabled, Session: runtime.id})
	case "set_session_name":
		if err := runtime.sessions.SetName(command.Name); err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Session: runtime.id})
	case "shutdown":
		if err := client.subscriber.enqueueAndWait(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true}); err != nil {
			return err
		}
		go client.registry.Shutdown()
		return errBackendShutdown
	default:
		return errors.New("unsupported command")
	}
	return nil
}
