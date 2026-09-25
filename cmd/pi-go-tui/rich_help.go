package main

import (
	"github.com/peterw22/pi-go/internal/tui"
	"strings"
)

func richHelp(input string) []tui.Suggestion {
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	if strings.HasPrefix(input, "/model") {
		prefix := strings.TrimSpace(strings.TrimPrefix(input, "/model"))
		var out []tui.Suggestion
		for _, model := range models {
			if strings.HasPrefix(model, prefix) {
				out = append(out, tui.Suggestion{Value: "/model " + model, Description: "switch model and start a new session"})
			}
		}
		return out
	}
	if strings.HasPrefix(input, "/thinking") {
		prefix := strings.TrimSpace(strings.TrimPrefix(input, "/thinking"))
		var out []tui.Suggestion
		for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
			if strings.HasPrefix(level, prefix) {
				out = append(out, tui.Suggestion{Value: "/thinking " + level, Description: "reasoning effort for future turns"})
			}
		}
		return out
	}
	items := []tui.Suggestion{{Value: "/compact", Description: "summarize older context; optional focus"}, {Value: "/cwd", Description: "change agent working directory"}, {Value: "/model", Description: "select Codex model"}, {Value: "/sessions", Description: "switch session (Ctrl-B s)"}, {Value: "/new", Description: "create a blank session"}, {Value: "/name", Description: "name this session"}, {Value: "/thinking", Description: "select reasoning effort"}, {Value: "/abort", Description: "cancel current turn"}, {Value: "/clear", Description: "clear transcript"}, {Value: "/exit", Description: "quit Pi Go"}}
	var out []tui.Suggestion
	for _, item := range items {
		if strings.HasPrefix(item.Value, input) {
			out = append(out, item)
		}
	}
	return out
}
