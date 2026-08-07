package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func renderSessionDialog(screen Screen, sessions []SessionChoice, selected int) string {
	width, height := screen.Width, screen.Height
	if width < 48 {
		width = 48
	}
	if height < 12 {
		height = 12
	}
	rows := (height - 7) / 2
	if rows < 1 {
		rows = 1
	}
	start := selected - rows/2
	if start < 0 {
		start = 0
	}
	if start+rows > len(sessions) {
		start = len(sessions) - rows
		if start < 0 {
			start = 0
		}
	}
	end := start + rows
	if end > len(sessions) {
		end = len(sessions)
	}
	lines := []string{cyan + " Switch session " + reset, strings.Repeat("─", width), dim + "↑/↓ select  Enter switch  Esc cancel  ·  newest UUIDv7 first" + reset, ""}
	for index := start; index < end; index++ {
		session := sessions[index]
		name := session.Name
		if strings.TrimSpace(name) == "" {
			name = session.ID
		}
		marker, color := "  ", ""
		if index == selected {
			marker, color = "> ", green
		}
		when := ""
		if !session.LastMessageTime.IsZero() {
			when = session.LastMessageTime.Local().Format("2006-01-02 15:04")
		}
		available := width - utf8.RuneCountInString(when) - 3
		label := truncateSessionText(name, available)
		padding := available - utf8.RuneCountInString(label)
		if padding < 1 {
			padding = 1
		}
		lines = append(lines, color+marker+label+strings.Repeat(" ", padding)+when+reset)
		preview := session.Preview
		if preview == "" {
			preview = "(no messages)"
		}
		lines = append(lines, dim+"    “"+truncateSessionText(preview, width-7)+"”"+reset)
	}
	for len(lines) < height-2 {
		lines = append(lines, "")
	}
	lines = append(lines, dim+fmt.Sprintf("%d sessions", len(sessions))+reset)
	return "\x1b[H\x1b[2J" + strings.Join(lines, "\n")
}

func truncateSessionText(value string, width int) string {
	value = strings.Join(strings.Fields(clean(value)), " ")
	if width < 1 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
