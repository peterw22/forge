package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Model is the rich TUI state machine. The frontend sends Append and Editor
// messages from its backend/input goroutines; Bubble Tea serializes all redraws.
type Model struct {
	Screen          Screen
	OnSubmit        func(string)
	OnCommand       func(string)
	OnAbort         func()
	OnApproval      func(string, bool)
	Complete        func(string) []string
	Help            func(string) []Suggestion
	OnOpenSessions  func()
	OnSelectSession func(string)
	sessions        []SessionChoice
	sessionSelected int
	approval        *ApprovalRequest
	controlB        bool
	quit            bool
}
type Append struct{ Block Block }
type SetEditor struct{ Value string }
type SetStatus struct{ Value string }
type ReplaceLast struct {
	Kind BlockKind
	Text string
}
type ReplaceBlock struct {
	ID, Text string
	Error    bool
}
type ReplaceSafety struct{ ID, Status, Message string }
type SetModel struct{ Value string }
type SetThinking struct{ Value string }
type AddUsage struct{ Input, CacheRead, Output, Total int }
type SessionChoice struct {
	ID, Name, Preview string
	LastMessageTime   time.Time
}
type ShowSessions struct{ Sessions []SessionChoice }
type ApprovalRequest struct{ ID, ToolCallID, Tool, Description, Reason string }
type ShowApproval ApprovalRequest
type ClearApproval struct{ ToolCallID string }
type Restore struct {
	Blocks                                   []Block
	Model, Thinking                          string
	Input, CacheRead, Output, Total, Context int
	Streaming                                bool
}

func New(model, thinking string) Model {
	return Model{Screen: Screen{Width: 80, Height: 24, Model: model, Thinking: thinking, Status: "idle", ContextWindow: contextWindow(model)}}
}
func contextWindow(model string) int {
	if model == "gpt-5.3-codex-spark" {
		return 128000
	}
	return 272000
}
func (m Model) Init() tea.Cmd { return nil }
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch value := msg.(type) {
	case tea.WindowSizeMsg:
		m.Screen.Width, m.Screen.Height = value.Width, value.Height
	case tea.KeyMsg:
		if m.approval != nil {
			switch value.String() {
			case "y":
				id := m.approval.ID
				m.approval = nil
				m.Screen.Status = "operation manually approved"
				if m.OnApproval != nil {
					m.OnApproval(id, true)
				}
			case "n", "esc", "ctrl+c":
				id := m.approval.ID
				m.approval = nil
				m.Screen.Status = "operation rejected"
				if m.OnApproval != nil {
					m.OnApproval(id, false)
				}
				if value.String() == "ctrl+c" && m.OnAbort != nil {
					m.OnAbort()
				}
			}
			return m, nil
		}
		if len(m.sessions) > 0 {
			switch value.String() {
			case "up", "k":
				if m.sessionSelected > 0 {
					m.sessionSelected--
				}
			case "down", "j":
				if m.sessionSelected+1 < len(m.sessions) {
					m.sessionSelected++
				}
			case "enter":
				id := m.sessions[m.sessionSelected].ID
				m.sessions = nil
				if m.OnSelectSession != nil {
					m.OnSelectSession(id)
				}
			case "esc", "q":
				m.sessions = nil
			}
			return m, nil
		}
		if m.controlB {
			m.controlB = false
			if value.String() == "s" && m.OnOpenSessions != nil {
				m.OnOpenSessions()
				m.Screen.Status = "loading sessions…"
				return m, nil
			}
		}
		switch value.String() {
		case "ctrl+b":
			m.controlB = true
			m.Screen.Status = "Ctrl-B: press s for sessions"
		case "ctrl+o":
			for index := len(m.Screen.Blocks) - 1; index >= 0; index-- {
				if m.Screen.Blocks[index].Kind == Tool || m.Screen.Blocks[index].Kind == Compaction {
					m.Screen.Blocks[index].Collapsed = !m.Screen.Blocks[index].Collapsed
					break
				}
			}
		case "ctrl+c":
			if m.Screen.Status == "streaming" && m.OnAbort != nil {
				m.OnAbort()
				return m, nil
			}
			m.quit = true
			return m, tea.Quit
		case "q":
			if m.Screen.Editor == "" {
				m.quit = true
				return m, tea.Quit
			}
		case "enter":
			if text := m.Screen.Editor; text != "" {
				m.Screen.Editor = ""
				if text[0] == '/' && m.OnCommand != nil {
					m.OnCommand(text)
				} else if m.OnSubmit != nil {
					m.OnSubmit(text)
				}
			}
		case "tab":
			if m.Complete != nil {
				matches := m.Complete(m.Screen.Editor)
				if len(matches) == 1 {
					m.Screen.Editor = matches[0]
				} else if len(matches) > 1 {
					m.Screen.Status = "matches: " + strings.Join(matches, "  ")
				}
			}
		case "space", " ":
			m.Screen.Editor += " "
			m.refreshSuggestions()
		case "up", "pgup":
			m.Screen.Scroll += 4
		case "down", "pgdown":
			m.Screen.Scroll -= 4
			if m.Screen.Scroll < 0 {
				m.Screen.Scroll = 0
			}
		case "backspace":
			if len(m.Screen.Editor) > 0 {
				m.Screen.Editor = m.Screen.Editor[:len(m.Screen.Editor)-1]
				m.refreshSuggestions()
			}
		default:
			if value.Type == tea.KeyRunes {
				m.Screen.Editor += string(value.Runes)
				m.refreshSuggestions()
			}
		}
	case ClearApproval:
		if m.approval != nil && m.approval.ToolCallID == value.ToolCallID {
			m.approval = nil
		}
	case ShowApproval:
		request := ApprovalRequest(value)
		m.approval = &request
		m.Screen.Status = "waiting for safety approval"
	case ShowSessions:
		m.sessions = append([]SessionChoice(nil), value.Sessions...)
		m.sessionSelected = 0
		if len(m.sessions) == 0 {
			m.Screen.Status = "no sessions found"
		} else {
			m.Screen.Status = "select a session"
		}
	case Restore:
		m.Screen.Blocks = append([]Block(nil), value.Blocks...)
		m.Screen.Model = value.Model
		m.Screen.Thinking = value.Thinking
		m.Screen.InputTokens = value.Input
		m.Screen.CacheReadTokens = value.CacheRead
		m.Screen.OutputTokens = value.Output
		m.Screen.SessionTokens = value.Total
		m.Screen.ContextUsed = value.Context
		if m.Screen.ContextUsed == 0 {
			m.Screen.ContextUsed = value.Input
		}
		m.Screen.ContextWindow = contextWindow(value.Model)
		m.Screen.Scroll = 0
		if value.Streaming {
			m.Screen.Status = "streaming"
		} else {
			m.Screen.Status = "idle"
		}
	case tea.MouseMsg:
		event := tea.MouseEvent(value)
		if event.Action == tea.MouseActionPress && event.Button == tea.MouseButtonLeft {
			if index := m.Screen.BlockAt(event.Y); index >= 0 && index < len(m.Screen.Blocks) && (m.Screen.Blocks[index].Kind == Tool || m.Screen.Blocks[index].Kind == Compaction) {
				m.Screen.Blocks[index].Collapsed = !m.Screen.Blocks[index].Collapsed
			}
		}
	case Append:
		m.Screen.Blocks = append(m.Screen.Blocks, value.Block)
	case SetEditor:
		m.Screen.Editor = value.Value
	case SetStatus:
		m.Screen.Status = value.Value
	case SetModel:
		m.Screen.Model = value.Value
		m.Screen.ContextWindow = contextWindow(value.Value)
	case SetThinking:
		m.Screen.Thinking = value.Value
	case AddUsage:
		m.Screen.InputTokens += value.Input
		m.Screen.CacheReadTokens += value.CacheRead
		m.Screen.OutputTokens += value.Output
		m.Screen.SessionTokens += value.Total
		m.Screen.ContextUsed = value.Total
		if m.Screen.ContextUsed == 0 {
			m.Screen.ContextUsed = value.Input
		}
	case ReplaceSafety:
		for index := len(m.Screen.Blocks) - 1; index >= 0; index-- {
			if m.Screen.Blocks[index].ID == value.ID {
				m.Screen.Blocks[index].SafetyStatus = value.Status
				m.Screen.Blocks[index].SafetyMessage = value.Message
				break
			}
		}
	case ReplaceBlock:
		for index := len(m.Screen.Blocks) - 1; index >= 0; index-- {
			if m.Screen.Blocks[index].ID == value.ID {
				m.Screen.Blocks[index].Text = value.Text
				if value.Error {
					m.Screen.Blocks[index].IsError = true
				}
				break
			}
		}
	case ReplaceLast:
		for index := len(m.Screen.Blocks) - 1; index >= 0; index-- {
			if m.Screen.Blocks[index].Kind == value.Kind {
				m.Screen.Blocks[index].Text = value.Text
				break
			}
		}
	}
	return m, nil
}
func (m *Model) refreshSuggestions() {
	if m.Help != nil {
		m.Screen.Suggestions = m.Help(m.Screen.Editor)
	}
}
func (m Model) View() string {
	if m.quit {
		return ""
	}
	if m.approval != nil {
		return renderApprovalDialog(m.Screen, *m.approval)
	}
	if len(m.sessions) > 0 {
		return renderSessionDialog(m.Screen, m.sessions, m.sessionSelected)
	}
	return m.Screen.Render()
}
