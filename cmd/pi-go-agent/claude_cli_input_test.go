package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

const claudeTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="

func TestClaudeCLIUserInput(t *testing.T) {
	image := agent.ContentBlock{Type: "image", MIMEType: "image/png", Data: claudeTestPNG}
	for _, blocks := range [][]agent.ContentBlock{
		{image},
		{{Type: "text", Text: "What is this?\n"}, image, {Type: "text", Text: "Compare"}, image},
		{{Type: "text", Text: "plain text"}},
	} {
		input, err := claudeCLIUserInput(blocks)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type   string `json:"type"`
					Text   string `json:"text"`
					Source struct {
						Type string `json:"type"`
						MIME string `json:"media_type"`
						Data string `json:"data"`
					} `json:"source"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(input), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Type != "user" || envelope.Message.Role != "user" || len(envelope.Message.Content) != len(blocks) || !strings.HasSuffix(input, "\n") {
			t.Fatalf("bad envelope: %s", input)
		}
		for i, block := range blocks {
			got := envelope.Message.Content[i]
			if got.Type != block.Type || got.Text != block.Text {
				t.Fatalf("bad block: %+v", got)
			}
			if block.Type == "image" && (got.Source.Type != "base64" || got.Source.MIME != block.MIMEType || got.Source.Data != block.Data) {
				t.Fatalf("bad image: %+v", got)
			}
		}
	}
	for _, mime := range []string{"image/jpeg", "image/gif", "image/webp"} {
		image.MIMEType = mime
		if _, err := claudeCLIUserInput([]agent.ContentBlock{image}); err != nil {
			t.Fatal(err)
		}
	}
	for _, blocks := range [][]agent.ContentBlock{
		nil, {{Type: "text"}}, {{Type: "audio"}},
		{{Type: "image", MIMEType: "image/svg+xml", Data: claudeTestPNG}},
		{{Type: "image", MIMEType: "image/png"}},
		{{Type: "image", MIMEType: "image/png", Data: "not base64!"}},
	} {
		if _, err := claudeCLIUserInput(blocks); err == nil {
			t.Fatalf("accepted invalid blocks: %+v", blocks)
		}
	}
}

func TestClaudeCLIImagePromptOnStdin(t *testing.T) {
	stubClaudeOnPath(t)
	dir := t.TempDir()
	binary := filepath.Join(dir, "claude")
	argsPath, inputPath := filepath.Join(dir, "args"), filepath.Join(dir, "input")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsPath + "'\ncat > '" + inputPath + "'\nprintf '%s\\n' '" + claudeTestInit + "' '{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"session-1\",\"result\":\"ok\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := newClaudeCLIProvider(binary)
	blocks := []agent.ContentBlock{{Type: "text", Text: "describe attachment"}, {Type: "image", MIMEType: "image/png", Data: claudeTestPNG}}
	req := agent.Request{Model: "claude/claude-sonnet-5", Thinking: "high", SessionID: "pi-session", WorkingDirectory: dir, ToolGuard: agyDenyGuard{}, OnToolEvent: func(agent.Event) {}, Messages: []agent.Message{{Role: agent.RoleUser, Content: blocks}}}
	for turn := 0; turn < 2; turn++ {
		events, errs := p.Stream(context.Background(), req)
		for range events {
		}
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		args, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(args), "--input-format\nstream-json\n") || strings.Contains(string(args), claudeTestPNG) || strings.Contains(string(args), "describe attachment") {
			t.Fatalf("bad argv: %s", args)
		}
		if turn == 1 && !strings.Contains(string(args), "--resume\nsession-1\n") {
			t.Fatalf("missing resume: %s", args)
		}
		input, err := os.ReadFile(inputPath)
		if err != nil {
			t.Fatal(err)
		}
		want, err := claudeCLIUserInput(blocks)
		if err != nil {
			t.Fatal(err)
		}
		if string(input) != want {
			t.Fatalf("stdin=%q want=%q", input, want)
		}
	}
}
