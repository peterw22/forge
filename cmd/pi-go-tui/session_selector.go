package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/peterw22/forge/internal/session"
)

const resumeSelector = "__select__"

type sessionPicker struct {
	entries  []session.Entry
	selected int
	width    int
	height   int
	chosen   bool
	cancel   bool
}

func selectSession(cwd string) (string, bool, error) {
	entries, err := session.List(cwd)
	if err != nil {
		return "", false, err
	}
	if len(entries) == 0 {
		return "", false, fmt.Errorf("no sessions found under %s", filepathForSessions(cwd))
	}
	program := tea.NewProgram(sessionPicker{entries: entries, width: 80, height: 24}, tea.WithAltScreen())
	result, err := program.Run()
	if err != nil {
		return "", false, err
	}
	picker, ok := result.(sessionPicker)
	if !ok || picker.cancel || !picker.chosen {
		return "", false, nil
	}
	return picker.entries[picker.selected].ID, true, nil
}

func filepathForSessions(cwd string) string { return cwd + "/.pi-go/sessions" }

func (m sessionPicker) Init() tea.Cmd { return nil }
func (m sessionPicker) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = value.Width, value.Height
	case tea.KeyMsg:
		switch value.String() {
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected+1 < len(m.entries) {
				m.selected++
			}
		case "home":
			m.selected = 0
		case "end":
			m.selected = len(m.entries) - 1
		case "enter":
			m.chosen = true
			return m, tea.Quit
		case "esc", "ctrl+c", "q":
			m.cancel = true
			return m, tea.Quit
		}
	}
	return m, nil
}
func (m sessionPicker) View() string {
	if m.cancel || m.chosen {
		return ""
	}
	width := m.width
	if width < 48 {
		width = 48
	}
	rows := (m.height - 7) / 2
	if rows < 1 {
		rows = 1
	}
	start := m.selected - rows/2
	if start < 0 {
		start = 0
	}
	if start+rows > len(m.entries) {
		start = len(m.entries) - rows
		if start < 0 {
			start = 0
		}
	}
	end := start + rows
	if end > len(m.entries) {
		end = len(m.entries)
	}
	lines := []string{cyan + " Resume session " + reset, strings.Repeat("─", width), dim + "↑/↓ select  Enter resume  Esc cancel" + reset, ""}
	for index := start; index < end; index++ {
		entry := m.entries[index]
		name := entry.LastMessageTime.Local().Format("2006-01-02 15:04")
		marker, color := "  ", ""
		if index == m.selected {
			marker, color = "> ", green
		}
		when := entry.LastMessageTime.Local().Format("2006-01-02 15:04")
		space := width - 2 - utf8.RuneCountInString(when) - 1
		label := truncateOneLine(name, space)
		padding := space - utf8.RuneCountInString(label)
		if padding < 1 {
			padding = 1
		}
		lines = append(lines, color+marker+label+strings.Repeat(" ", padding)+when+reset)
		preview := entry.Summary
		if preview == "" {
			preview = "No completed turn summary yet."
		}
		lines = append(lines, dim+"    “"+truncateOneLine(preview, width-7)+"”"+reset)
	}
	lines = append(lines, "", dim+fmt.Sprintf("%d sessions · newest first", len(m.entries))+reset)
	return strings.Join(lines, "\n")
}

func truncateOneLine(value string, width int) string {
	value = strings.Join(strings.Fields(strings.NewReplacer("\x1b", "", "\r", "", "\n", " ").Replace(value)), " ")
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

// normalizeResumeArgs gives bare -r an internal selector value while retaining
// standard flag parsing for -r <uuid>, -r latest, and -r=<uuid>.
func normalizeResumeArgs(args []string) []string {
	out := make([]string, 0, len(args)+1)
	for index, arg := range args {
		if arg == "-r" && (index+1 == len(args) || strings.HasPrefix(args[index+1], "-")) {
			out = append(out, "-r="+resumeSelector)
			continue
		}
		out = append(out, arg)
	}
	return out
}
