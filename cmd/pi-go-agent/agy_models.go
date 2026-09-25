package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// discoverAgyModels is deliberately separate from configuredModels: a model
// must not be advertised until its MCP-only tool policy is actually enforced.
// agy models prints tab-separated IDs and display names.
func discoverAgyModels(ctx context.Context, binary string) ([]modelInfo, error) {
	return discoverAgyModelsAt(ctx, binary, "")
}

func discoverAgyModelsAt(ctx context.Context, binary, home string) ([]modelInfo, error) {
	if binary == "" {
		return nil, errors.New("agy binary is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "models")
	if home != "" {
		cmd.Env = append(os.Environ(), "HOME="+home)
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list agy models: %w", err)
	}
	if len(output) > 1<<20 {
		return nil, errors.New("agy model output exceeds limit")
	}
	var models []modelInfo
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(output), "\n") {
		id, name, hasTab := strings.Cut(strings.TrimSpace(line), "\t")
		if !hasTab || !strings.HasPrefix(id, "gemini-") || !validClassifierModelID(id) || strings.Contains(id, "/") || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, modelInfo{ID: "agy/" + id, Provider: "agy", Label: "Antigravity · " + strings.TrimSpace(name)})
	}
	if len(models) == 0 {
		return nil, errors.New("agy returned no model IDs")
	}
	return models, nil
}
