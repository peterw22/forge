package main

import (
	"context"
	"strings"
	"testing"

	"github.com/peterw22/forge/internal/agent"
)

func TestParseAgyStream(t *testing.T) {
	input := strings.Join([]string{
		`{"event":"init","conversation_id":"id"}`,
		`{"event":"step_update","step_update":{"step_type":"agent_thinking","thinking_delta":"Checking options."}}`,
		`{"event":"step_update","step_update":{"step_type":"thinking","text_delta":" More detail."}}`,
		`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"hello "}}`,
		`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"world"}}`,
		`{"event":"result","result":{"conversation_id":"id","status":"SUCCESS","response":"hello world","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`,
	}, "\n")
	events := make(chan agent.ProviderEvent, 8)
	saved := ""
	if err := parseAgyStream(context.Background(), strings.NewReader(input), events, func(id string) { saved = id }); err != nil {
		t.Fatal(err)
	}
	close(events)
	var text, thinking string
	var count int
	for event := range events {
		if event.Type == agent.ProviderThinkingDelta {
			thinking += event.Delta
		}
		if event.Type == agent.ProviderTextDelta {
			text += event.Delta
		}
		if event.Type == agent.ProviderDone {
			count++
			if event.Usage.TotalTokens != 5 {
				t.Fatalf("usage=%#v", event.Usage)
			}
		}
	}
	if saved != "id" || text != "hello world" || thinking != "Checking options. More detail." || count != 1 {
		t.Fatalf("saved=%q text=%q count=%d", saved, text, count)
	}
}

func TestParseAgyStreamFailsClosed(t *testing.T) {
	cases := []string{
		`{"event":"result","result":{"conversation_id":"id","status":"ERROR","response":"oops"}}`,
		`{"event":"init","conversation_id":"a"}` + "\n" + `{"event":"result","result":{"conversation_id":"b","status":"SUCCESS"}}`,
		`{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"first"}}` + "\n" + `{"event":"result","result":{"conversation_id":"id","status":"SUCCESS","response":"different"}}`,
		`{"event":"init","conversation_id":"id"}`,
		`{"event":"result","result":{"conversation_id":"id","status":"SUCCESS","response":""}}`,
	}
	for _, input := range cases {
		events := make(chan agent.ProviderEvent, 8)
		if err := parseAgyStream(context.Background(), strings.NewReader(input), events, func(string) {}); err == nil {
			t.Errorf("accepted invalid stream: %s", input)
		}
	}
}
