package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/peterw22/pi-go/internal/agent"
)

var agyConversationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var agyConversationCleanupMissing sync.Once

// CompleteStructured runs one classifier or summarizer request as a fresh agy
// conversation. agy has no schema or system-prompt flag, so the schema is part
// of the prompt and the reply must be exactly one JSON object. The verified
// policy denies every native agy tool, and neither MCP turn credentials nor the
// MCP executable are passed, so the pi-go-agent MCP child cannot start: the run
// has no usable tools.
func (p *agyProvider) CompleteStructured(ctx context.Context, req agent.Request) (map[string]any, error) {
	if p == nil {
		return nil, errors.New("agy provider is not enabled")
	}
	if !p.policyReady {
		return nil, errors.New("agy MCP-only policy has not been verified")
	}
	model, ok := strings.CutPrefix(req.Model, "agy/")
	if !ok || !strings.HasPrefix(model, "gemini-") || !validClassifierModelID(model) || strings.Contains(model, "/") {
		return nil, errors.New("invalid agy model")
	}
	input, schema, err := structuredRequestInput(req)
	if err != nil {
		return nil, err
	}
	home, err := agyHome()
	if err != nil {
		return nil, err
	}
	dir, err := structuredOutputWorkdir()
	if err != nil {
		return nil, err
	}
	if err := checkAgyPolicy(home, dir); err != nil {
		return nil, err
	}
	prompt := req.SystemPrompt + "\n\nRespond with exactly one JSON object and nothing else: no Markdown, code fences, or commentary. Its fields are your result and must match this JSON schema exactly:\n" + string(schema) + "\n\nInput:\n" + input
	cmd := exec.CommandContext(ctx, p.binary, "--output-format", "stream-json", "--print-timeout", "0", "--disable-slash-commands", "--model", model, "--print="+prompt)
	cmd.Dir = dir
	// Empty MCP values override any inherited ones; the last value wins.
	cmd.Env = append(os.Environ(), "HOME="+home, agyMCPBinaryEnv+"=", "PI_GO_AGY_MCP_ADDRESS=", "PI_GO_AGY_MCP_TOKEN=")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: 4096}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agy: %w", err)
	}
	events := make(chan agent.ProviderEvent, 32)
	collected := make(chan string, 1)
	go func() {
		var text strings.Builder
		for event := range events {
			if event.Type == agent.ProviderTextDelta {
				text.WriteString(event.Delta)
			}
		}
		collected <- text.String()
	}()
	var conversation string
	parseErr := parseAgyStream(ctx, stdout, events, func(id string) { conversation = id })
	close(events)
	text := <-collected
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if conversation != "" {
		removeAgyConversation(home, conversation)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("agy structured output: %w; stderr: %s; exit: %v", parseErr, stderr.String(), waitErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if waitErr != nil {
		return nil, fmt.Errorf("agy exited: %w: %s", waitErr, stderr.String())
	}
	return parseStructuredJSONText(text)
}

// removeAgyConversation deletes the files agy persisted for a one-shot
// classifier conversation so they do not accumulate in Pi Go's agy home. Only
// entries named exactly after the conversation UUID (optionally with an
// extension) are removed, and symlinks are never followed.
func removeAgyConversation(home, id string) {
	if !agyConversationIDPattern.MatchString(id) {
		return
	}
	var matches []string
	_ = filepath.WalkDir(filepath.Join(home, ".gemini"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if name := entry.Name(); name == id || strings.HasPrefix(name, id+".") {
			matches = append(matches, path)
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if len(matches) == 0 {
		agyConversationCleanupMissing.Do(func() {
			log.Printf("agy classifier conversation %s left no files named after it under %s; nothing was cleaned up", id, home)
		})
	}
	for _, path := range matches {
		if err := os.RemoveAll(path); err != nil {
			log.Printf("remove agy classifier conversation %s: %v", id, err)
		}
	}
}
