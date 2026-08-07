package tui

import "strings"

func renderApprovalDialog(screen Screen, request ApprovalRequest) string {
	width, height := screen.Width, screen.Height
	if width < 48 {
		width = 48
	}
	if height < 12 {
		height = 12
	}
	lines := []string{
		yellow + " Bash Safety approval required " + reset,
		strings.Repeat("─", width),
		red + "This operation was blocked and will not run without your approval." + reset,
		"",
		cyan + clean(request.Tool) + reset,
	}
	for _, line := range wrap(clean(request.Description), width-2) {
		lines = append(lines, "  "+line)
	}
	lines = append(lines, "", yellow+"Reason"+reset)
	for _, line := range wrap(clean(request.Reason), width-2) {
		lines = append(lines, "  "+line)
	}
	for len(lines) < height-3 {
		lines = append(lines, "")
	}
	lines = append(lines, strings.Repeat("─", width), green+"y  approve once"+reset+"    "+red+"n/Esc  reject"+reset)
	return "\x1b[H\x1b[2J" + strings.Join(lines, "\n")
}
