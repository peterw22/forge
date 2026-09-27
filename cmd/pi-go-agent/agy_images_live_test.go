package main

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peterw22/forge/internal/agent"
)

// TestMain lets live CLI tests use the test binary as the MCP child, since the
// providers launch os.Executable() with --agy-mcp-stdio.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--agy-mcp-stdio" {
		if err := runAgyMCPChild(); err != nil {
			os.Stderr.WriteString(err.Error() + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Opt-in: uses the signed-in agy home and a generated, non-private image.
// agy rejects image prompt blocks, so images can only reach Gemini as MCP tool
// results; this checks that path end to end.
func TestLiveAgyImageViaMCPRead(t *testing.T) {
	if os.Getenv("PI_GO_LIVE_AGY_IMAGES") != "1" {
		t.Skip("set PI_GO_LIVE_AGY_IMAGES=1")
	}
	binary, err := agyBinary()
	if err != nil {
		t.Skip(err)
	}
	workspace := t.TempDir()
	models, err := readyAgyModels(context.Background(), workspace)
	if err != nil {
		t.Skip(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 128, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	file, err := os.Create(filepath.Join(workspace, "swatch.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	file.Close()
	provider := newAgyProvider(binary)
	provider.policyReady = true
	var readImage bool
	req := agent.Request{Model: models[0].ID, SessionID: "live-agy-image", WorkingDirectory: workspace, Tools: builtInTools(workspace), ToolGuard: agyDenyGuard{}, YOLO: true,
		Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "Use the read tool on swatch.png, then reply with only the name of its solid color."}}}},
		OnToolEvent: func(event agent.Event) {
			if event.Type == agent.EventToolExecutionEnd && event.ToolName == "read" && event.Result != nil {
				for _, block := range event.Result.Content {
					readImage = readImage || block.Type == "image"
				}
			}
		}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	events, errs := provider.Stream(ctx, req)
	var answer strings.Builder
	for event := range events {
		if event.Type == agent.ProviderTextDelta {
			answer.WriteString(event.Delta)
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	t.Logf("model=%s readImage=%v answer=%q", models[0].ID, readImage, answer.String())
	if !readImage {
		t.Fatal("agy did not read the image through the MCP read tool")
	}
	if !strings.Contains(strings.ToLower(answer.String()), "red") {
		t.Fatalf("image content did not reach the model: %q", answer.String())
	}
}
