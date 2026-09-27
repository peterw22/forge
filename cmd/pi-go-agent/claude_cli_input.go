package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/peterw22/forge/internal/agent"
)

// Claude's streaming input accepts Anthropic content blocks, not Pi Go's
// flattened image blocks. Keep the payload off argv (images can be large).
func claudeCLIUserInput(blocks []agent.ContentBlock) (string, error) {
	content := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": block.Text})
			}
		case "image":
			switch block.MIMEType {
			case "image/png", "image/jpeg", "image/gif", "image/webp":
			default:
				return "", fmt.Errorf("Claude does not support image MIME type %q", block.MIMEType)
			}
			if block.Data == "" {
				return "", errors.New("Claude image data is empty")
			}
			if _, err := base64.StdEncoding.DecodeString(block.Data); err != nil {
				return "", errors.New("Claude image data must be valid base64")
			}
			content = append(content, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": block.MIMEType, "data": block.Data}})
		default:
			return "", fmt.Errorf("Claude does not support prompt content type %q", block.Type)
		}
	}
	if len(content) == 0 {
		return "", errors.New("Claude prompt is empty")
	}
	data, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}
