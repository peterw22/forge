package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/peterw22/forge/internal/agent"
)

const claudeStructuredInstruction = "Return your result only as the structured output matching the supplied JSON schema. Do not return it as ordinary text or Markdown."

// CompleteStructured runs one classifier or summarizer request through Claude
// Code with no tools, no MCP servers, no settings, and no saved session. The
// payload is sent on stdin so it does not appear in the process list.
func (p *claudeCLIProvider) CompleteStructured(ctx context.Context, req agent.Request) (map[string]any, error) {
	if p == nil {
		return nil, errors.New("Claude Code is not installed")
	}
	model, ok := strings.CutPrefix(req.Model, "claude/")
	if !ok || !claudeModelAllowed(model) {
		return nil, errors.New("unsupported Claude model")
	}
	if err := claudeModelEffortError(model, req.Thinking); err != nil {
		return nil, err
	}
	input, schema, err := structuredRequestInput(req)
	if err != nil {
		return nil, err
	}
	dir, err := structuredOutputWorkdir()
	if err != nil {
		return nil, err
	}
	args := []string{"--print", "--model", model, "--effort", req.Thinking, "--output-format", "stream-json", "--verbose", "--json-schema", string(schema), "--system-prompt", req.SystemPrompt + "\n\n" + claudeStructuredInstruction, "--tools", "", "--restricted", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "", "--permission-mode", "dontAsk", "--no-session-persistence", "--disable-slash-commands"}
	cmd := exec.CommandContext(ctx, p.binary, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: 4096}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Claude: %w", err)
	}
	result, parseErr := parseClaudeStructuredStream(ctx, stdout)
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return nil, fmt.Errorf("Claude structured output: %w; stderr: %s; exit: %v", parseErr, stderr.String(), waitErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if waitErr != nil {
		return nil, fmt.Errorf("Claude exited: %w: %s", waitErr, stderr.String())
	}
	return result, nil
}

// parseClaudeStructuredStream verifies that Claude started without MCP servers
// or tools (other than its internal structured-output tool) and returns the
// result's structured_output object.
func parseClaudeStructuredStream(ctx context.Context, reader io.Reader) (map[string]any, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLine)
	var session string
	var result map[string]any
	initialized, done := false, false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if done {
			return nil, errors.New("Claude emitted events after result")
		}
		var msg struct {
			Type              string            `json:"type"`
			Subtype           string            `json:"subtype"`
			SessionID         string            `json:"session_id"`
			Tools             []string          `json:"tools"`
			MCPServers        []json.RawMessage `json:"mcp_servers"`
			IsError           bool              `json:"is_error"`
			PermissionDenials []json.RawMessage `json:"permission_denials"`
			StructuredOutput  json.RawMessage   `json:"structured_output"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return nil, fmt.Errorf("decode Claude event: %w", err)
		}
		switch msg.Type {
		case "system":
			if msg.Subtype != "init" {
				continue
			}
			if initialized || msg.SessionID == "" || len(msg.MCPServers) != 0 {
				return nil, errors.New("Claude tool-free boundary was not established")
			}
			for _, name := range msg.Tools {
				if name != "StructuredOutput" {
					return nil, errors.New("Claude exposed unexpected tool: " + name)
				}
			}
			initialized = true
			session = msg.SessionID
		case "result":
			if !initialized || msg.SessionID != session || msg.IsError || msg.Subtype != "success" || len(msg.PermissionDenials) > 0 {
				return nil, errors.New("Claude structured turn failed")
			}
			if err := json.Unmarshal(msg.StructuredOutput, &result); err != nil || result == nil {
				return nil, errors.New("Claude returned no structured output object")
			}
			done = true
		default:
			if !initialized {
				return nil, errors.New("Claude emitted events before tool boundary verification")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !done {
		return nil, errors.New("Claude ended without a result")
	}
	return result, nil
}
