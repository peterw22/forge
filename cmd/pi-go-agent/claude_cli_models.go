package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// claudeModel is one model Claude Code offers the signed-in account.
type claudeModel struct {
	ID           string
	Name         string
	EffortLevels []string // empty when Claude does not report effort support
}

// Pinned models verified with Claude Code 2.1.282, used when discovery fails.
var claudeFallbackModels = []claudeModel{
	{ID: "claude-opus-5-5", Name: "Opus 5.5", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-sonnet-5", Name: "Sonnet 5", EffortLevels: []string{"low", "medium", "high", "xhigh", "max"}},
	{ID: "claude-haiku-4-5-20251001", Name: "Haiku 4.5"},
}

// Discovered IDs are passed to --model, so only plain pinned IDs are accepted.
var claudeModelIDPattern = regexp.MustCompile(`^claude-[a-z0-9][a-z0-9.-]*$`)

const (
	claudeCatalogTTL   = 5 * time.Minute
	claudeCatalogRetry = 30 * time.Second
)

var claudeCatalog struct {
	mu      sync.Mutex
	binary  string
	models  []claudeModel
	fetched time.Time
	ok      bool
}

// claudeModels returns the account's models, refreshing a stale cache. A
// failed discovery falls back to the pinned list and is retried sooner.
func claudeModels(ctx context.Context, binary string) []claudeModel {
	claudeCatalog.mu.Lock()
	defer claudeCatalog.mu.Unlock()
	ttl := claudeCatalogTTL
	if !claudeCatalog.ok {
		ttl = claudeCatalogRetry
	}
	if claudeCatalog.binary == binary && time.Since(claudeCatalog.fetched) < ttl {
		return claudeCatalog.models
	}
	models, err := discoverClaudeModels(ctx, binary)
	claudeCatalog.binary, claudeCatalog.fetched, claudeCatalog.ok = binary, time.Now(), err == nil
	if err != nil {
		models = claudeFallbackModels
	}
	claudeCatalog.models = models
	return models
}

func claudeModelInfo(model string) (claudeModel, bool) {
	binary, err := exec.LookPath("claude")
	if err != nil {
		return claudeModel{}, false
	}
	models := claudeModels(context.Background(), binary)
	index := slices.IndexFunc(models, func(m claudeModel) bool { return m.ID == model })
	if index < 0 {
		return claudeModel{}, false
	}
	return models[index], true
}

func claudeModelAllowed(model string) bool {
	_, ok := claudeModelInfo(model)
	return ok
}

// claudeModelEffortError rejects effort levels the model does not list.
func claudeModelEffortError(model, level string) error {
	if !claudeEffortAllowed(level) {
		return errors.New("Claude thinking effort must be low, medium, high, xhigh, or max (off and minimal are unsupported)")
	}
	info, ok := claudeModelInfo(model)
	if ok && len(info.EffortLevels) > 0 && !slices.Contains(info.EffortLevels, level) {
		return fmt.Errorf("Claude %s supports thinking effort %s", info.Name, strings.Join(info.EffortLevels, ", "))
	}
	return nil
}

// discoverClaudeModels reads the model list from Claude Code's stream-json
// initialize control response. No prompt is sent, so no model is called.
func discoverClaudeModels(ctx context.Context, binary string) ([]claudeModel, error) {
	dir, err := structuredOutputWorkdir()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--print", "--output-format", "stream-json", "--verbose", "--input-format", "stream-json", "--tools", "", "--restricted", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "", "--permission-mode", "dontAsk", "--no-session-persistence", "--disable-slash-commands")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(`{"type":"control_request","request_id":"pi-go-models","request":{"subtype":"initialize"}}` + "\n")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("query Claude models: %w", err)
	}
	return parseClaudeModels(output)
}

func parseClaudeModels(output []byte) ([]claudeModel, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 64*1024), maxStreamLine)
	for scanner.Scan() {
		var msg struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Response  struct {
					Models []struct {
						ResolvedModel         string   `json:"resolvedModel"`
						DisplayName           string   `json:"displayName"`
						SupportedEffortLevels []string `json:"supportedEffortLevels"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil || msg.Type != "control_response" || msg.Response.RequestID != "pi-go-models" {
			continue
		}
		var models []claudeModel
		for _, item := range msg.Response.Response.Models {
			if !claudeModelIDPattern.MatchString(item.ResolvedModel) {
				continue
			}
			name := strings.TrimSpace(item.DisplayName)
			if name == "" || strings.HasPrefix(name, "Default") {
				name = item.ResolvedModel
			}
			// Aliases such as "default" and "opus" resolve to the same pinned
			// ID; keep it once, preferring a real display name.
			if index := slices.IndexFunc(models, func(m claudeModel) bool { return m.ID == item.ResolvedModel }); index >= 0 {
				if models[index].Name == models[index].ID {
					models[index].Name = name
				}
				continue
			}
			models = append(models, claudeModel{ID: item.ResolvedModel, Name: name, EffortLevels: item.SupportedEffortLevels})
		}
		if len(models) == 0 {
			return nil, errors.New("Claude reported no models")
		}
		return models, nil
	}
	return nil, errors.New("Claude did not report its models")
}
