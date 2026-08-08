package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
	"github.com/peterw22/pi-go/internal/session"
)

type detachedTestProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newDetachedTestProvider() *detachedTestProvider {
	return &detachedTestProvider{started: make(chan struct{}), release: make(chan struct{})}
}
func (provider *detachedTestProvider) Stream(ctx context.Context, _ agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
	events := make(chan agent.ProviderEvent, 3)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: "working"}
		provider.once.Do(func() { close(provider.started) })
		select {
		case <-provider.release:
			events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: " done"}
			events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "stop", Usage: agent.Usage{Input: 4, Output: 2, TotalTokens: 6}}
		case <-ctx.Done():
			errs <- ctx.Err()
		}
	}()
	return events, errs
}
func testSessionRuntime(t *testing.T, provider agent.Provider) (*sessionRuntime, string) {
	t.Helper()
	root := t.TempDir()
	store, err := session.New(root, "test", "low")
	if err != nil {
		t.Fatal(err)
	}
	core, err := agent.New(agent.Config{Model: "test", Thinking: "low", WorkingDirectory: root, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return newSessionRuntime(store.ID(), core, session.NewController(root, store)), root
}
func waitRuntimeIdle(t *testing.T, runtime *sessionRuntime) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for runtime.Busy() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if runtime.Busy() {
		t.Fatal("runtime did not become idle")
	}
}
func drainSubscriber(subscriber *subscriber) {
	for {
		select {
		case <-subscriber.responses:
		default:
			return
		}
	}
}

type detachedToolProvider struct {
	mu    sync.Mutex
	calls int
}

func (provider *detachedToolProvider) Stream(_ context.Context, _ agent.Request) (<-chan agent.ProviderEvent, <-chan error) {
	provider.mu.Lock()
	provider.calls++
	call := provider.calls
	provider.mu.Unlock()
	events := make(chan agent.ProviderEvent, 2)
	if call == 1 {
		events <- agent.ProviderEvent{Type: agent.ProviderToolCall, ToolCall: agent.ContentBlock{Type: "toolCall", ID: "detached-tool", Name: "wait"}}
		events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "toolUse"}
	} else {
		events <- agent.ProviderEvent{Type: agent.ProviderTextDelta, Delta: "tool completed"}
		events <- agent.ProviderEvent{Type: agent.ProviderDone, StopReason: "stop"}
	}
	close(events)
	errs := make(chan error)
	close(errs)
	return events, errs
}

func TestDetachedBashLikeToolContinuesAfterDisconnect(t *testing.T) {
	provider := &detachedToolProvider{}
	root := t.TempDir()
	store, err := session.New(root, "test", "low")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	core, err := agent.New(agent.Config{Model: "test", Provider: provider, Tools: []agent.Tool{{Name: "wait", Execute: func(ctx context.Context, _ map[string]any, _ func(agent.ToolResult)) (agent.ToolResult, error) {
		close(started)
		select {
		case <-release:
			return agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: "detached output"}}}, nil
		case <-ctx.Done():
			return agent.ToolResult{}, ctx.Err()
		}
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newSessionRuntime(store.ID(), core, session.NewController(root, store))
	defer runtime.close()
	subscriber := newSubscriber(nil)
	runtime.subscribe(subscriber)
	drainSubscriber(subscriber)
	if err := runtime.StartPrompt("tool-run", []agent.ContentBlock{{Type: "text", Text: "run tool"}}); err != nil {
		t.Fatal(err)
	}
	<-started
	runtime.unsubscribe(subscriber)
	subscriber.close()
	close(release)
	waitRuntimeIdle(t, runtime)
	state := core.Snapshot()
	found := false
	for _, message := range state.Messages {
		if message.Role == agent.RoleToolResult && message.Content[0].Text == "detached output" {
			found = true
		}
	}
	if !found {
		t.Fatalf("state=%#v", state)
	}
}

type runtimeDenyGuard struct{}

func (runtimeDenyGuard) Check(context.Context, agent.GuardRequest) agent.GuardDecision {
	return agent.GuardDecision{Reason: "needs approval", Description: "guarded tool"}
}

func TestPendingApprovalIsCaughtUpAndFirstResponseWins(t *testing.T) {
	provider := &detachedToolProvider{}
	root := t.TempDir()
	store, err := session.New(root, "test", "low")
	if err != nil {
		t.Fatal(err)
	}
	core, err := agent.New(agent.Config{Model: "test", Provider: provider, ToolGuard: runtimeDenyGuard{}, AllowApproval: true, Tools: []agent.Tool{{Name: "wait", Execute: func(context.Context, map[string]any, func(agent.ToolResult)) (agent.ToolResult, error) {
		return agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: "ran"}}}, nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newSessionRuntime(store.ID(), core, session.NewController(root, store))
	defer runtime.close()
	first := newSubscriber(nil)
	runtime.subscribe(first)
	drainSubscriber(first)
	if err := runtime.StartPrompt("approval-run", []agent.ContentBlock{{Type: "text", Text: "run"}}); err != nil {
		t.Fatal(err)
	}
	var approvalID string
	deadline := time.After(3 * time.Second)
	for approvalID == "" {
		select {
		case outbound := <-first.responses:
			if outbound.response.Event != nil && outbound.response.Event.Type == agent.EventApprovalRequired {
				approvalID = outbound.response.Event.ApprovalID
			}
		case <-deadline:
			t.Fatal("approval was not requested")
		}
	}
	second := newSubscriber(nil)
	runtime.subscribe(second)
	caughtUp := false
	for len(second.responses) > 0 {
		outbound := <-second.responses
		if outbound.response.Event != nil && outbound.response.Event.Type == agent.EventApprovalRequired && outbound.response.Event.ApprovalID == approvalID {
			caughtUp = true
		}
	}
	if !caughtUp {
		t.Fatal("joining subscriber did not receive pending approval")
	}
	if !runtime.ResolveApproval(approvalID, false) {
		t.Fatal("first approval response failed")
	}
	if runtime.ResolveApproval(approvalID, true) {
		t.Fatal("second approval response unexpectedly won")
	}
	waitRuntimeIdle(t, runtime)
}

func TestBusySessionRejectsMutationAndExplicitAbortCancels(t *testing.T) {
	provider := newDetachedTestProvider()
	runtime, _ := testSessionRuntime(t, provider)
	defer runtime.close()
	if err := runtime.StartPrompt("busy", []agent.ContentBlock{{Type: "text", Text: "wait"}}); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	if err := runtime.StartPrompt("second", []agent.ContentBlock{{Type: "text", Text: "no"}}); err == nil {
		t.Fatal("second prompt was accepted")
	}
	if err := runtime.setModel("other"); err == nil {
		t.Fatal("model mutation was accepted while busy")
	}
	if !runtime.Abort() {
		t.Fatal("abort did not report active task")
	}
	waitRuntimeIdle(t, runtime)
}

func TestDetachedRunContinuesAfterInitiatingSubscriberDisconnects(t *testing.T) {
	provider := newDetachedTestProvider()
	runtime, _ := testSessionRuntime(t, provider)
	defer runtime.close()
	subscriber := newSubscriber(nil)
	runtime.subscribe(subscriber)
	drainSubscriber(subscriber)
	if err := runtime.StartPrompt("run-1", []agent.ContentBlock{{Type: "text", Text: "work"}}); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	runtime.unsubscribe(subscriber)
	subscriber.close()
	close(provider.release)
	waitRuntimeIdle(t, runtime)
	state := runtime.core.Snapshot()
	if len(state.Messages) != 2 || state.Messages[1].Content[0].Text != "working done" {
		t.Fatalf("state=%#v", state)
	}
}

func TestSubscribersReceiveSameLiveEvents(t *testing.T) {
	provider := newDetachedTestProvider()
	runtime, _ := testSessionRuntime(t, provider)
	defer runtime.close()
	first, second := newSubscriber(nil), newSubscriber(nil)
	runtime.subscribe(first)
	runtime.subscribe(second)
	drainSubscriber(first)
	drainSubscriber(second)
	if err := runtime.StartPrompt("shared", []agent.ContentBlock{{Type: "text", Text: "work"}}); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	close(provider.release)
	collect := func(subscriber *subscriber) []agent.EventType {
		var result []agent.EventType
		deadline := time.After(3 * time.Second)
		for {
			select {
			case outbound := <-subscriber.responses:
				response := outbound.response
				if response.Event != nil {
					result = append(result, response.Event.Type)
					if response.Event.Type == agent.EventAgentEnd {
						return result
					}
				}
			case <-deadline:
				t.Fatal("timed out waiting for shared events")
			}
		}
	}
	firstEvents := collect(first)
	secondEvents := collect(second)
	if len(firstEvents) != len(secondEvents) {
		t.Fatalf("event lengths %v / %v", firstEvents, secondEvents)
	}
	for i := range firstEvents {
		if firstEvents[i] != secondEvents[i] {
			t.Fatalf("events %v / %v", firstEvents, secondEvents)
		}
	}
}

func TestJoiningMidStreamReceivesSyntheticCatchUpThenCompletion(t *testing.T) {
	provider := newDetachedTestProvider()
	runtime, _ := testSessionRuntime(t, provider)
	defer runtime.close()
	first := newSubscriber(nil)
	runtime.subscribe(first)
	drainSubscriber(first)
	if err := runtime.StartPrompt("catchup", []agent.ContentBlock{{Type: "text", Text: "work"}}); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	// Wait until the cumulative assistant delta has reached in-flight state.
	deadline := time.Now().Add(time.Second)
	for {
		runtime.mu.Lock()
		ready := runtime.inFlight.assistant != nil
		runtime.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("assistant did not enter in-flight state")
		}
		time.Sleep(time.Millisecond)
	}
	second := newSubscriber(nil)
	runtime.subscribe(second)
	var catchup []agent.EventType
	for len(second.responses) > 0 {
		outbound := <-second.responses
		response := outbound.response
		if response.Event != nil {
			catchup = append(catchup, response.Event.Type)
		}
	}
	want := []agent.EventType{agent.EventAgentStart, agent.EventMessageStart, agent.EventMessageUpdate}
	if len(catchup) < len(want) {
		t.Fatalf("catchup=%v", catchup)
	}
	for i := range want {
		if catchup[i] != want[i] {
			t.Fatalf("catchup=%v", catchup)
		}
	}
	close(provider.release)
	deadlineTimer := time.After(3 * time.Second)
	for {
		select {
		case outbound := <-second.responses:
			response := outbound.response
			if response.Event != nil && response.Event.Type == agent.EventAgentEnd {
				return
			}
		case <-deadlineTimer:
			t.Fatal("joining subscriber missed completion")
		}
	}
}

func TestIndependentSessionRuntimesExecuteConcurrently(t *testing.T) {
	provider := newDetachedTestProvider()
	initial, root := testSessionRuntime(t, provider)
	factory := func(workspace, model, thinking string, messages []agent.Message, usage agent.Usage) (*agent.Agent, error) {
		core, err := agent.New(agent.Config{Model: model, Thinking: thinking, WorkingDirectory: workspace, Provider: provider})
		if err == nil {
			core.Restore(messages, model, thinking, usage)
		}
		return core, err
	}
	registry := newRuntimeRegistry(root, initial, factory)
	defer registry.Shutdown()
	second, err := registry.New(initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := initial.StartPrompt("one", []agent.ContentBlock{{Type: "text", Text: "one"}}); err != nil {
		t.Fatal(err)
	}
	if err := second.StartPrompt("two", []agent.ContentBlock{{Type: "text", Text: "two"}}); err != nil {
		t.Fatal(err)
	}
	if !initial.Busy() || !second.Busy() {
		t.Fatal("both independent sessions were not active concurrently")
	}
	close(provider.release)
	waitRuntimeIdle(t, initial)
	waitRuntimeIdle(t, second)
	if len(initial.core.Snapshot().Messages) != 2 || len(second.core.Snapshot().Messages) != 2 {
		t.Fatal("independent session state was not retained")
	}
}

func TestYOLOMutationPersistsAndIsRejectedWhileBusy(t *testing.T) {
	provider := newDetachedTestProvider()
	runtime, _ := testSessionRuntime(t, provider)
	defer runtime.close()
	if err := runtime.setYOLO(true); err != nil {
		t.Fatal(err)
	}
	if !runtime.core.Snapshot().YOLO {
		t.Fatal("YOLO mode was not enabled")
	}
	if err := runtime.StartPrompt("busy-yolo", []agent.ContentBlock{{Type: "text", Text: "wait"}}); err != nil {
		t.Fatal(err)
	}
	<-provider.started
	if err := runtime.setYOLO(false); err == nil {
		t.Fatal("YOLO mutation was accepted while busy")
	}
	runtime.Abort()
	waitRuntimeIdle(t, runtime)
}

func TestSlowSubscriberIsRemovedWithoutBlockingRuntime(t *testing.T) {
	runtime, _ := testSessionRuntime(t, newDetachedTestProvider())
	defer runtime.close()
	slow := newSubscriber(nil)
	runtime.subscribe(slow)
	drainSubscriber(slow)
	for i := 0; i < subscriberQueueSize+1; i++ {
		runtime.publish("slow", agent.Event{Type: agent.EventMessageUpdate})
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("slow subscriber was not removed")
	}
}
