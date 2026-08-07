// Package tui provides the dependency-minimal rendering primitives used by the
// Go terminal frontend. Rendering is deterministic ANSI text: terminal input
// is handled separately by golang.org/x/term.
package tui

import (
	"fmt"
	"strings"
)

const (
	reset  = "\x1b[0m"
	dim    = "\x1b[2m"
	cyan   = "\x1b[36m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	red    = "\x1b[31m"
)

type BlockKind string

const (
	User       BlockKind = "user"
	Assistant  BlockKind = "assistant"
	Thinking   BlockKind = "thinking"
	Tool       BlockKind = "tool"
	Compaction BlockKind = "compaction"
	Error      BlockKind = "error"
)

type Block struct {
	ID            string
	Kind          BlockKind
	Title, Text   string
	Input         string
	InputLanguage string
	Collapsed     bool
	IsError       bool
	SafetyStatus  string
	SafetyMessage string
}
type Suggestion struct{ Value, Description string }
type Screen struct {
	Width, Height                                                                         int
	Model, Thinking, Status, Editor                                                       string
	Blocks                                                                                []Block
	Suggestions                                                                           []Suggestion
	Scroll                                                                                int
	SessionTokens, InputTokens, CacheReadTokens, OutputTokens, ContextWindow, ContextUsed int
}

// Render produces a complete alternate-screen frame. Untrusted text has ANSI
// control bytes stripped before display so model/tool output cannot alter the UI.
func (s Screen) Render() string {
	width := s.Width
	if width < 40 {
		width = 40
	}
	height := s.Height
	if height < 10 {
		height = 10
	}
	lines, _ := s.transcript(width)
	editorLines := wrap(clean(s.Editor), width-2)
	if len(editorLines) == 0 {
		editorLines = []string{""}
	}
	contentHeight := height - 3 - len(editorLines) - len(s.Suggestions)
	if contentHeight < 1 {
		contentHeight = 1
	}
	if len(lines) > contentHeight {
		maxScroll := len(lines) - contentHeight
		scroll := s.Scroll
		if scroll < 0 {
			scroll = 0
		}
		if scroll > maxScroll {
			scroll = maxScroll
		}
		start := maxScroll - scroll
		lines = lines[start : start+contentHeight]
	}
	for len(lines) < contentHeight {
		lines = append(lines, "")
	}
	lines = append(lines, strings.Repeat("─", width))
	lines = append(lines, dim+clean(s.Status)+reset)
	percent := 0
	if s.ContextWindow > 0 {
		percent = s.ContextUsed * 100 / s.ContextWindow
	}
	lines = append(lines, dim+fmt.Sprintf("session %d  in %d  cache %d  out %d  context %d/%d (%d%%)", s.SessionTokens, s.InputTokens, s.CacheReadTokens, s.OutputTokens, s.ContextUsed, s.ContextWindow, percent)+reset)
	for index, line := range editorLines {
		prefix := "  "
		if index == 0 {
			prefix = green + "> " + reset
		}
		cursor := ""
		if index == len(editorLines)-1 {
			cursor = "▏"
		}
		lines = append(lines, prefix+line+cursor)
	}
	for _, suggestion := range s.Suggestions {
		lines = append(lines, dim+"  "+clean(suggestion.Value)+"  "+clean(suggestion.Description)+reset)
	}
	return "\x1b[H\x1b[2J" + strings.Join(lines, "\n")
}
func (s Screen) transcript(width int) ([]string, []int) {
	lines := []string{cyan + " Pi Go " + reset + dim + "  " + clean(s.Model) + " · thinking: " + clean(s.Thinking) + reset, strings.Repeat("─", width)}
	owners := []int{-1, -1}
	for index, block := range s.Blocks {
		title, color := block.Title, cyan
		switch block.Kind {
		case User:
			title, color = "You", green
		case Thinking:
			title, color = "Reasoning summary", dim
		case Tool:
			title, color = "Tool: "+title, yellow
		case Compaction:
			title, color = "Compaction summary", cyan
		}
		if block.IsError {
			color = red
		}
		for _, titleLine := range wrap(clean(title), width) {
			lines = append(lines, color+titleLine+reset)
			owners = append(owners, index)
		}
		if block.SafetyStatus != "" {
			color := dim
			marker := "○"
			if block.SafetyStatus == "approved" {
				color, marker = green, "✓"
			}
			if block.SafetyStatus == "rejected" {
				color, marker = red, "✗"
			}
			for _, line := range wrap(marker+" Bash Safety · "+clean(block.SafetyMessage), width-2) {
				lines = append(lines, "  "+color+line+reset)
				owners = append(owners, index)
			}
		}
		if block.Collapsed {
			label := "details folded · click to expand"
			if block.Kind == Tool {
				label = "input/output folded · click to expand"
			}
			lines = append(lines, dim+"  [collapsed] · "+label+reset)
			owners = append(owners, index)
			continue
		}
		if block.Kind == Tool {
			for _, line := range renderToolDetails(block, width-2) {
				lines = append(lines, "  "+line)
				owners = append(owners, index)
			}
			continue
		}
		body := wrap(clean(block.Text), width-2)
		if block.Kind == Assistant || block.Kind == Compaction {
			body = renderMarkdown(clean(block.Text), width-2)
		}
		for _, line := range body {
			lines = append(lines, "  "+line)
			owners = append(owners, index)
		}
	}
	return lines, owners
}

func renderToolDetails(block Block, width int) []string {
	if width < 8 {
		width = 8
	}
	language := block.InputLanguage
	input := clean(block.Input)
	output := clean(block.Text)
	if input == "" {
		input = "(no input)"
	}
	if output == "" {
		output = "(waiting for output…)"
	}
	lines := []string{dim + "Input" + reset, dim + "```" + language + reset}
	for _, line := range wrap(input, width) {
		lines = append(lines, yellow+line+reset)
	}
	lines = append(lines, dim+"```"+reset, dim+"Output"+reset, dim+"```text"+reset)
	for _, line := range wrap(output, width) {
		lines = append(lines, line)
	}
	return append(lines, dim+"```"+reset)
}

// BlockAt maps a visible transcript row to its backing block for mouse clicks.
func (s Screen) BlockAt(y int) int {
	width, height := s.Width, s.Height
	if width < 40 {
		width = 40
	}
	if height < 10 {
		height = 10
	}
	lines, owners := s.transcript(width)
	editorLines := wrap(clean(s.Editor), width-2)
	contentHeight := height - 3 - len(editorLines) - len(s.Suggestions)
	if contentHeight < 1 || y < 0 || y >= contentHeight {
		return -1
	}
	start := 0
	if len(lines) > contentHeight {
		maxScroll := len(lines) - contentHeight
		scroll := s.Scroll
		if scroll < 0 {
			scroll = 0
		}
		if scroll > maxScroll {
			scroll = maxScroll
		}
		start = maxScroll - scroll
	}
	position := start + y
	if position < 0 || position >= len(owners) {
		return -1
	}
	return owners[position]
}

func clean(value string) string {
	return strings.NewReplacer("\x1b", "", "\r", "", "\x00", "").Replace(value)
}
func wrap(value string, width int) []string {
	if value == "" {
		return []string{""}
	}
	var out []string
	for _, line := range strings.Split(value, "\n") {
		for len(line) > width {
			cut := strings.LastIndex(line[:width+1], " ")
			if cut < 1 {
				cut = width
			}
			out = append(out, line[:cut])
			line = strings.TrimLeft(line[cut:], " ")
		}
		out = append(out, line)
	}
	return out
}
func EnterAlternateScreen() string { return "\x1b[?1049h\x1b[?25l" }
func ExitAlternateScreen() string  { return fmt.Sprintf("%s\x1b[?1049l", reset) }
