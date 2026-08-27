package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
	systemprompt "github.com/peterw22/pi-go/internal/prompt"
	"github.com/peterw22/pi-go/internal/session"
)

const (
	subscriberQueueSize       = 1024
	defaultTranscriptMessages = 25
	maxTranscriptMessages     = 100
)

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
	root     string
	core     *agent.Agent
	sessions *session.Controller

	mu             sync.Mutex
	active         bool
	cancel         context.CancelFunc
	inFlight       inFlightState
	subscribers    map[*subscriber]struct{}
	tasks          sync.WaitGroup
	closed         bool
	status         func(string, bool, bool)
	approvalPush   func(string, string)
	completionPush func(string, string)
	summarizeTurn  func(context.Context, agent.Message) (string, error)
	cron           *cronManager
}

func newSessionRuntime(id string, core *agent.Agent, sessions *session.Controller) *sessionRuntime {
	core.SetSessionID(id)
	return &sessionRuntime{id: id, core: core, sessions: sessions, subscribers: make(map[*subscriber]struct{})}
}
func newDraftSessionRuntime(id, root string, core *agent.Agent) *sessionRuntime {
	runtime := newSessionRuntime(id, core, nil)
	runtime.root = root
	return runtime
}
func (runtime *sessionRuntime) bindCron(manager *cronManager) {
	runtime.cron = manager
	_, _, workspace := runtime.core.Settings()
	if manager == nil {
		runtime.core.SetTools(builtInTools(workspace))
		return
	}
	runtime.core.SetTools(builtInTools(workspace, cronTool(manager, runtime.id)))
}

func (runtime *sessionRuntime) ensurePersistedLocked() error {
	if runtime.sessions != nil {
		return nil
	}
	model, thinking, workspace := runtime.core.Settings()
	store, err := session.NewAtID(runtime.root, workspace, model, thinking, runtime.id, runtime.core.YOLOEnabled())
	if err != nil {
		return err
	}
	runtime.sessions = session.NewController(runtime.root, store)
	return nil
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
func transcriptPage(messages []agent.Message, before, limit int) (page []agent.Message, start int, hasMore bool) {
	if before <= 0 || before > len(messages) {
		before = len(messages)
	}
	if limit <= 0 {
		limit = defaultTranscriptMessages
	}
	if limit > maxTranscriptMessages {
		limit = maxTranscriptMessages
	}
	start = max(0, before-limit)
	return append([]agent.Message(nil), messages[start:before]...), start, start > 0
}
func (runtime *sessionRuntime) enqueueSnapshotLocked(subscriber *subscriber, id, command string) {
	fullState := runtime.core.Snapshot()
	state := fullState
	page, start, hasMore := transcriptPage(fullState.Messages, len(fullState.Messages), defaultTranscriptMessages)
	state.Messages = page
	model, thinking, cwd := runtime.core.Settings()
	subscriber.enqueue(backendResponse{ID: id, Type: "response", Command: command, Success: true, State: &state, Model: model, Thinking: thinking, CWD: cwd, Session: runtime.id, HistoryBefore: start, HistoryHasMore: hasMore})
	if !runtime.active || runtime.inFlight.ending {
		return
	}
	events := runtime.catchUpEventsLocked(fullState)
	for _, event := range events {
		copy := event
		subscriber.enqueue(backendResponse{ID: runtime.inFlight.runID, Type: "event", Event: &copy, Session: runtime.id})
	}
}
func (runtime *sessionRuntime) sendHistory(subscriber *subscriber, id string, before, turns int) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	state := runtime.core.Snapshot()
	page, start, hasMore := transcriptPage(state.Messages, before, turns)
	subscriber.enqueue(backendResponse{ID: id, Type: "response", Command: "get_transcript_history", Success: true, Session: runtime.id, HistoryMessages: page, HistoryBefore: start, HistoryHasMore: hasMore})
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
	status := runtime.status
	approvalPush := runtime.approvalPush
	approvalRequired := event.Type == agent.EventApprovalRequired
	runtime.mu.Unlock()
	if approvalRequired {
		if approvalPush != nil {
			approvalPush(runtime.id, event.NotificationSummary)
		}
		if status != nil {
			status(runtime.id, true, true)
		}
	}
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
	if err := runtime.ensurePersistedLocked(); err != nil {
		runtime.mu.Unlock()
		return fmt.Errorf("persist draft session: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime.active = true
	runtime.cancel = cancel
	runtime.inFlight = inFlightState{runID: id, kind: "prompt", tools: make(map[string]*activeToolState)}
	runtime.tasks.Add(1)
	status := runtime.status
	runtime.mu.Unlock()
	if status != nil {
		status(runtime.id, true, false)
	}
	go func() {
		defer runtime.tasks.Done()
		var finalAssistant *agent.Message
		err := runtime.core.RunContent(ctx, content, func(event agent.Event) {
			if event.Type == agent.EventTurnEnd && event.Message != nil && len(event.Message.Content) > 0 {
				copy := cloneMessage(*event.Message)
				finalAssistant = &copy
			}
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
		if err == nil && finalAssistant != nil && runtime.summarizeTurn != nil {
			summaryCtx, summaryCancel := context.WithTimeout(context.Background(), 30*time.Second)
			summary, summaryErr := runtime.summarizeTurn(summaryCtx, *finalAssistant)
			summaryCancel()
			if summaryErr != nil {
				log.Printf("turn summary for session %s: %v", runtime.id, summaryErr)
				summary = "The agent completed a turn, but its result summary is unavailable."
			}
			runtime.publish(id, agent.Event{Type: agent.EventTurnSummary, Summary: summary})
			timestamp := time.Now().UnixMilli()
			if runtime.sessions != nil {
				if persistErr := runtime.sessions.AppendTurnSummary(summary, timestamp); persistErr != nil {
					log.Printf("persist turn summary for session %s: %v", runtime.id, persistErr)
				}
			}
			if runtime.completionPush != nil {
				runtime.completionPush(runtime.id, summary)
			}
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
	if runtime.sessions == nil {
		runtime.mu.Unlock()
		return errors.New("not enough context to compact")
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
	status := runtime.status
	runtime.mu.Unlock()
	if status != nil {
		status(runtime.id, true, false)
	}
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
	status := runtime.status
	runtime.mu.Unlock()
	if status != nil {
		status(runtime.id, false, false)
	}
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
	if runtime.sessions != nil {
		if err := runtime.sessions.SetCWD(workspace); err != nil {
			return err
		}
	}
	tools := builtInTools(workspace)
	if runtime.cron != nil {
		tools = builtInTools(workspace, cronTool(runtime.cron, runtime.id))
	}
	runtime.core.SetWorkingDirectory(workspace, systemprompt.Default(workspace), tools)
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
	if runtime.sessions != nil {
		if err := runtime.sessions.SetModel(model); err != nil {
			return err
		}
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
	if runtime.sessions != nil {
		if err := runtime.sessions.SetThinking(level); err != nil {
			return err
		}
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
	if runtime.sessions != nil {
		if err := runtime.sessions.SetYOLO(enabled); err != nil {
			return err
		}
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
func (runtime *sessionRuntime) setName(name *string) error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active {
		return errors.New("abort the current turn before naming the session")
	}
	if err := runtime.ensurePersistedLocked(); err != nil {
		return fmt.Errorf("persist draft session: %w", err)
	}
	return runtime.sessions.SetName(name)
}
func (runtime *sessionRuntime) ResolveApproval(id string, approved bool) bool {
	if !runtime.core.ResolveApproval(id, approved) {
		return false
	}
	runtime.mu.Lock()
	toolCallID := ""
	if runtime.inFlight.approval != nil && runtime.inFlight.approval.ApprovalID == id {
		toolCallID = runtime.inFlight.approval.ToolCallID
		runtime.inFlight.approval = nil
	}
	resolved := agent.Event{Type: agent.EventApprovalResolved, ApprovalID: id, ToolCallID: toolCallID}
	response := backendResponse{ID: runtime.inFlight.runID, Type: "event", Event: &resolved, Session: runtime.id}
	for subscriber := range runtime.subscribers {
		if !subscriber.enqueue(response) {
			delete(runtime.subscribers, subscriber)
			log.Printf("removed slow agent subscriber for session %s", runtime.id)
		}
	}
	active, status := runtime.active, runtime.status
	runtime.mu.Unlock()
	if status != nil {
		status(runtime.id, active, false)
	}
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
	runtime.core.CloseProviderSession()
	if runtime.sessions != nil {
		_ = runtime.sessions.Close()
	}
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
	subscribers      map[*subscriber]struct{}
	defaultID        string
	closed           bool
	done             chan struct{}
	shutdownComplete chan struct{}
	shutdownOnce     sync.Once
	push             *pushManager
	cron             *cronManager
}

func newRuntimeRegistry(root string, initial *sessionRuntime, factory agentFactory) *runtimeRegistry {
	var push *pushManager
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("PI_GO_PUSH_DISABLED")), "true") {
		var pushErr error
		push, pushErr = newPushManager()
		if pushErr != nil {
			log.Printf("push manager unavailable: %v", pushErr)
		}
	}
	registry := &runtimeRegistry{root: root, factory: factory, runtimes: map[string]*sessionRuntime{initial.id: initial}, subscribers: make(map[*subscriber]struct{}), defaultID: initial.id, done: make(chan struct{}), shutdownComplete: make(chan struct{}), push: push}
	initial.status = registry.broadcastStatus
	initial.approvalPush = registry.pushApproval
	initial.completionPush = registry.pushCompletion
	initial.summarizeTurn = registry.summarizeAssistantTurn
	return registry
}
func (registry *runtimeRegistry) enableCron() error {
	manager, err := newCronManager(registry.runCronJob)
	if err != nil {
		return err
	}
	registry.mu.Lock()
	registry.cron = manager
	runtimes := make([]*sessionRuntime, 0, len(registry.runtimes))
	for _, runtime := range registry.runtimes {
		runtimes = append(runtimes, runtime)
	}
	registry.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.bindCron(manager)
	}
	return nil
}

func (registry *runtimeRegistry) runCronJob(job cronJobRecord) string {
	runtime, err := registry.Attach(job.SessionID)
	if err != nil {
		log.Printf("cron job %d session %s unavailable: %v", job.ID, job.SessionID, err)
		return "session_unavailable"
	}
	runID := fmt.Sprintf("cron-%d-%d", job.ID, time.Now().UnixNano())
	err = runtime.StartPrompt(runID, []agent.ContentBlock{{Type: "text", Text: job.Prompt}})
	if err == nil {
		return "started"
	}
	if strings.Contains(err.Error(), "already running") {
		log.Printf("cron job %d skipped because session %s is busy", job.ID, job.SessionID)
		return "skipped_busy"
	}
	log.Printf("cron job %d could not start in session %s: %v", job.ID, job.SessionID, err)
	return "start_failed"
}

func (registry *runtimeRegistry) subscribe(subscriber *subscriber) {
	registry.mu.Lock()
	registry.subscribers[subscriber] = struct{}{}
	registry.mu.Unlock()
}
func (registry *runtimeRegistry) unsubscribe(subscriber *subscriber) {
	registry.mu.Lock()
	delete(registry.subscribers, subscriber)
	registry.mu.Unlock()
}
func (registry *runtimeRegistry) summarizeAssistantTurn(ctx context.Context, message agent.Message) (string, error) {
	if runtimeClassifierSettings == nil || runtimeClassifierProvider == nil {
		return "", errors.New("classifier settings manager is unavailable")
	}
	return summarizeAssistantTurn(ctx, runtimeClassifierProvider, runtimeClassifierSettings.Model(), message)
}

func (registry *runtimeRegistry) pushCompletion(sessionID, summary string) {
	if registry.push != nil {
		registry.push.NotifyCompletion(sessionID, summary)
	}
}

func (registry *runtimeRegistry) pushApproval(sessionID, summary string) {
	if registry.push != nil {
		registry.push.NotifyApproval(sessionID, summary)
	}
}

func (registry *runtimeRegistry) broadcastStatus(id string, active, waitingInput bool) {
	if registry.push != nil {
		registry.push.ObserveStatus(id, active, waitingInput)
	}
	response := backendResponse{Type: "session_status", Session: id, Active: active, WaitingInput: waitingInput}
	registry.mu.Lock()
	for subscriber := range registry.subscribers {
		if !subscriber.enqueue(response) {
			delete(registry.subscribers, subscriber)
		}
	}
	registry.mu.Unlock()
}
func (registry *runtimeRegistry) Default() *sessionRuntime {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return registry.runtimes[registry.defaultID]
}
func (registry *runtimeRegistry) List() ([]session.Entry, error) {
	entries, err := session.List(registry.root)
	if err != nil {
		return nil, err
	}
	registry.mu.Lock()
	runtimes := make(map[string]*sessionRuntime, len(registry.runtimes))
	for id, runtime := range registry.runtimes {
		runtimes[id] = runtime
	}
	registry.mu.Unlock()
	for index := range entries {
		if runtime := runtimes[entries[index].ID]; runtime != nil {
			entries[index].Active = runtime.Busy()
		}
	}
	return entries, nil
}
func (registry *runtimeRegistry) ListPage(offset, limit int) ([]session.Entry, int, bool, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	// Ask for one extra UUIDv7 filename so hasMore is known without scanning
	// metadata for every session in the directory.
	entries, err := session.ListPage(registry.root, offset, limit+1)
	if err != nil {
		return nil, 0, false, err
	}
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	registry.mu.Lock()
	runtimes := make(map[string]*sessionRuntime, len(registry.runtimes))
	for id, runtime := range registry.runtimes {
		runtimes[id] = runtime
	}
	registry.mu.Unlock()
	for index := range entries {
		if runtime := runtimes[entries[index].ID]; runtime != nil {
			entries[index].Active = runtime.Busy()
		}
	}
	nextOffset := offset + len(entries)
	return entries, nextOffset, hasMore, nil
}

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
	runtime.bindCron(registry.cron)
	runtime.status = registry.broadcastStatus
	runtime.approvalPush = registry.pushApproval
	runtime.completionPush = registry.pushCompletion
	runtime.summarizeTurn = registry.summarizeAssistantTurn
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
	runtime.bindCron(registry.cron)
	runtime.status = registry.broadcastStatus
	runtime.approvalPush = registry.pushApproval
	runtime.completionPush = registry.pushCompletion
	runtime.summarizeTurn = registry.summarizeAssistantTurn
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
		if registry.cron != nil {
			if err := registry.cron.close(); err != nil {
				log.Printf("stop cron scheduler: %v", err)
			}
		}
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
	registry      *runtimeRegistry
	runtime       *sessionRuntime
	subscriber    *subscriber
	pushDeviceID  string
	pushPairingID string
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
	client.registry.unsubscribe(client.subscriber)
	if client.runtime != nil {
		client.runtime.unsubscribe(client.subscriber)
	}
	client.subscriber.close()
}

func serveClient(registry *runtimeRegistry, input io.Reader, output io.Writer, closer io.Closer, authPolicy *clientAuthPolicy) error {
	var closeIO func()
	if closer != nil {
		closeIO = func() { _ = closer.Close() }
	}
	subscriber := newSubscriber(closeIO)
	client := &clientConnection{registry: registry, subscriber: subscriber}
	writerDone := make(chan struct{})
	var secureMu sync.RWMutex
	var secure *secureSession
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
				response := outbound.response
				secureMu.RLock()
				currentSecure := secure
				secureMu.RUnlock()
				var err error
				if currentSecure != nil && response.Type != "encrypted" {
					response, err = currentSecure.encrypt(response)
				}
				if err == nil {
					err = encoder(response)
				}
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
	if authPolicy != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, negotiatedSecure, authErr := authPolicy.authenticate(ctx, decoder, func(response backendResponse) error {
			return subscriber.enqueueAndWait(response)
		})
		cancel()
		if authErr != nil {
			_ = subscriber.enqueueAndWait(backendResponse{Type: "auth_failed", Error: "authentication failed"})
			return authErr
		}
		secureMu.Lock()
		secure = negotiatedSecure
		secureMu.Unlock()
		if authenticated, ok := input.(interface{ SetAuthenticated() }); ok {
			authenticated.SetAuthenticated()
		}
	}
	registry.subscribe(subscriber)
	client.attach(registry.Default())
	for {
		var command backendCommand
		if err := decoder(&command); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode agent-backend command: %w", err)
		}
		secureMu.RLock()
		currentSecure := secure
		secureMu.RUnlock()
		if currentSecure != nil {
			var plain backendCommand
			if err := currentSecure.decrypt(command, &plain); err != nil {
				return err
			}
			command = plain
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

var runtimeAuthManager *codexAuthManager
var runtimeClassifierSettings *classifierSettings
var runtimeClassifierProvider agent.Provider

func authStatusResponse(id, command string, status codexAuthStatus) backendResponse {
	authenticated := status.Authenticated
	return backendResponse{
		ID: id, Type: "response", Command: command, Success: true,
		Authenticated: &authenticated, AccountID: status.AccountID, ExpiresAt: status.ExpiresAt,
	}
}

func (client *clientConnection) reply(response backendResponse) { client.subscriber.enqueue(response) }
func (client *clientConnection) handle(command backendCommand) error {
	runtime := client.runtime
	switch command.Type {
	case "push_hello":
		if command.Platform != "ios" && command.Platform != "android" && command.Platform != "macos" {
			return errors.New("push pairing requires an iOS, Android, or macOS client")
		}
		if client.registry.push == nil {
			return errors.New("push manager is unavailable")
		}
		if strings.TrimSpace(command.DeviceID) == "" || strings.TrimSpace(command.ConnectionChallenge) == "" {
			return errors.New("push identity and connection challenge are required")
		}
		agentID, publicKey, fingerprint, err := client.registry.push.Identity(context.Background())
		if err != nil {
			return err
		}
		proof, err := client.registry.push.ConnectionProof(command.ConnectionChallenge, command.DeviceID)
		if err != nil {
			return err
		}
		authorized, err := client.registry.push.DeviceAuthorized(context.Background(), command.DeviceID)
		if err != nil {
			return err
		}
		client.pushDeviceID = command.DeviceID
		if err := client.registry.push.SetDevicePlatform(command.DeviceID, command.Platform); err != nil {
			return err
		}
		response := backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, AgentID: agentID, AgentPublicKey: &publicKey, AgentFingerprint: fingerprint, ConnectionProof: proof, DeviceID: command.DeviceID, PushAuthorized: authorized}
		if authorized {
			key, keySignature, keyErr := client.registry.push.PushKey(context.Background(), command.DeviceID)
			if keyErr != nil {
				return keyErr
			}
			response.PushKeyID, response.PushKey, response.PushKeySignature = key.KeyID, key.Key, keySignature
		}
		client.reply(response)
	case "push_authorizations":
		if client.registry.push == nil {
			return errors.New("push manager is unavailable")
		}
		authorizations, err := client.registry.push.ListAuthorizations(context.Background())
		if err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, PushAuthorizations: authorizations})
	case "push_authorization_revoke":
		if client.registry.push == nil {
			return errors.New("push manager is unavailable")
		}
		if strings.TrimSpace(command.DeviceID) == "" {
			return errors.New("deviceId must not be empty")
		}
		if err := client.registry.push.RevokeAuthorization(context.Background(), command.DeviceID); err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, DeviceID: command.DeviceID})
	case "push_pair":
		if client.pushDeviceID == "" || command.DeviceID != client.pushDeviceID {
			return errors.New("complete push hello before pairing")
		}
		pairing, err := client.registry.push.CreatePairing(context.Background(), client.pushDeviceID)
		if err != nil {
			return err
		}
		client.pushPairingID = pairing.PairingID
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, DeviceID: client.pushDeviceID, PairingID: pairing.PairingID, VerificationCode: pairing.VerificationCode, Scopes: pairing.Scopes, PairingExpiresAt: pairing.ExpiresAt})
	case "push_pair_complete":
		if client.pushDeviceID == "" || command.PairingID == "" || command.PairingID != client.pushPairingID {
			return errors.New("push pairing is not pending for this client")
		}
		authorized, err := client.registry.push.CompletePairing(context.Background(), client.pushDeviceID, command.PairingID)
		if err != nil {
			return err
		}
		if !authorized {
			return errors.New("push pairing has not been approved")
		}
		key, keySignature, err := client.registry.push.PushKey(context.Background(), client.pushDeviceID)
		if err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, DeviceID: client.pushDeviceID, PairingID: command.PairingID, PushAuthorized: true, PushKeyID: key.KeyID, PushKey: key.Key, PushKeySignature: keySignature})
	case "get_state":
		runtime.sendSnapshot(client.subscriber, command.ID, command.Type)
	case "get_transcript_history":
		if command.Before <= 0 {
			return errors.New("history before cursor is required")
		}
		runtime.sendHistory(client.subscriber, command.ID, command.Before, command.Limit)
	case "list_sessions":
		entries, nextOffset, hasMore, err := client.registry.ListPage(command.Before, command.Limit)
		if err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Sessions: entries, Session: runtime.id, SessionsOffset: nextOffset, SessionsHasMore: hasMore})
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
	case "get_classifier_config":
		if runtimeClassifierSettings == nil {
			return errors.New("classifier settings manager is unavailable")
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, ClassifierModel: runtimeClassifierSettings.Model()})
	case "set_classifier_model":
		if runtimeClassifierSettings == nil || runtimeAuthManager == nil {
			return errors.New("classifier settings manager is unavailable")
		}
		models, err := configuredModels(runtimeAuthManager)
		if err != nil {
			return err
		}
		if err := runtimeClassifierSettings.SetModel(command.ClassifierModel, models); err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, ClassifierModel: runtimeClassifierSettings.Model()})
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
		if err := runtime.setName(command.Name); err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Session: runtime.id})
	case "get_provider_config":
		if runtimeAuthManager == nil {
			return errors.New("provider configuration manager is unavailable")
		}
		configs, err := runtimeAuthManager.APIConfigs()
		if err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Provider: "api", ProviderConfigs: configs})
	case "set_provider_config":
		if runtimeAuthManager == nil {
			return errors.New("provider configuration manager is unavailable")
		}
		name := command.ProviderName
		if name == "" && command.Provider != "api" {
			name = command.Provider // legacy qwen-code-plan client
		}
		if command.DeleteProvider {
			if err := runtimeAuthManager.DeleteAPIConfig(name); err != nil {
				return err
			}
			configs, err := runtimeAuthManager.APIConfigs()
			if err != nil {
				return err
			}
			client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Provider: "api", ProviderConfigs: configs})
			break
		}
		update := apiConfig{APIKey: command.APIKey, Protocol: command.Protocol, OpenAIBaseURL: command.OpenAIBaseURL, AnthropicBaseURL: command.AnthropicBaseURL, DefaultModel: command.DefaultModel, Models: command.Models}
		config, err := runtimeAuthManager.SetAPIConfig(name, update, strings.TrimSpace(command.APIKey) == "" && !command.ClearAPIKey, command.ClearAPIKey)
		if err != nil {
			return err
		}
		configs, err := runtimeAuthManager.APIConfigs()
		if err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Provider: "api", ProviderConfig: &config, ProviderConfigs: configs})
	case "list_models":
		if runtimeAuthManager == nil {
			return errors.New("provider configuration manager is unavailable")
		}
		models, err := configuredModels(runtimeAuthManager)
		if err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Models: models})
	case "fetch_provider_models":
		if runtimeAuthManager == nil {
			return errors.New("provider configuration manager is unavailable")
		}
		name := command.ProviderName
		if name == "" && command.Provider != "api" {
			name = command.Provider
		}
		models, err := runtimeAuthManager.FetchAPIModels(context.Background(), name)
		if err != nil {
			return err
		}
		current, err := runtimeAuthManager.APIConfig(name)
		if err != nil {
			return err
		}
		discovered := make([]string, 0, len(models))
		for _, model := range models {
			discovered = append(discovered, strings.TrimPrefix(model.ID, name+"/"))
		}
		updated, err := runtimeAuthManager.SetAPIConfig(name, apiConfig{Protocol: current.Protocol, OpenAIBaseURL: current.OpenAIBaseURL, AnthropicBaseURL: current.AnthropicBaseURL, DefaultModel: current.DefaultModel, Models: discovered}, true, false)
		if err != nil {
			return err
		}
		configs, err := runtimeAuthManager.APIConfigs()
		if err != nil {
			return err
		}
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Provider: "api", ProviderConfig: &updated, ProviderConfigs: configs, Models: models})
	case "auth_status":
		if runtimeAuthManager == nil {
			return errors.New("authentication manager is unavailable")
		}
		status, err := runtimeAuthManager.Status()
		if err != nil {
			return err
		}
		client.reply(authStatusResponse(command.ID, command.Type, status))
	case "auth_login":
		if runtimeAuthManager == nil {
			return errors.New("authentication manager is unavailable")
		}
		loginCtx, err := runtimeAuthManager.BeginLogin()
		if err != nil {
			return err
		}
		go func() {
			defer runtimeAuthManager.EndLogin()
			status, err := runtimeAuthManager.LoginDeviceCode(loginCtx, func(code codexDeviceCode) {
				client.reply(backendResponse{
					ID: command.ID, Type: "auth_device_code", Command: command.Type,
					UserCode: code.UserCode, VerificationURI: code.VerificationURI, ExpiresAt: code.ExpiresAt,
				})
			})
			if err != nil {
				client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Error: err.Error()})
				return
			}
			client.reply(authStatusResponse(command.ID, command.Type, status))
		}()
	case "auth_cancel":
		if runtimeAuthManager == nil {
			return errors.New("authentication manager is unavailable")
		}
		active := runtimeAuthManager.CancelLogin()
		client.reply(backendResponse{ID: command.ID, Type: "response", Command: command.Type, Success: true, Active: active})
	case "auth_logout":
		if runtimeAuthManager == nil {
			return errors.New("authentication manager is unavailable")
		}
		if err := runtimeAuthManager.Logout(); err != nil {
			return err
		}
		client.reply(authStatusResponse(command.ID, command.Type, codexAuthStatus{}))
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
