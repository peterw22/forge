package tui

import (
	"strings"
	"testing"
)

func TestToolSafetyStatusRendersInsideCollapsedBubble(t *testing.T) {
	frame := Screen{Width: 80, Height: 24, Blocks: []Block{{Kind: Tool, Title: "bash", Collapsed: true, SafetyStatus: "approved", SafetyMessage: "Luna approved this command"}}}.Render()
	if !strings.Contains(frame, "Bash Safety · Luna approved this command") || !strings.Contains(frame, green) {
		t.Fatalf("frame = %q", frame)
	}
}

func TestScreenKeepsEditorAfterLongTranscript(t *testing.T) {
	blocks := make([]Block, 40)
	for i := range blocks {
		blocks[i] = Block{Kind: Assistant, Text: strings.Repeat("output ", 20)}
	}
	editor := strings.Repeat("input ", 30)
	frame := Screen{Width: 48, Height: 18, Model: "gpt", Thinking: "high", Status: "idle", Editor: editor, Blocks: blocks, Suggestions: []Suggestion{{Value: "/model gpt-5.6-sol", Description: "switch model"}}}.Render()
	if !strings.Contains(frame, "gpt-5.6-sol") || !strings.Contains(frame, "input ") {
		t.Fatalf("editor or suggestion displaced:\n%s", frame)
	}
	if !strings.Contains(frame, "▏") {
		t.Fatal("cursor missing")
	}
}
func TestScreenSeparatesExpandedToolInputAndOutput(t *testing.T) {
	frame := Screen{Width: 70, Height: 22, Model: "gpt", Thinking: "high", Blocks: []Block{{Kind: Tool, Title: "bash · List files", Input: "ls -la", InputLanguage: "bash", Text: "file.txt"}}}.Render()
	for _, expected := range []string{"Input", "```bash", "ls -la", "Output", "```text", "file.txt"} {
		if !strings.Contains(frame, expected) {
			t.Fatalf("missing %q:\n%s", expected, frame)
		}
	}
}

func TestScreenRendersCollapsedReasoningAndEscapesOutput(t *testing.T) {
	frame := Screen{Width: 48, Height: 20, Model: "gpt", Thinking: "high", Status: "idle", Editor: "hello", Blocks: []Block{{Kind: User, Text: "prompt"}, {Kind: Thinking, Text: "secret summary", Collapsed: true}, {Kind: Assistant, Text: "answer\x1b[2J"}}}.Render()
	for _, e := range []string{"Pi Go", "You", "Reasoning summary", "[collapsed]", "answer[2J", "hello▏"} {
		if !strings.Contains(frame, e) {
			t.Fatalf("missing %q", e)
		}
	}
}
