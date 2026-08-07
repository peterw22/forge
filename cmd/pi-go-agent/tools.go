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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/peterw22/pi-go/internal/agent"
)

func builtInTools(cwd string) []agent.Tool {
	return []agent.Tool{
		{Name: "read", Description: "Read a text file or attach a supported image (PNG, JPEG, GIF, WebP).", Parameters: objectSchema("path"), Execute: readTool(cwd)},
		{Name: "write", Description: "Write a UTF-8 text file.", Parameters: objectSchema("path", "content"), Execute: writeTool(cwd)},
		{Name: "bash", Description: "Run a shell command in the project directory. Always provide a concise plain-language description of what the command does and why it is being run; this description is shown to the user before and during execution.", Parameters: bashSchema(), Execute: bashTool(cwd)},
	}
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

func objectSchema(required ...string) map[string]any {
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
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return agent.ToolResult{}, err
		}
		if err := os.WriteFile(path, []byte(stringArg(args, "content")), 0o644); err != nil {
			return agent.ToolResult{}, err
		}
		return textResult("Wrote " + filepath.Base(path)), nil
	}
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
