package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
)

// Opt-in: uses the signed-in Claude account and generated, non-private images.
func TestLiveClaudeImagePrompts(t *testing.T) {
	if os.Getenv("PI_GO_LIVE_CLAUDE_IMAGES") != "1" {
		t.Skip("set PI_GO_LIVE_CLAUDE_IMAGES=1")
	}
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	child := filepath.Join(t.TempDir(), "pi-go-agent")
	if out, err := exec.Command("go", "build", "-o", child, ".").CombinedOutput(); err != nil {
		t.Fatalf("build MCP child: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bridge, err := newMCPGuardedTurnBridgeWithYOLO(ctx, workspace, agyDenyGuard{}, nil, builtInTools(workspace), false, func() bool { return true }, false, func(agent.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"pi-go-agent": map[string]any{"command": child, "args": []string{"--agy-mcp-stdio"}, "env": map[string]string{"PI_GO_AGY_MCP_ADDRESS": bridge.address + "/call", "PI_GO_AGY_MCP_TOKEN": bridge.token}}}})
	if err != nil {
		t.Fatal(err)
	}
	makeImage := func(c color.RGBA) agent.ContentBlock {
		img := image.NewRGBA(image.Rect(0, 0, 128, 128))
		for y := 0; y < 128; y++ {
			for x := 0; x < 128; x++ {
				img.SetRGBA(x, y, c)
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return agent.ContentBlock{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(buf.Bytes())}
	}
	id := ""
	t.Cleanup(func() {
		if !agyConversationIDPattern.MatchString(id) {
			return
		}
		files, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".claude", "projects", "*", id+".jsonl"))
		for _, file := range files { // Only this generated session, never the whole project directory.
			if err := os.Remove(file); err != nil {
				t.Errorf("cleanup session: %v", err)
			}
		}
	})
	run := func(blocks []agent.ContentBlock) string {
		input, err := claudeCLIUserInput(blocks)
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"--print", "--model", "claude-sonnet-5", "--effort", "low", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--tools", "", "--restricted", "--strict-mcp-config", "--mcp-config", string(config), "--setting-sources", "", "--permission-mode", "dontAsk", "--allowedTools", "mcp__pi-go-agent__*", "--append-system-prompt", mcpOnlyToolInstruction, "--disable-slash-commands"}
		if id != "" {
			args = append(args, "--resume", id)
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = workspace
		cmd.Stdin = strings.NewReader(input)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("Claude: %v: %s", err, stderr.String())
		}
		events := make(chan agent.ProviderEvent, 1024)
		previous := id
		if err := parseClaudeCLIStream(ctx, bytes.NewReader(output), events, func(saved string) { id = saved }); err != nil {
			t.Fatalf("parse: %v\n%s", err, output)
		}
		close(events)
		if previous != "" && id != previous {
			t.Fatalf("resume changed session ID")
		}
		var answer strings.Builder
		for e := range events {
			if e.Type == agent.ProviderTextDelta {
				answer.WriteString(e.Delta)
			}
		}
		t.Logf("Claude: %s", answer.String())
		return strings.ToLower(answer.String())
	}
	answer := run([]agent.ContentBlock{{Type: "text", Text: "Name the solid color of each attached image in order. Reply with only the two color names. Do not use tools. For subsequent image-only messages, reply with only the color of the newly attached image."}, makeImage(color.RGBA{255, 0, 0, 255}), makeImage(color.RGBA{0, 0, 255, 255})})
	if !strings.Contains(answer, "red") || !strings.Contains(answer, "blue") || strings.Index(answer, "red") > strings.Index(answer, "blue") {
		t.Fatalf("wrong image colors/order: %s", answer)
	}
	answer = run([]agent.ContentBlock{makeImage(color.RGBA{0, 255, 0, 255})})
	if !strings.Contains(answer, "green") {
		t.Fatalf("image-only failed: %s", answer)
	}
	answer = run([]agent.ContentBlock{{Type: "text", Text: "What were the colors of the first two images, in order? Reply only with their color names; no tools."}})
	if !strings.Contains(answer, "red") || !strings.Contains(answer, "blue") {
		t.Fatalf("image context not retained: %s", answer)
	}
}
