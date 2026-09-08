package main

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const maxBrowserTextBytes = 10000

func browserTypeSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"text"},
		"properties": map[string]any{
			"text": map[string]any{"type": "string", "description": "Literal UTF-8 text to type into the focused field, at most 10000 bytes. Not a key expression. Click a field first; use browser_press_key for Enter, Tab, selection, or deletion."},
		},
	}
}

func browserPressKeySchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"key"},
		"properties": map[string]any{
			"key": map[string]any{"type": "string", "description": "One Playwright key or chord, e.g. Enter, Tab, Shift+Tab, Backspace, Escape, ArrowDown, or ControlOrMeta+A. ControlOrMeta is Command on macOS and Control elsewhere. Sends keydown and keyup; no held keys across calls."},
		},
	}
}

func browserKeyboardArgument(args map[string]any, action string) (string, error) {
	field, limit := "text", maxBrowserTextBytes
	if action == "press_key" {
		field, limit = "key", 100
	}
	value, ok := args[field].(string)
	if !ok || value == "" || len(value) > limit || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("browser " + field + " must be nonempty UTF-8 within its size limit, without NUL characters")
	}
	if action == "press_key" && strings.ContainsAny(value, "\r\n\t") {
		return "", errors.New("use named keys such as Enter or Tab, not control characters")
	}
	return value, nil
}

func (session *browserSession) sendKeyboard(action string, args map[string]any) error {
	value, err := browserKeyboardArgument(args, action)
	if err != nil {
		return err
	}
	session.cursorPlaced = false
	keyboard := session.page.Keyboard()
	if action == "type" {
		// Type emits character key events where supported; Playwright inserts
		// Unicode characters that do not have physical keyboard equivalents.
		return keyboard.Type(value)
	}
	err = keyboard.Press(value)
	if err != nil {
		// A malformed chord or page closing during a press must not leave a
		// modifier logically held in a later tool call.
		for _, modifier := range []string{"Shift", "Control", "Alt", "Meta"} {
			_ = keyboard.Up(modifier)
		}
	}
	return err
}
