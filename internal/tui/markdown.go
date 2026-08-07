package tui

import (
	"regexp"
	"strings"
)

var (
	markdownLink   = regexp.MustCompile(`\[([^]]+)\]\(([^)]+)\)`)
	markdownBold   = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	markdownStrike = regexp.MustCompile(`~~([^~]+)~~`)
	markdownCode   = regexp.MustCompile("`([^`]+)`")
	markdownItalic = regexp.MustCompile(`(^|[^*])\*([^*]+)\*`)
)

// renderMarkdown provides a dependency-free terminal rendering of common
// Markdown. It deliberately handles presentation only; terminal escape bytes
// have already been removed by clean before this function is called.
func renderMarkdown(value string, width int) []string {
	if width < 1 {
		width = 1
	}
	var output []string
	inCode := false
	for _, source := range strings.Split(value, "\n") {
		trimmed := strings.TrimSpace(source)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if !inCode && len(trimmed) > 3 {
				output = append(output, dim+"  "+strings.TrimSpace(trimmed[3:])+reset)
			}
			inCode = !inCode
			continue
		}
		if inCode {
			for _, line := range wrap(source, width-2) {
				output = append(output, yellow+"│ "+line+reset)
			}
			continue
		}
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			output = append(output, dim+strings.Repeat("─", width)+reset)
			continue
		}

		style, prefix, text := "", "", source
		left := strings.TrimLeft(source, " ")
		switch {
		case strings.HasPrefix(left, "#"):
			level := 0
			for level < len(left) && left[level] == '#' {
				level++
			}
			if level < len(left) && left[level] == ' ' {
				text = strings.TrimSpace(left[level:])
				style = cyan + "\x1b[1m"
			}
		case strings.HasPrefix(left, "> "):
			text, prefix, style = strings.TrimPrefix(left, "> "), "│ ", dim
		case strings.HasPrefix(left, "- ") || strings.HasPrefix(left, "* ") || strings.HasPrefix(left, "+ "):
			text, prefix = left[2:], "• "
		}
		text = strings.ReplaceAll(text, "[x] ", "☑ ")
		text = strings.ReplaceAll(text, "[X] ", "☑ ")
		text = strings.ReplaceAll(text, "[ ] ", "☐ ")
		lines := wrap(text, width-len([]rune(prefix)))
		for index, line := range lines {
			linePrefix := prefix
			if index > 0 && prefix != "" {
				linePrefix = strings.Repeat(" ", len([]rune(prefix)))
			}
			output = append(output, style+linePrefix+renderInlineMarkdown(line)+reset)
		}
	}
	if len(output) == 0 {
		return []string{""}
	}
	return output
}

func renderInlineMarkdown(value string) string {
	value = markdownLink.ReplaceAllString(value, "\x1b[4m$1\x1b[24m "+dim+"($2)"+reset)
	value = markdownBold.ReplaceAllStringFunc(value, func(match string) string {
		text := strings.Trim(match, "*_")
		return "\x1b[1m" + text + "\x1b[22m"
	})
	value = markdownStrike.ReplaceAllString(value, "\x1b[9m$1\x1b[29m")
	value = markdownCode.ReplaceAllString(value, yellow+"$1"+reset)
	value = markdownItalic.ReplaceAllString(value, "$1\x1b[3m$2\x1b[23m")
	return value
}
