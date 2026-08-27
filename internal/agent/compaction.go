package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	compactionKeepRecentTokens = 20000
	toolResultSummaryLimit     = 2000
)

const summarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const updateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// CompactionResult is a durable checkpoint. RetainedTail remains verbatim while
// Summary replaces the older active context; the JSONL history itself is never deleted.
type CompactionResult struct {
	Summary              string    `json:"summary"`
	RetainedTail         []Message `json:"retainedTail"`
	TokensBefore         int       `json:"tokensBefore"`
	EstimatedTokensAfter int       `json:"estimatedTokensAfter"`
	Usage                Usage     `json:"usage"`
	Timestamp            int64     `json:"timestamp"`
}

// Compact summarizes older context using Pi's checkpoint prompt and keeps about
// 20k recent tokens. persist runs before the in-memory checkpoint is committed.
func (a *Agent) Compact(ctx context.Context, customInstructions string, persist func(CompactionResult) error) (CompactionResult, error) {
	a.mu.Lock()
	if a.state.Streaming {
		a.mu.Unlock()
		return CompactionResult{}, errors.New("agent is already running")
	}
	a.state.Streaming = true
	messages := append([]Message(nil), a.state.Messages...)
	model, thinking, provider := a.config.Model, a.config.Thinking, a.config.Provider
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.state.Streaming = false; a.mu.Unlock() }()

	firstKept, tokensBefore := compactionCutPoint(messages, compactionKeepRecentTokens)
	if firstKept <= 0 {
		return CompactionResult{}, errors.New("not enough context to compact")
	}
	toSummarize := messages[:firstKept]
	retained := append([]Message(nil), messages[firstKept:]...)
	previousSummary := ""
	if len(toSummarize) > 0 && toSummarize[0].Role == RoleCompactionSummary {
		previousSummary = contentText(toSummarize[0].Content)
		toSummarize = toSummarize[1:]
	}
	conversation := serializeForCompaction(toSummarize)
	if strings.TrimSpace(conversation) == "" && previousSummary == "" {
		return CompactionResult{}, errors.New("not enough context to compact")
	}
	basePrompt := summarizationPrompt
	if previousSummary != "" {
		basePrompt = updateSummarizationPrompt
	}
	if custom := strings.TrimSpace(customInstructions); custom != "" {
		basePrompt += "\n\nAdditional focus: " + custom
	}
	prompt := "<conversation>\n" + conversation + "\n</conversation>\n\n"
	if previousSummary != "" {
		prompt += "<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\n"
	}
	prompt += basePrompt

	request := Request{Model: model, Thinking: thinking, SystemPrompt: summarizationSystemPrompt, Messages: []Message{{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: prompt}}, Timestamp: time.Now().UnixMilli()}}}
	events, errs := provider.Stream(ctx, request)
	var summary strings.Builder
	var usage Usage
	var done bool
	for events != nil || errs != nil {
		select {
		case <-ctx.Done():
			return CompactionResult{}, ctx.Err()
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch event.Type {
			case ProviderTextDelta:
				summary.WriteString(event.Delta)
			case ProviderDone:
				usage, done = event.Usage, true
			case ProviderError:
				if event.Err != nil {
					return CompactionResult{}, event.Err
				}
				return CompactionResult{}, errors.New("summarization failed")
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return CompactionResult{}, err
			}
		}
	}
	if !done || strings.TrimSpace(summary.String()) == "" {
		return CompactionResult{}, errors.New("summarization ended without a summary")
	}
	text := strings.TrimSpace(summary.String()) + trackedFiles(toSummarize)
	checkpoint := Message{Role: RoleCompactionSummary, Content: []ContentBlock{{Type: "text", Text: text}}, Timestamp: time.Now().UnixMilli()}
	active := append([]Message{checkpoint}, retained...)
	result := CompactionResult{Summary: text, RetainedTail: retained, TokensBefore: tokensBefore, EstimatedTokensAfter: estimateMessages(active), Usage: usage, Timestamp: time.Now().UnixMilli()}
	if persist != nil {
		if err := persist(result); err != nil {
			return CompactionResult{}, fmt.Errorf("persist compaction: %w", err)
		}
	}
	a.mu.Lock()
	a.state.Messages = active
	a.state.ContextTokens = result.EstimatedTokensAfter
	a.state.Usage.Input += usage.Input
	a.state.Usage.Output += usage.Output
	a.state.Usage.CacheRead += usage.CacheRead
	a.state.Usage.CacheWrite += usage.CacheWrite
	a.state.Usage.TotalTokens += usage.TotalTokens
	a.mu.Unlock()
	return result, nil
}

func compactionCutPoint(messages []Message, keepTokens int) (int, int) {
	total := estimateMessages(messages)
	if total <= keepTokens || len(messages) < 2 {
		return 0, total
	}
	accumulated, candidate := 0, len(messages)-1
	for i := len(messages) - 1; i >= 0; i-- {
		accumulated += estimateMessage(messages[i])
		candidate = i
		if accumulated >= keepTokens {
			break
		}
	}
	// Prefer a complete user turn. If one huge turn exceeds the budget, split at
	// an assistant boundary and never leave a tool result without its call.
	for i := candidate; i >= 1; i-- {
		if messages[i].Role == RoleUser {
			return i, total
		}
	}
	for candidate > 0 && messages[candidate].Role == RoleToolResult {
		candidate--
	}
	if candidate <= 0 {
		return 0, total
	}
	return candidate, total
}

func estimateMessages(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += estimateMessage(message)
	}
	return total
}
func estimateMessage(message Message) int {
	chars := 0
	for _, block := range message.Content {
		switch block.Type {
		case "text", "thinking":
			chars += len(block.Text)
		case "image":
			chars += 4800
		case "toolCall":
			encoded, _ := json.Marshal(block.Arguments)
			chars += len(block.Name) + len(encoded)
		}
	}
	return (chars + 3) / 4
}

func serializeForCompaction(messages []Message) string {
	var parts []string
	for _, message := range messages {
		switch message.Role {
		case RoleUser:
			text := contentText(message.Content)
			for _, block := range message.Content {
				if block.Type == "image" {
					text += "\n[image attachment: " + block.MIMEType + "]"
				}
			}
			if strings.TrimSpace(text) != "" {
				parts = append(parts, "[User]: "+strings.TrimSpace(text))
			}
		case RoleAssistant:
			var text, reasoning []string
			var calls []string
			for _, block := range message.Content {
				switch block.Type {
				case "text":
					text = append(text, block.Text)
				case "thinking":
					reasoning = append(reasoning, block.Text)
				case "toolCall":
					encoded, _ := json.Marshal(block.Arguments)
					calls = append(calls, block.Name+"("+string(encoded)+")")
				}
			}
			if len(reasoning) > 0 {
				parts = append(parts, "[Assistant reasoning summary]: "+strings.Join(reasoning, "\n"))
			}
			if len(text) > 0 {
				parts = append(parts, "[Assistant]: "+strings.Join(text, "\n"))
			}
			if len(calls) > 0 {
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(calls, "; "))
			}
		case RoleToolResult:
			text := contentText(message.Content)
			if len(text) > toolResultSummaryLimit {
				text = text[:toolResultSummaryLimit] + fmt.Sprintf("\n\n[... %d more characters truncated]", len(text)-toolResultSummaryLimit)
			}
			if text != "" {
				parts = append(parts, "[Tool result]: "+text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func contentText(content []ContentBlock) string {
	var values []string
	for _, block := range content {
		if block.Type == "text" && block.Text != "" {
			values = append(values, block.Text)
		}
	}
	return strings.Join(values, "\n")
}

func trackedFiles(messages []Message) string {
	read, modified := map[string]bool{}, map[string]bool{}
	for _, message := range messages {
		if message.Role == RoleAssistant {
			for _, block := range message.Content {
				if block.Type != "toolCall" {
					continue
				}
				path, _ := block.Arguments["path"].(string)
				if path == "" {
					continue
				}
				switch block.Name {
				case "read":
					read[path] = true
				case "write", "edit", "replace":
					modified[path] = true
				}
			}
		}
	}
	for path := range modified {
		delete(read, path)
	}
	keys := func(values map[string]bool) []string {
		result := make([]string, 0, len(values))
		for value := range values {
			result = append(result, value)
		}
		sort.Strings(result)
		return result
	}
	var sections []string
	if values := keys(read); len(values) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(values, "\n")+"\n</read-files>")
	}
	if values := keys(modified); len(values) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(values, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}
