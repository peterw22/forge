package main

import (
	"context"
	"encoding/base64"
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
