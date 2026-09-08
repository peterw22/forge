package main

import (
	"strings"
	"testing"
)

func TestBrowserKeyboardArguments(t *testing.T) {
	for _, action := range []string{"type", "press_key"} {
		field, limit := "text", maxBrowserTextBytes
		if action == "press_key" {
			field, limit = "key", 100
		}
		for _, value := range []any{nil, true, 123, "", "\x00", string([]byte{0xff}), strings.Repeat("x", limit+1)} {
			if _, err := browserKeyboardArgument(map[string]any{field: value}, action); err == nil {
				t.Fatalf("accepted %s=%v", field, value)
			}
		}
	}
	for _, key := range []string{"Enter", "Tab", "Shift+Tab", "ControlOrMeta+A", "Backspace", "Escape", "ArrowDown"} {
		if _, err := browserKeyboardArgument(map[string]any{"key": key}, "press_key"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := browserKeyboardArgument(map[string]any{"text": "Hello 世界\nsecond line"}, "type"); err != nil {
		t.Fatal(err)
	}
	if _, err := browserKeyboardArgument(map[string]any{"key": "\n"}, "press_key"); err == nil {
		t.Fatal("literal newline accepted as key name")
	}
}
