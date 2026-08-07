package main

import (
	"io"
	"strings"
	"testing"

	"github.com/peterw22/pi-go/internal/agent"
)

func TestConsumeBridgeEmitsUpstreamTransport(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	defer inputReader.Close()
	events := make(chan agent.ProviderEvent, 1)
	bridge := strings.NewReader("{\"type\":\"start\",\"transport\":\"WS\"}\n")
	if err := consumeBridge(bridge, inputWriter, events); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Type != agent.ProviderTransport || event.Transport != "WS" {
			t.Fatalf("event = %#v", event)
		}
	default:
		t.Fatal("transport event was not emitted")
	}
}
