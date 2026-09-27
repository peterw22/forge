package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/peterw22/forge/internal/agent"
)

type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpToolCall func(context.Context, string, map[string]any) (agent.ToolResult, error)

// serveAgyMCP is a stdio MCP endpoint. It cannot execute tools by itself: the
// authenticated callback must connect it to the active Pi Go turn and guard.
func serveAgyMCP(ctx context.Context, input io.Reader, output io.Writer, tools []agent.Tool, call mcpToolCall) error {
	if call == nil {
		return errors.New("MCP tool dispatcher is required")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLine)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req mcpMessage
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return fmt.Errorf("invalid MCP message: %w", err)
		}
		if req.JSONRPC != "2.0" {
			return errors.New("unsupported MCP protocol")
		}
		if len(req.ID) == 0 {
			continue
		} // notifications have no response
		var result any
		var failure any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "pi-go-agent", "version": "1.0"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			list := make([]map[string]any, 0, len(tools))
			for _, tool := range tools {
				list = append(list, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.Parameters})
			}
			result = map[string]any{"tools": list}
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
				failure = map[string]any{"code": -32602, "message": "invalid tool arguments"}
				break
			}
			valid := false
			for _, tool := range tools {
				if tool.Name == params.Name {
					valid = true
					break
				}
			}
			if !valid {
				failure = map[string]any{"code": -32602, "message": "unknown tool"}
				break
			}
			value, err := call(ctx, params.Name, params.Arguments)
			if err != nil {
				value = agent.ToolResult{Content: []agent.ContentBlock{{Type: "text", Text: err.Error()}}, IsError: true}
			}
			content := make([]map[string]any, 0, len(value.Content))
			for _, block := range value.Content {
				if block.Type == "text" {
					content = append(content, map[string]any{"type": "text", "text": block.Text})
				} else if block.Type == "image" {
					content = append(content, map[string]any{"type": "image", "data": block.Data, "mimeType": block.MIMEType})
				}
			}
			result = map[string]any{"content": content, "isError": value.IsError}
		default:
			failure = map[string]any{"code": -32601, "message": "method not found"}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if failure != nil {
			response["error"] = failure
		} else {
			response["result"] = result
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// The stdio process deliberately has no fallback to direct tool execution.
func runAgyMCPChild() error {
	address := os.Getenv("PI_GO_AGY_MCP_ADDRESS")
	token := os.Getenv("PI_GO_AGY_MCP_TOKEN")
	if address == "" || token == "" {
		return errors.New("MCP turn credentials missing")
	}
	tools := builtInTools(".")
	return serveAgyMCP(context.Background(), os.Stdin, os.Stdout, tools, func(ctx context.Context, name string, args map[string]any) (agent.ToolResult, error) {
		payload, err := json.Marshal(map[string]any{"name": name, "arguments": args})
		if err != nil {
			return agent.ToolResult{}, err
		}
		request, err := httpNewAuthenticatedRequest(ctx, address, token, strings.NewReader(string(payload)))
		if err != nil {
			return agent.ToolResult{}, err
		}
		response, err := agyMCPClient.Do(request)
		if err != nil {
			return agent.ToolResult{}, err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return agent.ToolResult{}, fmt.Errorf("MCP bridge returned HTTP %d", response.StatusCode)
		}
		var result agent.ToolResult
		if err := json.NewDecoder(io.LimitReader(response.Body, 12<<20)).Decode(&result); err != nil {
			return result, err
		}
		return result, nil
	})
}
