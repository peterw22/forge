package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelAcceptsBackendBlocks(t *testing.T) {
	model := New("gpt-5.6-terra", "high")
	next, _ := model.Update(Append{Block: Block{Kind: Assistant, Text: "hello"}})
	state := next.(Model)
	if len(state.Screen.Blocks) != 1 {
		t.Fatal("append was not retained")
	}
	next, _ = state.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if next.(Model).Screen.Width != 100 {
		t.Fatal("resize was not applied")
	}
}

func TestClickTogglesCollapsedTool(t *testing.T) {
	model := New("gpt", "high")
	model.Screen.Width, model.Screen.Height = 80, 24
	model.Screen.Blocks = []Block{{Kind: Tool, Title: "bash", Text: "output", Collapsed: true}}
	next, _ := model.Update(tea.MouseMsg{X: 4, Y: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if next.(Model).Screen.Blocks[0].Collapsed {
		t.Fatal("tool stayed collapsed after click")
	}
}

func TestSafetyApprovalRequiresExplicitY(t *testing.T) {
	model := New("gpt", "low")
	approved := false
	model.OnApproval = func(id string, allow bool) { approved = id == "approval-1" && allow }
	next, _ := model.Update(ShowApproval{ID: "approval-1", Tool: "bash", Description: "rm file", Reason: "deletes a file"})
	model = next.(Model)
	if model.approval == nil || !strings.Contains(model.View(), "Bash Safety") {
		t.Fatal("approval dialog was not shown")
	}
	next, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	model = next.(Model)
	if model.approval != nil || !approved {
		t.Fatal("approval was not resolved")
	}
}

func TestTypingModelSpaceWithSuggestionsDoesNotPanic(t *testing.T) {
	model := New("gpt", "high")
	model.Help = func(input string) []Suggestion {
		if input == "/model " {
			return []Suggestion{{Value: "/model a"}, {Value: "/model b"}, {Value: "/model c"}, {Value: "/model d"}, {Value: "/model e"}}
		}
		return nil
	}
	for _, key := range []string{"/", "m", "o", "d", "e", "l"} {
		next, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		model = next.(Model)
	}
	next, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = next.(Model)
	_ = model.View()
	if len(model.Screen.Suggestions) != 5 {
		t.Fatalf("suggestions = %d", len(model.Screen.Suggestions))
	}
}
