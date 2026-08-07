package main

import (
	"github.com/peterw22/pi-go/internal/tui"
	"strings"
)

func (a *app) richCommand(command string) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return
	}
	switch parts[0] {
	case "/compact":
		instructions := strings.TrimSpace(strings.TrimPrefix(command, "/compact"))
		if err := a.send(backendCommand{ID: "compact", Type: "compact", CustomInstructions: instructions}); err != nil {
			a.program.Send(tui.SetStatus{Value: err.Error()})
		} else {
			a.program.Send(tui.SetStatus{Value: "compacting context…"})
		}
	case "/new":
		if err := a.send(backendCommand{ID: "new-session", Type: "new_session"}); err != nil {
			a.program.Send(tui.SetStatus{Value: err.Error()})
		}
	case "/sessions":
		if err := a.send(backendCommand{ID: "sessions", Type: "list_sessions"}); err != nil {
			a.program.Send(tui.SetStatus{Value: err.Error()})
		}
	case "/cwd":
		workspace := strings.TrimSpace(strings.TrimPrefix(command, "/cwd"))
		if workspace == "" {
			a.program.Send(tui.SetStatus{Value: "usage: /cwd <directory>"})
			return
		}
		if err := a.send(backendCommand{ID: "cwd", Type: "set_cwd", CWD: workspace}); err != nil {
			a.program.Send(tui.SetStatus{Value: err.Error()})
		} else {
			a.program.Send(tui.SetStatus{Value: "changing workspace to " + workspace})
		}
	case "/clear":
		a.program.Send(tui.Append{Block: tui.Block{}}) // transcript reset follows on next model/session switch
	case "/name":
		var name *string
		if len(parts) > 1 && strings.Join(parts[1:], " ") != "null" {
			value := strings.Join(parts[1:], " ")
			name = &value
		}
		if err := a.send(backendCommand{ID: "name", Type: "set_session_name", Name: name}); err != nil {
			a.program.Send(tui.SetStatus{Value: err.Error()})
		} else if name == nil {
			a.program.Send(tui.SetStatus{Value: "session name cleared"})
		} else {
			a.program.Send(tui.SetStatus{Value: "session name: " + *name})
		}
	case "/thinking":
		if len(parts) != 2 || !containsChoice([]string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}, parts[1]) {
			a.program.Send(tui.SetStatus{Value: "usage: /thinking <level>"})
			return
		}
		a.thinking = parts[1]
		_ = a.send(backendCommand{ID: "thinking", Type: "set_thinking_level", Level: a.thinking})
		a.program.Send(tui.SetThinking{Value: a.thinking})
		a.program.Send(tui.SetStatus{Value: "thinking: " + a.thinking})
	case "/model":
		if len(parts) != 2 {
			a.program.Send(tui.SetStatus{Value: "usage: /model <model-id>"})
			return
		}
		a.model = parts[1]
		if err := a.send(backendCommand{ID: "model", Type: "set_model", Model: a.model}); err != nil {
			a.program.Send(tui.SetStatus{Value: err.Error()})
		} else {
			a.program.Send(tui.SetModel{Value: a.model})
			a.program.Send(tui.SetStatus{Value: "model: " + a.model})
		}
	case "/exit", "/quit":
		a.program.Quit()
	default:
		a.program.Send(tui.SetStatus{Value: "unknown command"})
	}
}
func containsChoice(choices []string, value string) bool {
	for _, choice := range choices {
		if choice == value {
			return true
		}
	}
	return false
}
func richCompletions(value string) []string {
	commands := []string{"/abort", "/clear", "/compact", "/cwd", "/exit", "/model", "/name", "/new", "/quit", "/sessions", "/thinking"}
	models := []string{"gpt-5.3-codex-spark", "gpt-5.5", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"}
	levels := []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	prefix, choices := "", commands
	if value == "/model" || value == "/model " {
		prefix = "/model "
		choices = models
		value = ""
	} else if value == "/thinking" || value == "/thinking " {
		prefix = "/thinking "
		choices = levels
		value = ""
	} else if strings.HasPrefix(value, "/model ") {
		prefix = "/model "
		choices = models
		value = strings.TrimPrefix(value, prefix)
	} else if strings.HasPrefix(value, "/thinking ") {
		prefix = "/thinking "
		choices = levels
		value = strings.TrimPrefix(value, prefix)
	}
	var out []string
	for _, choice := range choices {
		if strings.HasPrefix(choice, value) {
			out = append(out, prefix+choice)
		}
	}
	return out
}
