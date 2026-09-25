package main

import "context"

// agySetupStatus is advisory only; tool execution still validates the MCP
// policy and classifier independently at each turn.
func agySetupStatus(ctx context.Context, workspace string) backendResponse {
	response := backendResponse{Type: "response", Command: "get_agy_setup", Success: true, Provider: "agy"}
	installed := false
	ready := false
	response.AgyInstalled = &installed
	response.AgyReady = &ready
	if _, err := agyBinary(); err != nil {
		response.AgySetupMessage = err.Error()
		response.AgySetupCommand = "Install agy from https://antigravity.google/download and ensure `agy` is on PATH; then restart pi-go-agent."
		return response
	}
	installed = true
	response.AgyInstalled = &installed
	home, err := agyHome()
	if err != nil {
		response.AgySetupMessage = err.Error()
		return response
	}
	response.AgySetupCommand = "HOME=" + shellQuote(home) + " agy"
	if err := prepareAgyWorkspace(workspace); err != nil {
		response.AgySetupMessage = err.Error()
		return response
	}
	if _, err := readyAgyModels(ctx, workspace); err != nil {
		response.AgySetupMessage = err.Error()
		return response
	}
	ready = true
	response.AgyReady = &ready
	response.AgySetupMessage = "Gemini models are available."
	return response
}
