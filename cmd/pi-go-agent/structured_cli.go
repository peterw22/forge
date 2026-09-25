package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/peterw22/pi-go/internal/agent"
)

// structuredOutputWorkdir is a fixed, private, empty directory for classifier
// and summarizer CLI runs. Never run them in the workspace: CLAUDE.md, agent
// rules, or MCP overrides there are untrusted input to the safety gate. A fixed
// path also keeps the CLIs from recording a new project for every call.
func structuredOutputWorkdir() (string, error) {
	configDir := strings.TrimSpace(os.Getenv("PI_GO_CONFIG_DIR"))
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, ".pi-go")
	}
	dir := filepath.Join(configDir, "classifier-workdir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) != 0 {
		return "", errors.New("classifier working directory " + dir + " must be empty")
	}
	return dir, nil
}

// structuredRequestInput returns the single text user message of an
// output-only request and the JSON schema of its only tool.
func structuredRequestInput(request agent.Request) (string, []byte, error) {
	if len(request.Tools) != 1 {
		return "", nil, errors.New("structured output requires exactly one tool definition")
	}
	if len(request.Messages) != 1 || request.Messages[0].Role != agent.RoleUser {
		return "", nil, errors.New("structured output requires exactly one user message")
	}
	var text strings.Builder
	for _, block := range request.Messages[0].Content {
		if block.Type != "text" {
			return "", nil, errors.New("structured output supports only text input")
		}
		text.WriteString(block.Text)
	}
	if text.Len() == 0 {
		return "", nil, errors.New("structured output input is empty")
	}
	schema, err := json.Marshal(request.Tools[0].Parameters)
	if err != nil {
		return "", nil, err
	}
	return text.String(), schema, nil
}

// parseStructuredJSONText accepts exactly one JSON object, optionally wrapped
// in a single Markdown code fence, with nothing else around it. Field-level
// validation is left to the caller, as for output tool arguments.
func parseStructuredJSONText(text string) (map[string]any, error) {
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, "```") {
		body = strings.TrimPrefix(strings.TrimPrefix(body, "```"), "json")
		inner, ok := strings.CutSuffix(strings.TrimSpace(body), "```")
		if !ok {
			return nil, errors.New("structured output has an unterminated code fence")
		}
		body = inner
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, errors.New("structured output is not a JSON object")
	}
	if result == nil {
		return nil, errors.New("structured output is not a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("structured output contains text after the JSON object")
	}
	return result, nil
}
