package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// agyHome keeps agy's login and permission policy separate from the user's
// normal Antigravity account. HOME must point to this directory when agy runs.
func agyHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("PI_GO_AGY_HOME")); value != "" {
		if !filepath.IsAbs(value) {
			return "", errors.New("PI_GO_AGY_HOME must be an absolute path")
		}
		return filepath.Clean(value), nil
	}
	config := strings.TrimSpace(os.Getenv("PI_GO_CONFIG_DIR"))
	if config == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		config = filepath.Join(home, ".pi-go")
	}
	return filepath.Join(config, "agy"), nil
}

func agyLoginInstruction(home string) string {
	return fmt.Sprintf("Antigravity sign-in required. Run in a terminal: HOME=%s agy (select Google OAuth); then reconnect or refresh models", shellQuote(home))
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func agyBinary() (string, error) {
	binary, err := exec.LookPath("agy")
	if err != nil {
		return "", errors.New("agy is not installed; install the Antigravity CLI and ensure `agy` is on PATH, then restart pi-go-agent")
	}
	return binary, nil
}

// The managed MCP entry never names the pi-go-agent executable. agy passes its
// environment to MCP children, so each turn injects the running executable in
// agyMCPBinaryEnv. Without it the launcher cannot start, and the run has no
// tools.
const (
	agyMCPBinaryEnv = "PI_GO_AGY_MCP_BINARY"
	agyMCPLauncher  = "/bin/sh"
	agyMCPScript    = `exec "$PI_GO_AGY_MCP_BINARY" --agy-mcp-stdio`
)

func managedAgyMCPServer() map[string]any {
	return map[string]any{"command": agyMCPLauncher, "args": []string{"-c", agyMCPScript}}
}

// setupAgyHome creates only absent files. Never rewrite settings that a user
// modified: the policy verifier must reject them rather than relaxing access.
func setupAgyHome(home string) error {
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	config := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	if err := os.Chmod(home, 0700); err != nil {
		return err
	}
	for _, file := range []string{settings, config} {
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return err
		}
	}
	policy := map[string]any{"permissions": map[string]any{
		"allow": []string{"mcp(pi-go-agent/*)"},
		"deny":  []string{"read_file(*)", "write_file(*)", "read_url(*)", "execute_url(*)", "command(*)", "unsandboxed(*)"},
	}}
	mcp := map[string]any{"mcpServers": map[string]any{"pi-go-agent": managedAgyMCPServer()}}
	for _, item := range []struct {
		path  string
		value any
	}{{settings, policy}, {config, mcp}} {
		data, err := json.MarshalIndent(item.value, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		file, err := os.OpenFile(item.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			_ = os.Remove(item.path)
			return fmt.Errorf("write agy configuration: %v %v", err, closeErr)
		}
	}
	return refreshManagedAgyMCP(config)
}

// refreshManagedAgyMCP checks an existing MCP-only configuration. The legacy
// managed entry, which pinned an absolute executable path, is migrated to the
// launcher. Any other configuration fails closed; remove it to regenerate it.
func refreshManagedAgyMCP(config string) error {
	data, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	var doc struct {
		MCPServers map[string]struct {
			Command  string            `json:"command"`
			Args     []string          `json:"args"`
			Env      map[string]string `json:"env"`
			Disabled bool              `json:"disabled,omitempty"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	entry, ok := doc.MCPServers["pi-go-agent"]
	if !ok || len(doc.MCPServers) != 1 || len(entry.Env) != 0 || entry.Disabled {
		return errors.New("agy MCP configuration was modified; remove the dedicated mcp_config.json to regenerate it")
	}
	if entry.Command == agyMCPLauncher && len(entry.Args) == 2 && entry.Args[0] == "-c" && entry.Args[1] == agyMCPScript {
		return nil
	}
	if !filepath.IsAbs(entry.Command) || len(entry.Args) != 1 || entry.Args[0] != "--agy-mcp-stdio" {
		return errors.New("agy MCP configuration was modified; remove the dedicated mcp_config.json to regenerate it")
	}
	payload, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"pi-go-agent": managedAgyMCPServer()}}, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	temporary := config + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, config); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func prepareAgyWorkspace(workspace string) error {
	home, err := agyHome()
	if err != nil {
		return err
	}
	if err := setupAgyHome(home); err != nil {
		return err
	}
	return checkAgyPolicy(home, workspace)
}

// readyAgyModels is best effort for catalog listing. A signed-out or missing
// CLI must not disrupt Codex or other providers.
func readyAgyModels(ctx context.Context, workspace string) ([]modelInfo, error) {
	binary, err := agyBinary()
	if err != nil {
		return nil, err
	}
	home, err := agyHome()
	if err != nil {
		return nil, err
	}
	if err := setupAgyHome(home); err != nil {
		return nil, err
	}
	if err := checkAgyPolicy(home, workspace); err != nil {
		return nil, err
	}
	models, err := discoverAgyModelsAt(ctx, binary, home)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", agyLoginInstruction(home), err)
	}
	return models, nil
}
