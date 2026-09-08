package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/peterw22/pi-go/internal/agent"
)

func builtInTools(cwd string, extras ...agent.Tool) []agent.Tool {
	tools := []agent.Tool{
		{Name: "read", Description: "Read a text file or attach a supported image (PNG, JPEG, GIF, WebP).", Parameters: objectSchema("path"), Execute: readTool(cwd)},
		{Name: "write", Description: "Write a complete UTF-8 text file. Use this for new files or intentional full rewrites.", Parameters: objectSchema("path", "content"), Execute: writeTool(cwd)},
		{Name: "replace", Description: "Replace exactly one span in an existing UTF-8 file. Provide path, newText, and exactly one of oldText or oldRegex. Prefer a concise RE2 oldRegex with stable first/last-line anchors for large blocks; this reduces output tokens because you do not need to reproduce the whole file. Use (?s) for multiline dot matching and (?m) for ^/$ line anchors. The operation always fails without writing unless the exact text or regex has one and only one non-empty match; multiple matches are never allowed. newText is literal and does not expand capture groups.", Parameters: replaceSchema(), Execute: replaceTool(cwd)},
		{Name: "bash", Description: "Run a shell command in the project directory. Always provide a concise plain-language description of what the command does and why it is being run; this description is shown to the user before and during execution.", Parameters: bashSchema(), Execute: bashTool(cwd)},
	}
	return append(tools, extras...)
}

func bashSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command":     map[string]any{"type": "string", "description": "The exact shell command to execute."},
			"description": map[string]any{"type": "string", "description": "A concise user-facing explanation of what this command does and why it is needed."},
		},
		"required":             []string{"command", "description"},
		"additionalProperties": false,
	}
}

func replaceSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":     map[string]any{"type": "string", "description": "Existing UTF-8 file inside the workspace."},
			"oldText":  map[string]any{"type": "string", "description": "Exact non-empty text to replace. It must occur exactly once. Mutually exclusive with oldRegex."},
			"oldRegex": map[string]any{"type": "string", "description": "RE2-compatible non-empty regular expression that must match exactly one non-empty span. Prefer concise stable boundary lines to reduce output tokens. Use (?s) for multiline matching and (?m) for line anchors. Mutually exclusive with oldText."},
			"newText":  map[string]any{"type": "string", "description": "Literal replacement text; capture references such as $1 are not expanded."},
		},
		"required":             []string{"path", "newText"},
		"additionalProperties": false,
	}
}

func objectSchema(required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	properties := map[string]any{}
	for _, name := range required {
		properties[name] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func readTool(cwd string) agent.ToolExecutor {
	return func(_ context.Context, args map[string]any, _ func(agent.ToolResult)) (agent.ToolResult, error) {
		path, err := projectPath(cwd, stringArg(args, "path"))
		if err != nil {
			return agent.ToolResult{}, err
		}
		if isProtectedCredentialPath(path) {
			return agent.ToolResult{}, fmt.Errorf("access to Pi Go provider credentials is blocked")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return agent.ToolResult{}, err
		}
		mimeType := http.DetectContentType(data)
		if isSupportedImage(mimeType) {
			const maxImageBytes = 10 << 20
			if len(data) > maxImageBytes {
				return agent.ToolResult{}, fmt.Errorf("image exceeds %d MiB limit", maxImageBytes>>20)
			}
			return agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: "Image attached: " + filepath.Base(path)}, {Type: "image", MIMEType: mimeType, Data: base64.StdEncoding.EncodeToString(data)}}}, nil
		}
		return textResult(string(data)), nil
	}
}

func writeTool(cwd string) agent.ToolExecutor {
	return func(_ context.Context, args map[string]any, _ func(agent.ToolResult)) (agent.ToolResult, error) {
		path, err := projectPath(cwd, stringArg(args, "path"))
		if err != nil {
			return agent.ToolResult{}, err
		}
		if isProtectedCredentialPath(path) {
			return agent.ToolResult{}, fmt.Errorf("access to Pi Go provider credentials is blocked")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return agent.ToolResult{}, err
		}
		if err := os.WriteFile(path, []byte(stringArg(args, "content")), 0o644); err != nil {
			return agent.ToolResult{}, err
		}
		return textResult("Wrote " + filepath.Base(path)), nil
	}
}

func replaceTool(cwd string) agent.ToolExecutor {
	return func(_ context.Context, args map[string]any, _ func(agent.ToolResult)) (agent.ToolResult, error) {
		path, err := existingProjectFile(cwd, stringArg(args, "path"))
		if err != nil {
			return agent.ToolResult{}, err
		}
		if isProtectedCredentialPath(path) {
			return agent.ToolResult{}, fmt.Errorf("access to Pi Go provider credentials is blocked")
		}
		info, err := os.Stat(path)
		if err != nil {
			return agent.ToolResult{}, err
		}
		if !info.Mode().IsRegular() {
			return agent.ToolResult{}, fmt.Errorf("replace target must be a regular file")
		}
		const maxReplaceBytes = 8 << 20
		if info.Size() > maxReplaceBytes {
			return agent.ToolResult{}, fmt.Errorf("replace target exceeds %d MiB limit", maxReplaceBytes>>20)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return agent.ToolResult{}, err
		}
		if !utf8.Valid(data) {
			return agent.ToolResult{}, fmt.Errorf("replace target is not valid UTF-8")
		}
		source := string(data)
		newText, ok := args["newText"].(string)
		if !ok {
			return agent.ToolResult{}, fmt.Errorf("newText is required")
		}
		if !utf8.ValidString(newText) {
			return agent.ToolResult{}, fmt.Errorf("newText must be valid UTF-8")
		}
		start, end, mode, err := replaceMatch(source, args)
		if err != nil {
			return agent.ToolResult{}, err
		}
		matched := source[start:end]
		replaced := source[:start] + newText + source[end:]
		if err := atomicReplaceFile(path, []byte(replaced), info.Mode().Perm()); err != nil {
			return agent.ToolResult{}, err
		}
		startLine := strings.Count(source[:start], "\n") + 1
		oldLines := strings.Count(matched, "\n") + 1
		newLines := strings.Count(newText, "\n") + 1
		details := map[string]any{"path": stringArg(args, "path"), "oldText": matched, "newText": newText, "startLine": startLine, "oldLineCount": oldLines, "newLineCount": newLines, "mode": mode}
		return agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: fmt.Sprintf("Replaced one %s match in %s at line %d", mode, filepath.Base(path), startLine)}}, Details: details}, nil
	}
}

func replaceMatch(source string, args map[string]any) (start, end int, mode string, err error) {
	oldText, hasText := args["oldText"].(string)
	oldRegex, hasRegex := args["oldRegex"].(string)
	// Some tool transports materialize every optional string field as "".
	hasText = hasText && oldText != ""
	hasRegex = hasRegex && oldRegex != ""
	if hasText == hasRegex {
		return 0, 0, "", fmt.Errorf("provide exactly one non-empty oldText or oldRegex")
	}
	if hasText {
		if strings.Count(source, oldText) != 1 {
			return 0, 0, "", fmt.Errorf("oldText must occur exactly once")
		}
		start = strings.Index(source, oldText)
		return start, start + len(oldText), "text", nil
	}
	pattern, compileErr := regexp.Compile(oldRegex)
	if compileErr != nil {
		return 0, 0, "", fmt.Errorf("compile oldRegex: %w", compileErr)
	}
	matches := pattern.FindAllStringIndex(source, 2)
	if len(matches) != 1 {
		return 0, 0, "", fmt.Errorf("oldRegex must match exactly once")
	}
	start, end = matches[0][0], matches[0][1]
	if start == end {
		return 0, 0, "", fmt.Errorf("oldRegex must match a non-empty span")
	}
	return start, end, "regex", nil
}

func existingProjectFile(cwd, value string) (string, error) {
	path, err := projectPath(cwd, value)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve replace target: %w", err)
	}
	relative, err := filepath.Rel(root, physical)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("replace target escapes project directory through a symlink")
	}
	return physical, nil
}

func atomicReplaceFile(path string, content []byte, mode os.FileMode) (err error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".forge-replace-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err = temporary.Write(content); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func bashTool(cwd string) agent.ToolExecutor {
	return func(ctx context.Context, args map[string]any, update func(agent.ToolResult)) (agent.ToolResult, error) {
		command := stringArg(args, "command")
		if command == "" {
			return agent.ToolResult{}, fmt.Errorf("command is required")
		}
		cmd := exec.Command("/bin/bash", "-lc", command)
		cmd.Dir = cwd
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return agent.ToolResult{}, err
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return agent.ToolResult{}, err
		}
		if err := cmd.Start(); err != nil {
			return agent.ToolResult{}, err
		}

		processDone := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				select {
				case <-processDone:
				case <-time.After(750 * time.Millisecond):
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
			case <-processDone:
			}
		}()

		chunks := make(chan []byte, 16)
		var readers sync.WaitGroup
		copyOutput := func(reader io.Reader) {
			defer readers.Done()
			buffer := make([]byte, 8*1024)
			for {
				count, readErr := reader.Read(buffer)
				if count > 0 {
					chunk := append([]byte(nil), buffer[:count]...)
					chunks <- chunk
				}
				if readErr != nil {
					return
				}
			}
		}
		readers.Add(2)
		go copyOutput(stdout)
		go copyOutput(stderr)
		go func() { readers.Wait(); close(chunks) }()

		log := &rollingToolLog{}
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		dirty, sent := false, false
		emitUpdate := func() { update(textResult(log.String())); dirty, sent = false, true }
		for {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					if dirty || !sent {
						emitUpdate()
					}
					waitErr := cmd.Wait()
					close(processDone)
					result := textResult(log.String())
					if waitErr != nil {
						result.IsError = true
						message := "Command failed: " + waitErr.Error()
						if ctx.Err() != nil {
							message = "Command aborted."
						}
						if result.Content[0].Text != "" {
							result.Content[0].Text += "\n"
						}
						result.Content[0].Text += message
					}
					return result, nil
				}
				log.Append(chunk)
				dirty = true
				if !sent {
					emitUpdate()
				}
			case <-ticker.C:
				if dirty {
					emitUpdate()
				}
			}
		}
	}
}

func projectPath(cwd, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("path is required")
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes project directory")
	}
	return path, nil
}

func isProtectedCredentialPath(path string) bool {
	configDir := strings.TrimSpace(os.Getenv("PI_GO_CONFIG_DIR"))
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		configDir = filepath.Join(home, ".pi-go")
	}
	credential, err := filepath.Abs(filepath.Join(configDir, "auth.json"))
	if err != nil {
		return false
	}
	candidate, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if candidate == credential {
		return true
	}
	// Prevent an in-workspace symlink from bypassing the protected-path check.
	resolvedCandidate, candidateErr := filepath.EvalSymlinks(candidate)
	resolvedCredential, credentialErr := filepath.EvalSymlinks(credential)
	return candidateErr == nil && credentialErr == nil && resolvedCandidate == resolvedCredential
}

func isSupportedImage(mimeType string) bool {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

func stringArg(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}

func textResult(text string) agent.ToolResult {
	return agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: text}}}
}

type rollingToolLog struct {
	data      []byte
	truncated bool
}

func (log *rollingToolLog) Append(value []byte) {
	const max = 64 * 1024
	log.data = append(log.data, value...)
	if len(log.data) > max {
		log.data = append([]byte(nil), log.data[len(log.data)-max:]...)
		log.truncated = true
	}
}
func (log *rollingToolLog) String() string {
	if log.truncated {
		return "... earlier output truncated ...\n" + string(log.data)
	}
	return string(log.data)
}
