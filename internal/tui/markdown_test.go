package tui

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	result := strings.Join(renderMarkdown("# Heading\n\n- **bold** and `code`\n> quote\n```go\nfmt.Println(\"ok\")\n```", 60), "\n")
	for _, expected := range []string{"Heading", "• ", "bold", "code", "│ quote", "fmt.Println", "\x1b[1m"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("markdown output missing %q:\n%s", expected, result)
		}
	}
	for _, marker := range []string{"# Heading", "**bold**", "```"} {
		if strings.Contains(result, marker) {
			t.Fatalf("markdown marker %q was not rendered", marker)
		}
	}
}
