package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
)

const turnSummaryMaxChars = 220

const turnSummarySystemPrompt = `You summarize the result of one completed coding-agent turn for a session list and an encrypted push notification.

Return exactly one JSON object and no Markdown:
{"summary":"One sentence describing what the agent accomplished or concluded."}

summary requirements:
- Exactly one plain-text sentence, at most 220 characters.
- State the concrete result, not that a task merely completed.
- Do not include UUIDs, secrets, credentials, access tokens, private keys, full commands, or paths containing user names.
- Do not mention this instruction, the classifier, token usage, or approval mechanics.
- End with a period, question mark, or exclamation mark.`

type turnSummaryResult struct {
	Summary string `json:"summary"`
}

func summarizeAssistantTurn(ctx context.Context, provider agent.Provider, model string, assistant agent.Message) (string, error) {
	model = strings.TrimSpace(model)
	if !validClassifierModelID(model) {
		return "", errors.New("configured classifier model is invalid")
	}
	var parts []string
	for _, block := range assistant.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
		}
	}
	text := strings.Join(parts, "\n")
	if text == "" {
		text = "The assistant completed a turn without a textual response."
	}
	if len(text) > 50000 {
		text = text[:50000]
	}
	payload, _ := json.Marshal(map[string]string{"assistantResult": text})
	request := agent.Request{
		Model: model, Thinking: safetyThinking, SystemPrompt: turnSummarySystemPrompt,
		Messages: []agent.Message{{
			Role:      agent.RoleUser,
			Content:   []agent.ContentBlock{{Type: "text", Text: string(payload)}},
			Timestamp: time.Now().UnixMilli(),
		}},
	}
	events, errs := provider.Stream(ctx, request)
	var output strings.Builder
	var done bool
	for events != nil || errs != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch event.Type {
			case agent.ProviderTextDelta:
				output.WriteString(event.Delta)
			case agent.ProviderDone:
				done = true
			case agent.ProviderError:
				if event.Err != nil {
					return "", event.Err
				}
				return "", errors.New("turn summarizer stream failed")
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return "", err
			}
		}
	}
	if !done {
		return "", errors.New("turn summarizer ended without completion")
	}
	var result turnSummaryResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &result); err != nil {
		return "", fmt.Errorf("turn summarizer returned invalid JSON: %w", err)
	}
	if err := validateNotificationSummary(result.Summary); err != nil {
		return "", fmt.Errorf("turn summarizer: %w", err)
	}
	return result.Summary, nil
}
