package main

import (
	"context"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

func TestSummarizeAssistantTurnUsesClassifierAndValidatesSentence(t *testing.T) {
	provider := &safetyTestProvider{decision: `{"summary":"Implemented encrypted completion summaries."}`}
	message := agent.Message{Role: agent.RoleAssistant, Content: []agent.ContentBlock{
		{Type: "thinking", Text: "private reasoning"},
		{Type: "text", Text: "Implemented persistence and push delivery."},
	}}
	summary, err := summarizeAssistantTurn(context.Background(), provider, "gpt-5.6-sol", message)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "Implemented encrypted completion summaries." {
		t.Fatalf("summary = %q", summary)
	}
	if len(provider.requests) != 1 || provider.requests[0].Model != "gpt-5.6-sol" || provider.requests[0].Thinking != safetyThinking || len(provider.requests[0].Tools) != 1 || provider.requests[0].Tools[0].Name != "submit_turn_summary" {
		t.Fatalf("request = %#v", provider.requests)
	}
	requestText := provider.requests[0].Messages[0].Content[0].Text
	if strings.Contains(requestText, "private reasoning") || !strings.Contains(requestText, "Implemented persistence") {
		t.Fatalf("summarizer input = %q", requestText)
	}
}

func TestSummarizeAssistantTurnRejectsInvalidSummary(t *testing.T) {
	provider := &safetyTestProvider{decision: `{"summary":"First sentence. Second sentence."}`}
	_, err := summarizeAssistantTurn(context.Background(), provider, defaultClassifierModel, agent.Message{
		Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Type: "text", Text: "done"}},
	})
	if err == nil {
		t.Fatal("multi-sentence summary accepted")
	}
}

func TestSummarizeAssistantTurnHandlesNoText(t *testing.T) {
	provider := &safetyTestProvider{decision: `{"summary":"Completed the requested operation without a textual response."}`}
	summary, err := summarizeAssistantTurn(context.Background(), provider, defaultClassifierModel, agent.Message{
		Role:    agent.RoleAssistant,
		Content: []agent.ContentBlock{{Type: "toolCall", Name: "bash"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary != "Completed the requested operation without a textual response." {
		t.Fatalf("summary = %q", summary)
	}
}
