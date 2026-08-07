package main

import "testing"

func TestParseWebSocketEndpoint(t *testing.T) {
	address, path, err := parseWebSocketEndpoint("ws://127.0.0.1:7346/agent", false)
	if err != nil {
		t.Fatal(err)
	}
	if address != "127.0.0.1:7346" || path != "/agent" {
		t.Fatalf("got %q %q", address, path)
	}
	if _, _, err := parseWebSocketEndpoint("ws://0.0.0.0:7346/ws", false); err == nil {
		t.Fatal("non-loopback WebSocket listener accepted")
	}
	if _, _, err := parseWebSocketEndpoint("wss://127.0.0.1:7346/ws", false); err == nil {
		t.Fatal("unsupported TLS listener accepted")
	}
	if address, _, err := parseWebSocketEndpoint("ws://192.168.50.50:7346/ws", true); err != nil || address != "192.168.50.50:7346" {
		t.Fatalf("explicit remote listener = %q, %v", address, err)
	}
}

func TestAllowedWebSocketOrigin(t *testing.T) {
	for _, origin := range []string{"", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if !allowedWebSocketOrigin(origin) {
			t.Fatalf("origin %q rejected", origin)
		}
	}
	if allowedWebSocketOrigin("https://example.com") {
		t.Fatal("remote origin accepted")
	}
}
