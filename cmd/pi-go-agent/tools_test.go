package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
)

func TestPromptContentAcceptsImageAttachment(t *testing.T) {
	image := agent.ContentBlock{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("png"))}
	content, err := promptContent("inspect", []agent.ContentBlock{image})
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 2 || content[1].Type != "image" {
		t.Fatalf("content = %#v", content)
	}
}

func TestBashSchemaRequiresUserFacingDescription(t *testing.T) {
	schema := bashSchema()
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 2 || required[0] != "command" || required[1] != "description" {
		t.Fatalf("required = %#v", schema["required"])
	}
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["description"]; !ok {
		t.Fatal("bash description property missing")
	}
}

func TestBashToolStreamsOutput(t *testing.T) {
	var updates []string
	result, err := bashTool(t.TempDir())(context.Background(), map[string]any{"command": "printf first; sleep .2; printf second"}, func(result agent.ToolResult) {
		updates = append(updates, result.Content[0].Text)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content[0].Text, "firstsecond") {
		t.Fatalf("result = %#v", result)
	}
	if len(updates) < 2 || !strings.Contains(updates[0], "first") || !strings.Contains(updates[len(updates)-1], "second") {
		t.Fatalf("updates = %#v", updates)
	}
}

func TestBashToolCancellationKillsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan agent.ToolResult, 1)
	go func() {
		result, _ := bashTool(t.TempDir())(ctx, map[string]any{"command": "sleep 30"}, func(agent.ToolResult) {})
		finished <- result
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case result := <-finished:
		if !result.IsError || !strings.Contains(result.Content[0].Text, "aborted") {
			t.Fatalf("result = %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled bash process did not stop")
	}
}

func TestReplaceToolUsesUniqueMultilineRegexAndReturnsDiffDetails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "example.php")
	original := "<?php\nfunction oldName(): string {\n    return 'old';\n}\necho oldName();\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := replaceTool(root)(context.Background(), map[string]any{
		"path": "example.php", "oldText": "", "oldRegex": `(?s)function oldName\(\): string \{.*?\n\}`, "newText": "function newName(): string {\n    return 'new';\n}",
	}, func(agent.ToolResult) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || !strings.Contains(result.Content[0].Text, "regex match") {
		t.Fatalf("result = %#v", result)
	}
	details, ok := result.Details.(map[string]any)
	if !ok || details["mode"] != "regex" || details["startLine"] != 2 || !strings.Contains(details["oldText"].(string), "return 'old'") {
		t.Fatalf("details = %#v", result.Details)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "function newName") || strings.Contains(string(updated), "return 'old'") {
		t.Fatalf("updated = %s", updated)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, err = %v", info.Mode().Perm(), err)
	}
}

func TestReplaceToolTreatsEmptyUnusedRegexAsAbsent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "example.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := replaceTool(root)(context.Background(), map[string]any{
		"path": "example.txt", "oldText": "before", "oldRegex": "", "newText": "after",
	}, func(agent.ToolResult) {})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil || string(updated) != "after\n" {
		t.Fatalf("updated = %q, err = %v", updated, err)
	}
}

func TestReplaceToolFailsWithoutWritingOnMultipleRegexMatches(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "example.txt")
	original := "same\nkeep\nsame\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := replaceTool(root)(context.Background(), map[string]any{
		"path": "example.txt", "oldRegex": `same`, "newText": "changed",
	}, func(agent.ToolResult) {})
	if err == nil || !strings.Contains(err.Error(), "exactly once") {
		t.Fatalf("err = %v", err)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != original {
		t.Fatalf("file changed after ambiguous regex: %q", unchanged)
	}
}

func TestReplaceToolRejectsEmptyRegexMatchAndSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceTool(root)(context.Background(), map[string]any{
		"path": "inside.txt", "oldRegex": `^`, "newText": "prefix",
	}, func(agent.ToolResult) {}); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("empty-match err = %v", err)
	}
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := replaceTool(root)(context.Background(), map[string]any{
		"path": "link.txt", "oldText": "secret", "newText": "changed",
	}, func(agent.ToolResult) {}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink err = %v", err)
	}
}

func TestReplaceSchemaAdvisesRegexAndForbidsReplaceAll(t *testing.T) {
	tools := builtInTools(t.TempDir())
	for _, tool := range tools {
		if tool.Name != "replace" {
			continue
		}
		if !strings.Contains(tool.Description, "reduces output tokens") || !strings.Contains(tool.Description, "exactly one") {
			t.Fatalf("replace description = %q", tool.Description)
		}
		properties := tool.Parameters["properties"].(map[string]any)
		if properties["oldRegex"] == nil || properties["oldText"] == nil || properties["newText"] == nil || properties["replaceAll"] != nil {
			t.Fatalf("replace properties = %#v", properties)
		}
		return
	}
	t.Fatal("replace tool is not registered")
}
