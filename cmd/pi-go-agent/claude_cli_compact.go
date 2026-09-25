package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/peterw22/pi-go/internal/agent"
)

// CompactConversation forwards Pi Go's /compact to the CLI conversation bound
// to the session. Claude compacts natively under the same session ID. agy has
// no compaction command, and local summarization would not shrink its context.
func (router *providerRouter) CompactConversation(ctx context.Context, request agent.Request, instructions string) (agent.ProviderCompaction, bool, error) {
	switch {
	case strings.HasPrefix(request.Model, "claude/"):
		provider, _ := router.claude.(*claudeCLIProvider)
		if provider == nil {
			return agent.ProviderCompaction{}, true, errors.New("Claude Code is not installed")
		}
		result, err := provider.compactConversation(ctx, request, instructions)
		return result, true, err
	case strings.HasPrefix(request.Model, "agy/"):
		return agent.ProviderCompaction{}, true, errors.New("agy has no compaction command; start a new session to reduce its context")
	}
	return agent.ProviderCompaction{}, false, nil
}

// compactConversation runs Claude's /compact on the bound conversation with no
// tools, MCP servers, or settings. Slash commands must stay enabled for this
// one invocation; the prompt is only /compact and the user's instructions.
func (p *claudeCLIProvider) compactConversation(ctx context.Context, req agent.Request, instructions string) (agent.ProviderCompaction, error) {
	model, ok := strings.CutPrefix(req.Model, "claude/")
	if !ok || !claudeModelAllowed(model) {
		return agent.ProviderCompaction{}, errors.New("unsupported Claude model")
	}
	if req.SessionID == "" {
		return agent.ProviderCompaction{}, errors.New("Claude compaction requires a session")
	}
	if info, err := os.Stat(req.WorkingDirectory); err != nil || !info.IsDir() {
		return agent.ProviderCompaction{}, errors.New("Claude working directory is invalid")
	}
	p.mu.Lock()
	conversation := p.conversations[req.SessionID].ID
	p.mu.Unlock()
	if req.LoadProviderConversation != nil {
		id, err := req.LoadProviderConversation("claude", req.Model, req.WorkingDirectory)
		if err != nil {
			return agent.ProviderCompaction{}, err
		}
		conversation = id
	}
	if conversation == "" {
		return agent.ProviderCompaction{}, errors.New("no Claude conversation to compact yet")
	}
	prompt := "/compact"
	if custom := strings.TrimSpace(instructions); custom != "" {
		prompt += " " + custom
	}
	args := []string{"--print", "--model", model, "--output-format", "stream-json", "--verbose", "--tools", "", "--restricted", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "", "--permission-mode", "dontAsk", "--resume", conversation}
	if claudeEffortAllowed(req.Thinking) {
		args = append(args, "--effort", req.Thinking)
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, p.binary, args...)
	// Claude stores sessions per project directory; resume from the bound one.
	cmd.Dir = req.WorkingDirectory
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agent.ProviderCompaction{}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: 4096}
	if err := cmd.Start(); err != nil {
		return agent.ProviderCompaction{}, fmt.Errorf("start Claude: %w", err)
	}
	result, parseErr := parseClaudeCompactStream(ctx, stdout, conversation)
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return agent.ProviderCompaction{}, fmt.Errorf("Claude compaction: %w; stderr: %s; exit: %v", parseErr, stderr.String(), waitErr)
	}
	if err := ctx.Err(); err != nil {
		return agent.ProviderCompaction{}, err
	}
	if waitErr != nil {
		return agent.ProviderCompaction{}, fmt.Errorf("Claude exited: %w: %s", waitErr, stderr.String())
	}
	return result, nil
}

// parseClaudeCompactStream requires a manual compact_boundary on the expected
// session, the continuation summary Claude injects after it, and a successful
// result. Every event must belong to the resumed session.
func parseClaudeCompactStream(ctx context.Context, reader io.Reader, conversation string) (agent.ProviderCompaction, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	var result agent.ProviderCompaction
	boundary, summarySeen, done := false, false, false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return agent.ProviderCompaction{}, err
		}
		if done {
			return agent.ProviderCompaction{}, errors.New("Claude emitted events after result")
		}
		var msg struct {
			Type       string            `json:"type"`
			Subtype    string            `json:"subtype"`
			SessionID  string            `json:"session_id"`
			Tools      []string          `json:"tools"`
			MCPServers []json.RawMessage `json:"mcp_servers"`
			Metadata   struct {
				Trigger    string `json:"trigger"`
				PreTokens  int    `json:"pre_tokens"`
				PostTokens int    `json:"post_tokens"`
			} `json:"compact_metadata"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			IsError bool `json:"is_error"`
			Usage   struct {
				Input      int `json:"input_tokens"`
				Output     int `json:"output_tokens"`
				CacheRead  int `json:"cache_read_input_tokens"`
				CacheWrite int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return agent.ProviderCompaction{}, fmt.Errorf("decode Claude event: %w", err)
		}
		if msg.SessionID != "" && msg.SessionID != conversation {
			return agent.ProviderCompaction{}, errors.New("Claude compacted a different session")
		}
		switch msg.Type {
		case "system":
			switch msg.Subtype {
			case "init":
				if len(msg.MCPServers) != 0 || len(msg.Tools) != 0 {
					return agent.ProviderCompaction{}, errors.New("Claude compaction exposed tools or MCP servers")
				}
			case "compact_boundary":
				if boundary || msg.Metadata.Trigger != "manual" {
					return agent.ProviderCompaction{}, errors.New("unexpected Claude compaction boundary")
				}
				boundary = true
				result.TokensBefore, result.TokensAfter = msg.Metadata.PreTokens, msg.Metadata.PostTokens
			}
		case "user":
			// Only the first message after the boundary is the continuation
			// summary; the command's own stdout echo is never one.
			if boundary && !summarySeen {
				summarySeen = true
				text := strings.TrimSpace(claudeMessageText(msg.Message.Content))
				if !strings.HasPrefix(text, "<local-command-") {
					result.Summary = text
				}
			}
		case "assistant":
			return agent.ProviderCompaction{}, errors.New("Claude answered instead of compacting")
		case "result":
			if msg.SessionID != conversation || msg.IsError || msg.Subtype != "success" {
				return agent.ProviderCompaction{}, errors.New("Claude compaction failed")
			}
			if !boundary || result.Summary == "" {
				return agent.ProviderCompaction{}, errors.New("Claude did not compact the conversation")
			}
			u := msg.Usage
			result.Usage = agent.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, TotalTokens: u.Input + u.Output + u.CacheRead + u.CacheWrite}
			done = true
		}
	}
	if err := scanner.Err(); err != nil {
		return agent.ProviderCompaction{}, err
	}
	if err := ctx.Err(); err != nil {
		return agent.ProviderCompaction{}, err
	}
	if !done {
		return agent.ProviderCompaction{}, errors.New("Claude compaction ended without a result")
	}
	return result, nil
}

// claudeMessageText accepts a string or an array of content blocks.
func claudeMessageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}
