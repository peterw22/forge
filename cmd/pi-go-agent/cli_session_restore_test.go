package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/peterw22/forge/internal/agent"
	"github.com/peterw22/forge/internal/session"
)

func TestCLITranscriptAndBindingSurviveSessionSwitch(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	factory := func(_, model, thinking string, messages []agent.Message, usage agent.Usage) (*agent.Agent, error) {
		core, err := agent.New(agent.Config{Model: model, Thinking: thinking, WorkingDirectory: workspace, Provider: immediateTestProvider{}})
		if err == nil {
			core.Restore(messages, model, thinking, usage)
		}
		return core, err
	}
	store, err := session.NewAt(root, workspace, "claude/claude-sonnet-5", "high")
	if err != nil {
		t.Fatal(err)
	}
	core, err := factory(workspace, "claude/claude-sonnet-5", "high", nil, agent.Usage{})
	if err != nil {
		t.Fatal(err)
	}
	original := newSessionRuntime(store.ID(), core, session.NewController(root, store))
	registry := newRuntimeRegistry(root, original, factory)
	if err := original.StartPrompt("first", []agent.ContentBlock{{Type: "text", Text: "What was my first prompt?"}}); err != nil {
		t.Fatal(err)
	}
	waitRuntimeIdle(t, original)
	if err := original.sessions.SetProviderBinding(session.ProviderBinding{Provider: "claude", Model: "claude/claude-sonnet-5", CWD: workspace, ConversationID: "11111111-2222-3333-4444-555555555555"}); err != nil {
		t.Fatal(err)
	}
	second, err := registry.New(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.StartPrompt("second", []agent.ContentBlock{{Type: "text", Text: "Other session"}}); err != nil {
		t.Fatal(err)
	}
	waitRuntimeIdle(t, second)
	registry.Shutdown() // simulate a process restart: no in-memory provider map
	reopened, header, messages, _, err := session.Resume(filepath.Join(root, ".pi-go", "sessions", original.id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if header.Model != "claude/claude-sonnet-5" || len(messages) != 2 || messages[0].Content[0].Text != "What was my first prompt?" {
		t.Fatalf("history lost: %#v %#v", header, messages)
	}
	got, err := session.ReadProviderBinding(reopened.Path(), "claude")
	if err != nil || got.ConversationID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("binding=%#v err=%v", got, err)
	}
	other, err := session.ReadProviderBinding(filepath.Join(root, ".pi-go", "sessions", second.id+".jsonl"), "claude")
	if err != nil || other.ConversationID != "" {
		t.Fatalf("second session reused binding: %#v %v", other, err)
	}
}

func TestCLIBindingCallbacksPersistToolTranscript(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewAt(root, root, "agy/gemini-3.8-flash-low", "high")
	if err != nil {
		t.Fatal(err)
	}
	core, err := agent.New(agent.Config{Model: "agy/gemini-3.8-flash-low", Thinking: "high", WorkingDirectory: root, Provider: immediateTestProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newSessionRuntime(store.ID(), core, session.NewController(root, store))
	defer runtime.close()
	runtime.persistProviderToolEvent(agent.Event{Type: agent.EventToolExecutionStart, ToolCallID: "mcp-1", ToolName: "read", Arguments: map[string]any{"path": "README.md"}})
	runtime.persistProviderToolEvent(agent.Event{Type: agent.EventToolExecutionEnd, ToolCallID: "mcp-1", ToolName: "read", Result: &agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: "contents"}}}})
	binding := session.ProviderBinding{Provider: "agy", Model: "agy/gemini-3.8-flash-low", CWD: root, ConversationID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	if err := runtime.sessions.SetProviderBinding(binding); err != nil {
		t.Fatal(err)
	}
	_, _, messages, _, err := session.Resume(runtime.sessions.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content[0].Type != "toolCall" || messages[1].Content[0].Text != "contents" {
		t.Fatalf("tool transcript=%#v", messages)
	}
	live := runtime.core.Snapshot().Messages
	if len(live) != 2 || live[0].Content[0].Type != "toolCall" || live[1].Content[0].Text != "contents" {
		t.Fatalf("live transcript=%#v", live)
	}
}

func TestCLIToolResultKeepsDetailsForReload(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewAt(root, root, "claude/claude-sonnet-5", "high")
	if err != nil {
		t.Fatal(err)
	}
	core, err := agent.New(agent.Config{Model: "claude/claude-sonnet-5", WorkingDirectory: root, Provider: immediateTestProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newSessionRuntime(store.ID(), core, session.NewController(root, store))
	defer runtime.close()
	details := map[string]any{"path": "main.go", "oldText": "old", "newText": "new", "startLine": 8}
	runtime.persistProviderToolEvent(agent.Event{Type: agent.EventToolExecutionStart, ToolCallID: "mcp-1", ToolName: "replace", Arguments: map[string]any{"path": "main.go"}})
	runtime.persistProviderToolEvent(agent.Event{Type: agent.EventToolExecutionEnd, ToolCallID: "mcp-1", ToolName: "replace", Result: &agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: "Replaced one text match in main.go at line 8"}}, Details: details}})
	_, _, messages, _, err := session.Resume(runtime.sessions.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("tool transcript=%#v", messages)
	}
	// Forge draws the diff of a reloaded replace from these details.
	stored, ok := messages[1].ToolDetails.(map[string]any)
	if !ok || stored["oldText"] != "old" || stored["newText"] != "new" || stored["startLine"] != float64(8) {
		t.Fatalf("stored details=%#v", messages[1].ToolDetails)
	}
	live, ok := runtime.core.Snapshot().Messages[1].ToolDetails.(map[string]any)
	if !ok || live["oldText"] != "old" {
		t.Fatalf("live details=%#v", runtime.core.Snapshot().Messages[1].ToolDetails)
	}
}

func TestParallelCLIProviderToolEventsKeepTranscriptConsistent(t *testing.T) {
	root := t.TempDir()
	store, err := session.NewAt(root, root, "claude/claude-sonnet-5", "high")
	if err != nil {
		t.Fatal(err)
	}
	core, err := agent.New(agent.Config{Model: "claude/claude-sonnet-5", WorkingDirectory: root, Provider: immediateTestProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newSessionRuntime(store.ID(), core, session.NewController(root, store))
	defer runtime.close()
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			id := fmt.Sprintf("mcp-%d", index)
			runtime.persistProviderToolEvent(agent.Event{Type: agent.EventToolExecutionStart, ToolCallID: id, ToolName: "read", Arguments: map[string]any{"path": "file"}})
			runtime.persistProviderToolEvent(agent.Event{Type: agent.EventToolExecutionEnd, ToolCallID: id, ToolName: "read", Result: &agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: "ok"}}}})
		}(i)
	}
	group.Wait()
	_, _, disk, _, err := session.Resume(runtime.sessions.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(disk) != 24 || len(runtime.core.Snapshot().Messages) != 24 {
		t.Fatalf("disk=%d memory=%d", len(disk), len(runtime.core.Snapshot().Messages))
	}
}

var _ = context.Background
