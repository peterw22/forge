package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"time"
)

// claudeSetupStatus is advisory: actual turns still verify MCP-only tool
// inventory and classify each tool call.
func claudeSetupStatus(ctx context.Context) backendResponse {
	response := backendResponse{Type: "response", Command: "get_claude_setup", Success: true, Provider: "claude"}
	installed, ready := false, false
	response.ClaudeInstalled = &installed
	response.ClaudeReady = &ready
	if _, err := exec.LookPath("claude"); err != nil {
		response.ClaudeSetupMessage = "Claude Code is not installed on the agent server."
		response.ClaudeSetupCommand = "Install Claude Code from https://code.claude.com/docs/en/setup and ensure `claude` is on PATH; then restart pi-go-agent."
		return response
	}
	installed = true
	response.ClaudeInstalled = &installed
	response.ClaudeSetupCommand = "claude auth login"
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "claude", "auth", "status").Output()
	if err != nil {
		response.ClaudeSetupMessage = "Claude Code sign-in required on the agent server."
		return response
	}
	var status struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err := json.Unmarshal(output, &status); err != nil || !status.LoggedIn {
		response.ClaudeSetupMessage = "Claude Code sign-in required on the agent server."
		return response
	}
	ready = true
	response.ClaudeReady = &ready
	response.ClaudeSetupMessage = "Claude Code models are available."
	return response
}
