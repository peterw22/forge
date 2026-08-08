package session

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
)

func TestRepairDanglingToolCalls(t *testing.T) {
	messages := []agent.Message{{Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Type: "toolCall", ID: "call_1", Name: "read"}}}}
	repaired := repairDanglingToolCalls(messages)
	if len(repaired) != 1 || repaired[0].ToolCallID != "call_1" || !repaired[0].IsError {
		t.Fatalf("repair = %#v", repaired)
	}
	messages = append(messages, repaired[0])
	if again := repairDanglingToolCalls(messages); len(again) != 0 {
		t.Fatalf("duplicate repair = %#v", again)
	}
}

func TestResumeUsesHeaderSettingsAsFallbackAndLatestSettingsOverride(t *testing.T) {
	tests := []struct {
		name, extra, wantModel, wantThinking, wantCWD string
		wantYOLO                                      bool
	}{
		{"header fallback", "", "gpt-original", "high", "/old", true},
		{"settings override", "{\"type\":\"session_settings\",\"model\":\"gpt-new\"}\n{\"type\":\"session_settings\",\"thinking\":\"max\",\"cwd\":\"/new\",\"yolo\":false}\n", "gpt-new", "max", "/new", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			body := "{\"type\":\"session\",\"version\":1,\"CWD\":\"/old\",\"Model\":\"gpt-original\",\"Thinking\":\"high\",\"yolo\":true,\"createdAt\":\"2024-01-01T00:00:00Z\"}\n" + test.extra
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			store, header, _, _, err := Resume(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if header.Model != test.wantModel || header.Thinking != test.wantThinking || header.CWD != test.wantCWD || header.YOLO != test.wantYOLO {
				t.Fatalf("settings = %q/%q/%q/%v, want %q/%q/%q/%v", header.Model, header.Thinking, header.CWD, header.YOLO, test.wantModel, test.wantThinking, test.wantCWD, test.wantYOLO)
			}
		})
	}
}

func TestResumeUsesLatestCompactionCheckpointAndKeepsCumulativeUsage(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir, "model", "high")
	if err != nil {
		t.Fatal(err)
	}
	old := agent.Message{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "old"}}}
	kept := agent.Message{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "kept"}}}
	if err := store.Append(old, agent.Usage{TotalTokens: 10}); err != nil {
		t.Fatal(err)
	}
	result := agent.CompactionResult{Summary: "## Goal\ncontinue", RetainedTail: []agent.Message{kept}, Usage: agent.Usage{TotalTokens: 3}, Timestamp: time.Now().UnixMilli()}
	if err := store.AppendCompaction(result); err != nil {
		t.Fatal(err)
	}
	future := agent.Message{Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Type: "text", Text: "future"}}}
	if err := store.Append(future, agent.Usage{TotalTokens: 4}); err != nil {
		t.Fatal(err)
	}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restored, _, messages, usage, err := Resume(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if len(messages) != 3 || messages[0].Role != agent.RoleCompactionSummary || messages[1].Content[0].Text != "kept" || messages[2].Content[0].Text != "future" {
		t.Fatalf("messages = %#v", messages)
	}
	if usage.TotalTokens != 17 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestConcurrentSessionCreationUsesIndependentLatestTemps(t *testing.T) {
	root := t.TempDir()
	const count = 12
	stores := make(chan *Store, count)
	errors := make(chan error, count)
	var group sync.WaitGroup
	for i := 0; i < count; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			store, err := NewAt(root, root, "model", "low")
			if err != nil {
				errors <- err
				return
			}
			stores <- store
		}()
	}
	group.Wait()
	close(stores)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for store := range stores {
		ids[store.ID()] = true
		_ = store.Close()
	}
	if len(ids) != count {
		t.Fatalf("created %d sessions", len(ids))
	}
	latest, err := Latest(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(latest); err != nil {
		t.Fatalf("latest points to %q: %v", latest, err)
	}
}

func TestListSortsUUIDv7DescendingAndReadsMetadata(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".pi-go", "sessions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	older := "01900000-0000-7000-8000-000000000001"
	newer := "01900000-0001-7000-8000-000000000001"
	write := func(id, body string) {
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(newer, `{"type":"session","version":1,"createdAt":"2024-01-01T00:00:00Z"}
{"type":"session_name","name":"Named work"}
{"type":"message","message":{"role":"user","timestamp":1720000000000,"content":[{"type":"text","text":"last prompt"}]}}
`)
	write(older, `{"type":"session","version":1,"createdAt":"2023-01-01T00:00:00Z"}
`)
	entries, err := List(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].ID != newer {
		t.Fatalf("first ID = %q", entries[0].ID)
	}
	if entries[0].Name == nil || *entries[0].Name != "Named work" {
		t.Fatalf("name = %#v", entries[0].Name)
	}
	if entries[0].Preview != "last prompt" {
		t.Fatalf("preview = %q", entries[0].Preview)
	}
	if got := entries[0].LastMessageTime; !got.Equal(time.UnixMilli(1720000000000)) {
		t.Fatalf("time = %v", got)
	}
}
