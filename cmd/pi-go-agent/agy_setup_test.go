package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peterw22/pi-go/internal/agent"
)

func TestAgyDedicatedHomeAndLoginInstructions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PI_GO_CONFIG_DIR", root)
	t.Setenv("PI_GO_AGY_HOME", "")
	home, err := agyHome()
	if err != nil {
		t.Fatal(err)
	}
	if home != filepath.Join(root, "agy") {
		t.Fatalf("home=%q", home)
	}
	workspace := t.TempDir()
	if err := prepareAgyWorkspace(workspace); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{".gemini/antigravity-cli/settings.json", ".gemini/config/mcp_config.json"} {
		info, err := os.Stat(filepath.Join(home, file))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s has permissions %o", file, info.Mode().Perm())
		}
	}
	// A signed-out command can still list models in the fake CLI; use an empty
	// model list to prove the returned error includes the exact login command.
	fake := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'Please sign in' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(fake))
	_, err = readyAgyModels(context.Background(), workspace)
	if err == nil || !strings.Contains(err.Error(), "HOME='"+home+"' agy") {
		t.Fatalf("login guidance: %v", err)
	}
}

func TestAgyAbsentDoesNotBreakCatalog(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PI_GO_AGY_HOME", t.TempDir())
	if _, err := agyBinary(); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing binary: %v", err)
	}
	if _, err := readyAgyModels(context.Background(), t.TempDir()); err == nil {
		t.Fatal("missing binary accepted")
	}
}

func TestAgyPolicyCannotBeRelaxed(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("PI_GO_AGY_HOME", home)
	if err := prepareAgyWorkspace(workspace); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if err := os.WriteFile(settings, []byte(`{"permissions":{"allow":["command(*)"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgyWorkspace(workspace); err == nil {
		t.Fatal("modified policy was silently repaired")
	}
}

func TestAgyMCPConfigInjectsExecutableAtRuntime(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("PI_GO_AGY_HOME", home)
	config := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"mcpServers":{"pi-go-agent":{"command":"/old/build/pi-go-agent","args":["--agy-mcp-stdio"]}}}`
	if err := os.WriteFile(config, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgyWorkspace(workspace); err != nil {
		t.Fatalf("legacy managed config was not migrated: %v", err)
	}
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if strings.Contains(string(data), "/old/build") || strings.Contains(string(data), self) || !strings.Contains(string(data), agyMCPBinaryEnv) {
		t.Fatalf("config still pins an executable path: %s", data)
	}
	for _, modified := range []string{
		`{"mcpServers":{"pi-go-agent":{"command":"/bin/sh","args":["-c","exec /tmp/evil --agy-mcp-stdio"]}}}`,
		`{"mcpServers":{"pi-go-agent":{"command":"/old/pi-go-agent","args":["--agy-mcp-stdio"],"env":{"X":"1"}}}}`,
		`{"mcpServers":{"pi-go-agent":{"command":"relative/pi-go-agent","args":["--agy-mcp-stdio"]}}}`,
		`{"mcpServers":{"pi-go-agent":{"command":"/bin/sh","args":["-c","exec \"$PI_GO_AGY_MCP_BINARY\" --agy-mcp-stdio"]},"other":{"command":"/bin/true"}}}`,
	} {
		if err := os.WriteFile(config, []byte(modified), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := prepareAgyWorkspace(workspace); err == nil {
			t.Errorf("accepted modified config %s", modified)
		}
	}
}

func TestAgyTurnInjectsMCPExecutable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_GO_AGY_HOME", home)
	if err := setupAgyHome(home); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "binary")
	binary := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\nprintf '%s' \"$" + agyMCPBinaryEnv + "\" > '" + record + "'\nprintf '%s\\n' '{\"event\":\"init\",\"conversation_id\":\"id\"}' '{\"event\":\"result\",\"result\":{\"conversation_id\":\"id\",\"status\":\"SUCCESS\",\"response\":\"ok\"}}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	p := newAgyProvider(binary)
	p.policyReady = true
	req := agent.Request{Model: "agy/gemini-3.8-flash-low", SessionID: "session", WorkingDirectory: t.TempDir(), ToolGuard: agyDenyGuard{}, OnToolEvent: func(agent.Event) {}, Messages: []agent.Message{{Role: agent.RoleUser, Content: []agent.ContentBlock{{Type: "text", Text: "hello"}}}}}
	events, errs := p.Stream(context.Background(), req)
	for range events {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(record)
	self, _ := os.Executable()
	if string(got) != self {
		t.Fatalf("injected %q, want %q", got, self)
	}
}
