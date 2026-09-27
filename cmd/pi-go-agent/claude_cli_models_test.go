package main

import (
	"slices"
	"strings"
	"testing"
)

// Abridged from a Claude Code 2.1.282 initialize response.
func TestParseClaudeModelsDedupesAliases(t *testing.T) {
	output := `{"type":"system","subtype":"other"}
{"type":"control_response","response":{"subtype":"success","request_id":"pi-go-models","response":{"models":[
{"value":"default","resolvedModel":"claude-opus-5-5","displayName":"Default (recommended)","supportedEffortLevels":["low","medium","high","xhigh","max"]},
{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus 5.5"},
{"value":"claude-fable-5-1","resolvedModel":"claude-fable-5-1","displayName":"Fable 5.1","supportedEffortLevels":["low","medium","high","xhigh","max"]},
{"value":"haiku","resolvedModel":"claude-haiku-4-5-20251001","displayName":"Haiku 4.5"},
{"value":"claude-opus-4-6","resolvedModel":"claude-opus-4-6","displayName":"Opus 4.6","supportedEffortLevels":["low","medium","high","max"]},
{"value":"bad","resolvedModel":"--dangerous","displayName":"Bad"}]}}}
`
	// stream-json is one object per line; the fixture is wrapped for reading.
	first, rest, _ := strings.Cut(output, "\n")
	models, err := parseClaudeModels([]byte(first + "\n" + strings.ReplaceAll(rest, "\n", "") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	var ids, names []string
	for _, model := range models {
		ids, names = append(ids, model.ID), append(names, model.Name)
	}
	if !slices.Equal(ids, []string{"claude-opus-5-5", "claude-fable-5-1", "claude-haiku-4-5-20251001", "claude-opus-4-6"}) {
		t.Fatalf("ids = %q", ids)
	}
	if !slices.Equal(names, []string{"Opus 5.5", "Fable 5.1", "Haiku 4.5", "Opus 4.6"}) {
		t.Fatalf("names = %q", names)
	}
	if slices.Contains(models[3].EffortLevels, "xhigh") {
		t.Fatalf("Opus 4.6 effort = %q", models[3].EffortLevels)
	}
}

func TestParseClaudeModelsRequiresResponse(t *testing.T) {
	if _, err := parseClaudeModels([]byte(`{"type":"result","subtype":"success"}`)); err == nil {
		t.Fatal("expected error without a control response")
	}
}
