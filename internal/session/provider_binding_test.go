package session

import (
	"path/filepath"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

func TestProviderBindingPersistAndClear(t *testing.T) {
	root := t.TempDir()
	store, err := NewAt(root, root, "claude/claude-sonnet-5", "high")
	if err != nil {
		t.Fatal(err)
	}
	id := store.ID()
	controller := NewController(root, store)
	if err := controller.Append(agent.Message{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "first"}}}, agent.Usage{}); err != nil {
		t.Fatal(err)
	}
	bind := ProviderBinding{Provider: "claude", Model: "claude/claude-sonnet-5", CWD: root, ConversationID: "11111111-2222-3333-4444-555555555555"}
	if err := controller.SetProviderBinding(bind); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".pi-go", "sessions", id+".jsonl")
	resumed, header, messages, _, err := Resume(path)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if header.Model != bind.Model || len(messages) != 1 || messages[0].Content[0].Text != "first" {
		t.Fatalf("header=%#v messages=%#v", header, messages)
	}
	got, err := ProviderBindingForSession(root, id, "claude", bind.Model, root)
	if err != nil || got.ConversationID != bind.ConversationID {
		t.Fatalf("binding=%#v err=%v", got, err)
	}
	if _, err := ProviderBindingForSession(root, id, "claude", "claude/claude-opus-5-5", root); err == nil {
		t.Fatal("wrong model resumed")
	}
	if _, err := ProviderBindingForSession(root, id, "claude", bind.Model, t.TempDir()); err == nil {
		t.Fatal("wrong workspace resumed")
	}
	if err := resumed.SetProviderBinding(ProviderBinding{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	got, err = ProviderBindingForSession(root, id, "claude", bind.Model, root)
	if err != nil || got.ConversationID != "" {
		t.Fatalf("cleared binding=%#v err=%v", got, err)
	}
}
