package cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/Wangch29/ikun-messenger/im"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

var (
	tuiServerAddr string
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Start the ikun messenger TUI client",
	Run:   runTui,
}

func init() {
	rootCmd.AddCommand(tuiCmd)
	tuiCmd.Flags().StringVarP(&tuiServerAddr, "server", "s", "localhost:8080", "Server address")
}

func runTui(cmd *cobra.Command, args []string) {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Fatal(err)
	}
}

// --- Styles ---
var (
	activeBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("62")).
				Padding(0, 1)

	inactiveBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("240")).
				Padding(0, 1)

	focusedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	noStyle      = lipgloss.NewStyle()

	senderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5")) // Purple
	selfStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // Green
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("237")).Padding(0, 1)
)

// --- State Management ---
type state int

const (
	stateLogin state = iota
	stateChat
)

// --- Messages ---
type errMsg error
type wsMsg im.ServerMessage
type connectedMsg struct {
	conn *websocket.Conn
}

type chatMessage struct {
	sender  string
	content string
	time    string
	isSelf  bool
}

// --- Model ---
type model struct {
	state  state
	width  int
	height int

	// --- Login ---
	loginInput textinput.Model

	// --- Chat Data ---
	userName      string
	conn          *websocket.Conn
	activeSession string                   // Current session name
	sessions      []string                 // List of open sessions
	chatHistory   map[string][]chatMessage // Map session -> messages

	// --- Chat UI ---
	viewport      viewport.Model
	chatInput     textinput.Model
	sidebarCursor int
	focusSideBar  bool

	// --- New Chat Popup ---
	showNewChat  bool
	newChatInput textinput.Model
}

func initialModel() model {
	li := textinput.New()
	li.Placeholder = "Enter username..."
	li.Focus()
	li.CharLimit = 20
	li.Width = 20

	ci := textinput.New()
	ci.Placeholder = "Type a message..."
	ci.CharLimit = 200
	ci.Width = 50

	nci := textinput.New()
	nci.Placeholder = "Chat with user..."
	nci.CharLimit = 20
	nci.Width = 20

	vp := viewport.New(0, 0)

	return model{
		state:         stateLogin,
		loginInput:    li,
		chatInput:     ci,
		newChatInput:  nci,
		viewport:      vp,
		activeSession: "Global",
		sessions:      []string{"Global"},
		chatHistory:   make(map[string][]chatMessage),
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

// --- Update ---

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Recalculate layout
		m.viewport.Width = m.width - 28  // Sidebar width 20 + padding/borders
		m.viewport.Height = m.height - 7 // Reduce height to fit help bar
		return m, nil
	}

	switch m.state {
	case stateLogin:
		return m.updateLogin(msg)
	case stateChat:
		return m.updateChat(msg)
	}
	return m, nil
}

func (m model) updateLogin(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter {
			username := strings.TrimSpace(m.loginInput.Value())
			if username != "" {
				m.userName = username
				return m, m.connectWebSocket()
			}
		}
	case connectedMsg:
		m.state = stateChat
		m.conn = msg.conn
		m.chatInput.Focus()
		return m, m.waitForMessage()
	case errMsg:
		// TODO: Show error
		return m, tea.Quit
	}
	m.loginInput, cmd = m.loginInput.Update(msg)
	return m, cmd
}

func (m model) updateChat(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	// Handle popup input first
	if m.showNewChat {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			if msg.Type == tea.KeyEsc {
				m.showNewChat = false
				m.newChatInput.Reset()
				return m, nil
			}
			if msg.Type == tea.KeyEnter {
				target := strings.TrimSpace(m.newChatInput.Value())
				if target != "" {
					// Add new session
					if !contains(m.sessions, target) {
						m.sessions = append(m.sessions, target)
					}
					m.activeSession = target
					m.showNewChat = false
					m.newChatInput.Reset()
					m.renderChat()
				}
				return m, nil
			}
		}
		m.newChatInput, cmd = m.newChatInput.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Global Shortcuts
		if msg.String() == "ctrl+n" {
			m.showNewChat = true
			m.newChatInput.Focus()
			return m, nil
		}
		if msg.Type == tea.KeyTab {
			m.focusSideBar = !m.focusSideBar
			if m.focusSideBar {
				m.chatInput.Blur()
			} else {
				m.chatInput.Focus()
			}
			return m, nil
		}

		if m.focusSideBar {
			// Navigate Sidebar
			switch msg.Type {
			case tea.KeyUp:
				if m.sidebarCursor > 0 {
					m.sidebarCursor--
				}
			case tea.KeyDown:
				if m.sidebarCursor < len(m.sessions)-1 {
					m.sidebarCursor++
				}
			case tea.KeyEnter:
				m.activeSession = m.sessions[m.sidebarCursor]
				m.focusSideBar = false
				m.chatInput.Focus()
				m.renderChat()
			}
		} else {
			// Chat Input
			switch msg.Type {
			case tea.KeyEnter:
				text := strings.TrimSpace(m.chatInput.Value())
				if text != "" {
					m.sendMessage(text)
					m.chatInput.Reset()
				}
			}
		}

	case wsMsg: // Received Message
		session := "Global"
		if msg.Type == "msg" {
			// Private message
			session = msg.From
			if msg.From == m.userName {
				// TODO: Handle self sent message.
			}
		}

		// Add to history
		if !contains(m.sessions, session) {
			m.sessions = append(m.sessions, session)
		}

		chatMsg := chatMessage{
			sender:  msg.From,
			content: msg.Content,
			time:    time.Now().Format("15:04"),
			isSelf:  msg.From == m.userName,
		}
		m.appendMessage(session, chatMsg)

		if m.activeSession == session {
			m.renderChat()
		}
		return m, m.waitForMessage() // Continue listening
	}

	if !m.focusSideBar {
		m.chatInput, cmd = m.chatInput.Update(msg)
		cmds = append(cmds, cmd)
	}
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// --- Helpers ---

func (m *model) appendMessage(session string, msg chatMessage) {
	hist := m.chatHistory[session]
	hist = append(hist, msg)
	if len(hist) > 50 {
		hist = hist[len(hist)-50:]
	}
	m.chatHistory[session] = hist
}

func (m *model) sendMessage(text string) {
	var msg im.ClientMessage
	if m.activeSession == "Global" {
		msg = im.ClientMessage{
			Type:    "broadcast",
			Content: text,
		}
	} else {
		msg = im.ClientMessage{
			Type:    "private",
			To:      m.activeSession,
			Content: text,
		}
		// Manually add self message to history
		chatMsg := chatMessage{
			sender:  m.userName,
			content: text,
			time:    time.Now().Format("15:04"),
			isSelf:  true,
		}
		m.appendMessage(m.activeSession, chatMsg)
		m.renderChat()
	}

	m.conn.WriteJSON(msg)
}

func (m *model) renderChat() {
	msgs := m.chatHistory[m.activeSession]
	var viewStrings []string
	width := m.viewport.Width // Full width

	for _, msg := range msgs {
		if msg.isSelf {
			// Right aligned for self
			content := fmt.Sprintf("%s :%s [%s]", msg.content, msg.sender, msg.time)
			bubble := selfStyle.Render(content)
			// Use PlaceHorizontal for reliable alignment
			line := lipgloss.PlaceHorizontal(width, lipgloss.Right, bubble)
			viewStrings = append(viewStrings, line)
		} else {
			// Left aligned for others
			content := fmt.Sprintf("[%s] %s: %s", msg.time, msg.sender, msg.content)
			bubble := senderStyle.Render(content)
			viewStrings = append(viewStrings, bubble)
		}
	}
	m.viewport.SetContent(strings.Join(viewStrings, "\n"))
	m.viewport.GotoBottom()
}

func (m model) connectWebSocket() tea.Cmd {
	return func() tea.Msg {
		u := url.URL{Scheme: "ws", Host: tuiServerAddr, Path: "/ws", RawQuery: "user_id=" + m.userName}
		conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			return errMsg(err)
		}
		return connectedMsg{conn: conn}
	}
}

func (m model) waitForMessage() tea.Cmd {
	return func() tea.Msg {
		_, message, err := m.conn.ReadMessage()
		if err != nil {
			slog.Error("Read error", "error", err)
			return errMsg(err)
		}
		var msg im.ServerMessage
		json.Unmarshal(message, &msg)
		return wsMsg(msg)
	}
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

// --- View ---

func (m model) View() string {
	if m.state == stateLogin {
		return fmt.Sprintf("\n\n  Welcome to Ikun Messenger\n\n  Username: %s\n\n  (Enter to login, Esc to quit)", m.loginInput.View())
	}

	// Sidebar
	// 1. Header
	header := lipgloss.NewStyle().
		Foreground(lipgloss.Color("205")).
		Bold(true).
		Padding(0, 1).
		Render(fmt.Sprintf("👤 %s", m.userName))

	// 2. Session List
	var sessionList []string
	for i, s := range m.sessions {
		cursor := " "
		if m.focusSideBar && m.sidebarCursor == i {
			cursor = ">"
		}

		style := noStyle
		if s == m.activeSession {
			style = focusedStyle
		}
		sessionList = append(sessionList, style.Render(fmt.Sprintf("%s %s", cursor, s)))
	}

	// 3. Combine Sidebar
	sidebarContent := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", 18)),
		strings.Join(sessionList, "\n"),
	)

	sidebar := lipgloss.NewStyle().
		Width(20).
		Height(m.height - 4).
		Border(lipgloss.RoundedBorder()).
		BorderRight(true).
		Render(sidebarContent)

	// Chat Area
	chatStyle := inactiveBorderStyle
	if !m.focusSideBar {
		chatStyle = activeBorderStyle
	}

	chatView := chatStyle.
		Width(m.width - 25).
		Height(m.height - 7).
		Render(m.viewport.View())

	inputView := m.chatInput.View()

	rightPane := lipgloss.JoinVertical(lipgloss.Left, chatView, inputView)

	mainView := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, rightPane)

	// Help Bar
	helpText := "  Tab: Focus Sidebar • Ctrl+n: New Chat • ↑/↓: Switch Session • Enter: Send/Select • Esc: Quit"
	helpView := helpStyle.Copy().Width(m.width).Render(helpText)

	finalView := lipgloss.JoinVertical(lipgloss.Left, mainView, helpView)

	if m.showNewChat {
		return fmt.Sprintf("\n  New Chat\n\n  Enter username: %s\n\n  (Esc to cancel)", m.newChatInput.View())
	}

	return finalView
}
