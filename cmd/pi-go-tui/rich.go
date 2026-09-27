package main

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/peterw22/forge/internal/tui"
)

func (a *app) runRich() {
	model := tui.New(a.model, a.thinking)
	model.OnSubmit = func(text string) {
		go func() {
			a.turn++
			if err := a.send(backendCommand{ID: fmt.Sprintf("turn-%d", a.turn), Type: "prompt", Message: text}); err != nil {
				a.program.Send(tui.SetStatus{Value: err.Error()})
			}
		}()
	}
	model.OnAbort = func() { _ = a.send(backendCommand{ID: "abort", Type: "abort"}) }
	model.OnApproval = func(id string, approved bool) {
		go func() {
			_ = a.send(backendCommand{ID: "approval", Type: "approval_response", ApprovalID: id, Approved: approved})
		}()
	}
	model.OnCommand = func(command string) { go a.richCommand(command) }
	model.Complete = richCompletions
	model.Help = richHelp
	model.OnOpenSessions = func() { go func() { _ = a.send(backendCommand{ID: "sessions", Type: "list_sessions"}) }() }
	model.OnSelectSession = func(id string) {
		go func() {
			if id == "__new_session__" {
				_ = a.send(backendCommand{ID: "new-session", Type: "new_session"})
			} else {
				_ = a.send(backendCommand{ID: "switch-session", Type: "switch_session", Session: id})
			}
		}()
	}
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	a.program = program
	if err := a.start(); err != nil {
		fmt.Println(err)
		return
	}
	_ = a.send(backendCommand{ID: "initial-state", Type: "get_state"})
	_, _ = program.Run()
	a.stop()
}

func (a *app) restoreRichState(response backendResponse) {
	var blocks []tui.Block
	toolTitles := make(map[string]string)
	toolInputs := make(map[string]string)
	toolLanguages := make(map[string]string)
	for _, message := range response.State.Messages {
		if message.Role != "assistant" {
			continue
		}
		for _, content := range message.Content {
			if content.Type == "toolCall" {
				toolTitles[content.ID] = toolSummary(content.Name, content.Arguments)
				toolInputs[content.ID], toolLanguages[content.ID] = toolInput(content.Name, content.Arguments)
			}
		}
	}
	for _, message := range response.State.Messages {
		switch message.Role {
		case "user":
			blocks = append(blocks, tui.Block{Kind: tui.User, Text: messageText(&message)})
		case "assistant":
			var text, thinking []string
			for _, content := range message.Content {
				if content.Type == "text" {
					text = append(text, content.Text)
				}
				if content.Type == "thinking" {
					thinking = append(thinking, content.Text)
				}
			}
			if len(thinking) > 0 {
				blocks = append(blocks, tui.Block{Kind: tui.Thinking, Text: strings.Join(thinking, "\n"), Collapsed: true})
			}
			if len(text) > 0 {
				blocks = append(blocks, tui.Block{Kind: tui.Assistant, Text: strings.Join(text, "\n")})
			}
		case "compactionSummary":
			blocks = append(blocks, tui.Block{Kind: tui.Compaction, Text: messageText(&message), Collapsed: true})
		case "toolResult":
			title := toolTitles[message.ToolCallID]
			if title == "" {
				title = message.ToolName
			}
			blocks = append(blocks, tui.Block{Kind: tui.Tool, Title: title, Text: messageText(&message), Input: toolInputs[message.ToolCallID], InputLanguage: toolLanguages[message.ToolCallID], IsError: message.IsError, Collapsed: true})
		}
	}
	a.model, a.thinking = response.Model, response.Thinking
	a.program.Send(tui.Restore{Blocks: blocks, Model: response.Model, Thinking: response.Thinking, Input: response.State.Usage.Input, CacheRead: response.State.Usage.CacheRead, Output: response.State.Usage.Output, Total: response.State.Usage.Total, Context: response.State.ContextTokens, Streaming: response.State.Streaming})
}

func (a *app) renderRich(event agentEvent) {
	if a.program == nil {
		return
	}
	switch event.Type {
	case "tool_safety_update":
		a.program.Send(tui.ReplaceSafety{ID: event.ToolCallID, Status: event.SafetyStatus, Message: event.SafetyMessage})
		a.program.Send(tui.ClearApproval{ToolCallID: event.ToolCallID})
	case "approval_required":
		a.program.Send(tui.ShowApproval{ID: event.ApprovalID, ToolCallID: event.ToolCallID, Tool: event.ToolName, Description: event.Description, Reason: event.Reason})
	case "compaction_start":
		a.program.Send(tui.SetStatus{Value: "compacting context… · Ctrl-C to abort"})
	case "compaction_end":
		if event.Error != "" {
			a.program.Send(tui.SetStatus{Value: event.Error})
		} else {
			a.program.Send(tui.SetStatus{Value: "context compacted"})
		}
	case "agent_start":
		a.program.Send(tui.SetStatus{Value: "streaming"})
	case "message_start":
		if event.Message != nil && event.Message.Role == "user" {
			a.program.Send(tui.Append{Block: tui.Block{Kind: tui.User, Text: messageText(event.Message)}})
		}
		if event.Message != nil && event.Message.Role == "assistant" {
			a.program.Send(tui.Append{Block: tui.Block{Kind: tui.Assistant}})
		}
	case "message_update":
		if event.Message != nil && event.Message.Role == "assistant" {
			a.program.Send(tui.ReplaceLast{Kind: tui.Assistant, Text: messageText(event.Message)})
		}
	case "message_end":
		if event.Message != nil && event.Message.Role == "assistant" {
			a.program.Send(tui.AddUsage{Input: event.Usage.Input, CacheRead: event.Usage.CacheRead, Output: event.Usage.Output, Total: event.Usage.Total})
		}
	case "tool_execution_start":
		a.program.Send(tui.Append{Block: func() tui.Block {
			input, language := toolInput(event.ToolName, event.Arguments)
			return tui.Block{ID: event.ToolCallID, Kind: tui.Tool, Title: toolSummary(event.ToolName, event.Arguments), Input: input, InputLanguage: language, Collapsed: true}
		}()})
		a.program.Send(tui.SetStatus{Value: event.ToolName + " running · Ctrl-C to abort/kill"})
	case "tool_execution_update":
		if event.Result != nil {
			a.program.Send(tui.ReplaceBlock{ID: event.ToolCallID, Text: toolResultText(event.Result), Error: event.Result.IsError})
		}
	case "tool_execution_end":
		if event.Result != nil {
			a.program.Send(tui.ReplaceBlock{ID: event.ToolCallID, Text: toolResultText(event.Result), Error: event.IsError || event.Result.IsError})
		}
		a.program.Send(tui.SetStatus{Value: "streaming"})
	case "agent_end":
		a.program.Send(tui.SetStatus{Value: "idle"})
	}
}

func toolInput(name string, arguments map[string]any) (string, string) {
	if name == "bash" {
		if command, ok := arguments["command"].(string); ok {
			return command, "bash"
		}
	}
	encoded, err := json.MarshalIndent(arguments, "", "  ")
	if err != nil {
		return "(input unavailable)", "json"
	}
	return string(encoded), "json"
}

func toolSummary(name string, arguments map[string]any) string {
	if name == "bash" {
		if description, ok := arguments["description"].(string); ok && strings.TrimSpace(description) != "" {
			return "bash · " + strings.TrimSpace(description)
		}
	}
	if name == "read" || name == "write" || name == "edit" {
		if path, ok := arguments["path"].(string); ok && path != "" {
			return name + " · " + path
		}
	}
	return name
}

func toolResultText(result *toolResult) string {
	var parts []string
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return "(no output)"
	}
	return strings.Join(parts, "\n")
}
