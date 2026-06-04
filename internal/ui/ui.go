// Package ui implements the BubbleTea TUI for Kin.
package ui

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"kin/internal/store"
	"kin/internal/tor"
)

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

var (
	purple     = lipgloss.Color("#9B59F6")
	purpleDark = lipgloss.Color("#6D28D9")
	purpleDim  = lipgloss.Color("#4C1D95")
	teal       = lipgloss.Color("#14B8A6")
	green      = lipgloss.Color("#22C55E")
	red        = lipgloss.Color("#EF4444")
	amber      = lipgloss.Color("#F59E0B")
	muted      = lipgloss.Color("#6B7280")
	white      = lipgloss.Color("#F9FAFB")
	base       = lipgloss.Color("#0F0F1A")
	surface    = lipgloss.Color("#1A1A2E")
	border     = lipgloss.Color("#312E5A")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(purple).
			Background(base).
			Padding(0, 2)

	bannerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(teal).
			Background(purpleDim).
			Padding(0, 1).
			MarginBottom(1)

	sidebarStyle = lipgloss.NewStyle().
			Width(26).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Padding(0, 1).
			Background(surface)

	chatStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(purple).
			Padding(0, 1).
			Background(base)

	statusBarStyle = lipgloss.NewStyle().
			Background(purpleDim).
			Foreground(white).
			Padding(0, 1).
			Width(0) // set dynamically

	myBubbleStyle = lipgloss.NewStyle().
			Background(purpleDark).
			Foreground(white).
			Padding(0, 1).
			MarginLeft(4).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(purple)

	theirBubbleStyle = lipgloss.NewStyle().
				Background(surface).
				Foreground(white).
				Padding(0, 1).
				MarginRight(4).
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(teal)

	timestampStyle = lipgloss.NewStyle().Foreground(muted).Faint(true)
	mutedStyle     = lipgloss.NewStyle().Foreground(muted)
	greenStyle     = lipgloss.NewStyle().Foreground(green).Bold(true)
	redStyle       = lipgloss.NewStyle().Foreground(red).Bold(true)
	amberStyle     = lipgloss.NewStyle().Foreground(amber).Bold(true)
	tealStyle      = lipgloss.NewStyle().Foreground(teal).Bold(true)
	purpleStyle    = lipgloss.NewStyle().Foreground(purple).Bold(true)

	selectedContactStyle = lipgloss.NewStyle().
				Background(purpleDim).
				Foreground(white).
				Bold(true).
				Padding(0, 1)

	contactStyle = lipgloss.NewStyle().
			Foreground(white).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(teal).
			BorderBottom(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(border).
			MarginBottom(1)
)

// ---------------------------------------------------------------------------
// Messages (BubbleTea commands)
// ---------------------------------------------------------------------------

type IncomingMsg struct {
	From string
	Body string
}

type IceStateMsg struct{ S string }
type TorStateMsg struct{ Line string }
type OnionAddrMsg struct{ Addr string }
type ErrorMsg struct{ Err error }
type TickMsg time.Time
type ContactsReloadMsg struct{ Contacts []*store.Contact }
type HistoryMsg struct {
	PeerID   string
	Messages []*store.Message
}
type SendResultMsg struct{ Err error }
type AddContactDoneMsg struct{ Contact *store.Contact }
type SDPOfferReadyMsg struct {
	OfferJSON string
}
type IncomingConnectionMsg struct {
	From  string
	Offer tor.SDPOffer
	Conn  net.Conn
}

// ---------------------------------------------------------------------------
// Screens
// ---------------------------------------------------------------------------

type Screen int

const (
	ScreenMain Screen = iota
	ScreenAddContact
	ScreenConnectPeer // manual ICE signaling
	ScreenSettings
	ScreenIncomingPrompt
)

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

// Callbacks wired from main into the TUI.
type Callbacks struct {
	SendMessage      func(peerID, body string) error
	ConnectPeer      func(peerID string)           // dial .onion for SDP
	AddContact       func(id, nickname string) error
	DeleteContact    func(id string) error
	GetHistory       func(peerID string) []*store.Message
	GetContacts      func() []*store.Contact
	GetMyOnion       func() string
	PasteOffer       func(offerJSON string) error  // paste remote SDP JSON
	GetLocalOffer    func() string                 // get our SDP offer JSON
	AcceptConnection func(offer tor.SDPOffer, conn net.Conn)
	RejectConnection func(conn net.Conn)
	ClearChat        func(peerID string) error
}

// Model is the root BubbleTea model.
type Model struct {
	width, height int
	screen        Screen
	activeView    string // "contacts" or "chat"

	contacts    []*store.Contact
	selectedIdx int
	messages    []*store.Message
	myOnion     string

	chatVP   viewport.Model
	input    textarea.Model
	cmdInput textinput.Model // for add-contact / settings screens

	// ICE / Tor status
	iceState  string
	torLog    []string // last N lines
	torOnline bool

	// Add-contact form
	acStep     int    // 0=onion, 1=nickname
	acOnion    string
	acNickname string

	// Connect-peer (manual SDP paste)
	cpStep    int    // 0=waiting our offer, 1=paste their offer
	cpOffer   string // our offer JSON
	cpPeer    string // target onion

	// Incoming connection details
	icFrom    string
	icOffer   tor.SDPOffer
	icConn    net.Conn

	// Layout fields
	sidebarWidth int
	chatWidth    int
	bodyHeight   int
	keysText     string

	// Log panel shown in main
	logLines []string

	cb Callbacks
}

func NewModel(cb Callbacks) Model {
	ta := textarea.New()
	ta.Placeholder = "Type a message… (Enter to send, Ctrl+D newline)"
	ta.Focus()
	ta.SetWidth(60)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.CharLimit = 2000
	ta.KeyMap.InsertNewline.SetKeys("ctrl+d")

	ti := textinput.New()
	ti.CharLimit = 200

	vp := viewport.New(60, 20)

	return Model{
		screen:     ScreenMain,
		activeView: "contacts",
		input:      ta,
		cmdInput:   ti,
		chatVP:     vp,
		iceState:   "idle",
		cb:         cb,
	}
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		tickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.relayout()

	case TickMsg:
		// Reload contacts periodically
		if m.cb.GetContacts != nil {
			m.contacts = m.cb.GetContacts()
		}
		if m.cb.GetMyOnion != nil {
			m.myOnion = m.cb.GetMyOnion()
		}
		if m.activeView == "chat" {
			m.loadHistory(false)
		}
		cmds = append(cmds, tickCmd())

	case ContactsReloadMsg:
		m.contacts = msg.Contacts

	case OnionAddrMsg:
		m.myOnion = msg.Addr
		m.torOnline = true

	case TorStateMsg:
		m.torLog = append(m.torLog, msg.Line)
		if len(m.torLog) > 80 {
			m.torLog = m.torLog[len(m.torLog)-80:]
		}
		if strings.Contains(msg.Line, "Bootstrapped 100%") {
			m.torOnline = true
		}
		if strings.HasPrefix(msg.Line, "[sig]") || strings.HasPrefix(msg.Line, "[ICE]") || strings.Contains(strings.ToLower(msg.Line), "err") {
			m.appendLog(msg.Line)
		}

	case IceStateMsg:
		m.iceState = msg.S
		m.appendLog("ICE: " + msg.S)

	case IncomingMsg:
		// Add to messages if this peer is selected
		body := msg.Body
		msgObj := &store.Message{
			ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
			From:      msg.From,
			Body:      body,
			Timestamp: time.Now(),
		}
		m.messages = append(m.messages, msgObj)
		m.refreshChat(true)
		m.appendLog("← " + msg.From[:min(8, len(msg.From))] + "…: " + truncate(body, 40))

	case ErrorMsg:
		m.appendLog("ERR: " + msg.Err.Error())
		if m.screen == ScreenConnectPeer {
			m.screen = ScreenMain
			m.input.Focus()
		}

	case SDPOfferReadyMsg:
		m.cpOffer = msg.OfferJSON
		m.cpStep = 1

	case IncomingConnectionMsg:
		m.screen = ScreenIncomingPrompt
		m.icFrom = msg.From
		m.icOffer = msg.Offer
		m.icConn = msg.Conn

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "ctrl+q" {
			return m, tea.Quit
		}
		switch m.screen {
		case ScreenMain:
			return m.updateMain(msg, cmds)
		case ScreenAddContact:
			return m.updateAddContact(msg, cmds)
		case ScreenConnectPeer:
			return m.updateConnectPeer(msg, cmds)
		case ScreenSettings:
			return m.updateSettings(msg, cmds)
		case ScreenIncomingPrompt:
			return m.updateIncomingPrompt(msg, cmds)
		}
	}

	// Update sub-components
	if m.screen == ScreenMain {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
		m.chatVP, cmd = m.chatVP.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) updateMain(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "ctrl+q":
		return m, tea.Quit

	case "ctrl+n": // new contact
		m.screen = ScreenAddContact
		m.acStep = 0
		m.acOnion = ""
		m.acNickname = ""
		m.cmdInput.Reset()
		m.cmdInput.Placeholder = "Peer .onion address"
		m.cmdInput.Focus()
		return m, textinput.Blink

	case "ctrl+x": // delete contact
		if m.activeView == "contacts" && len(m.contacts) > 0 && m.selectedIdx < len(m.contacts) {
			peer := m.contacts[m.selectedIdx]
			if m.cb.DeleteContact != nil {
				if err := m.cb.DeleteContact(peer.ID); err == nil {
					m.contacts = m.cb.GetContacts()
					if m.selectedIdx >= len(m.contacts) {
						m.selectedIdx = len(m.contacts) - 1
					}
					if m.selectedIdx < 0 {
						m.selectedIdx = 0
					}
					m.loadHistory(true)
				}
			}
		}
		return m, nil

	case "ctrl+p": // connect/ping peer
		if len(m.contacts) > 0 && m.selectedIdx < len(m.contacts) {
			peer := m.contacts[m.selectedIdx]
			m.appendLog("Connecting to " + truncate(peer.Nickname, 12) + "...")
			if m.cb.ConnectPeer != nil {
				go m.cb.ConnectPeer(peer.ID)
			}
		}
		return m, nil

	case "ctrl+l": // clear chat history
		if len(m.contacts) > 0 && m.selectedIdx < len(m.contacts) {
			peer := m.contacts[m.selectedIdx]
			if m.cb.ClearChat != nil {
				if err := m.cb.ClearChat(peer.ID); err == nil {
					m.appendLog("Cleared chat history with " + truncate(peer.Nickname, 12))
					m.loadHistory(true)
				} else {
					m.appendLog("Failed to clear chat: " + err.Error())
				}
			}
		}
		return m, nil

	case "ctrl+s": // settings
		m.screen = ScreenSettings
		m.cmdInput.Reset()
		m.cmdInput.Placeholder = "New nickname"
		m.cmdInput.Focus()
		return m, textinput.Blink
	}

	if m.activeView == "contacts" {
		switch msg.String() {
		case "up", "k":
			if m.selectedIdx > 0 {
				m.selectedIdx--
				m.loadHistory(true)
			}
		case "down", "j":
			if m.selectedIdx < len(m.contacts)-1 {
				m.selectedIdx++
				m.loadHistory(true)
			}
		case "enter", "right", "l":
			if len(m.contacts) > 0 && m.selectedIdx < len(m.contacts) {
				m.activeView = "chat"
				m.input.Focus()
				m.loadHistory(true)
				m.relayout()
			}
		}
	} else { // activeView == "chat"
		if m.input.Focused() {
			switch msg.String() {
			case "esc":
				m.activeView = "contacts"
				m.input.Blur()
				m.relayout()
				return m, nil
			case "tab":
				m.input.Blur()
				return m, nil
			case "pgup":
				m.chatVP.PageUp()
				return m, nil
			case "pgdn":
				m.chatVP.PageDown()
				return m, nil
			case "enter":
				if m.input.Value() != "" {
					body := m.input.Value()
					m.input.Reset()
					if len(m.contacts) > 0 && m.selectedIdx < len(m.contacts) {
						peer := m.contacts[m.selectedIdx]
						if m.cb.SendMessage != nil {
							err := m.cb.SendMessage(peer.ID, body)
							if err != nil {
								m.appendLog("Send error: " + err.Error())
							} else {
								// Add local echo
								m.messages = append(m.messages, &store.Message{
									ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
									From:      "me",
									Body:      body,
									Timestamp: time.Now(),
									Read:      false,
								})
								m.refreshChat(true)
							}
						}
					}
				}
				return m, nil
			}
		} else { // Scroll mode
			switch msg.String() {
			case "esc", "left", "h":
				m.activeView = "contacts"
				m.relayout()
				return m, nil
			case "tab":
				m.input.Focus()
				return m, nil
			case "up", "k":
				m.chatVP.LineUp(1)
				return m, nil
			case "down", "j":
				m.chatVP.LineDown(1)
				return m, nil
			case "pgup":
				m.chatVP.PageUp()
				return m, nil
			case "pgdn":
				m.chatVP.PageDown()
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) updateAddContact(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = ScreenMain
		m.input.Focus()
		return m, textarea.Blink
	case "enter":
		val := strings.TrimSpace(m.cmdInput.Value())
		m.cmdInput.Reset()
		if m.acStep == 0 {
			m.acOnion = val
			m.acStep = 1
			m.cmdInput.Placeholder = "Nickname"
		} else {
			m.acNickname = val
			if m.cb.AddContact != nil {
				err := m.cb.AddContact(m.acOnion, m.acNickname)
				if err != nil {
					m.appendLog("AddContact err: " + err.Error())
				}
			}
			m.screen = ScreenMain
			m.input.Focus()
			return m, textarea.Blink
		}
	}
	var cmd tea.Cmd
	m.cmdInput, cmd = m.cmdInput.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) updateConnectPeer(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = ScreenMain
		m.input.Focus()
		return m, textarea.Blink
	case "enter":
		if m.cpStep == 1 {
			// Paste their offer
			val := strings.TrimSpace(m.cmdInput.Value())
			m.cmdInput.Reset()
			if m.cb.PasteOffer != nil {
				if err := m.cb.PasteOffer(val); err != nil {
					m.appendLog("Offer paste err: " + err.Error())
				}
			}
			m.screen = ScreenMain
			m.input.Focus()
			return m, textarea.Blink
		}
	}
	var cmd tea.Cmd
	m.cmdInput, cmd = m.cmdInput.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) updateSettings(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.screen = ScreenMain
		m.input.Focus()
		return m, textarea.Blink
	}
	var cmd tea.Cmd
	m.cmdInput, cmd = m.cmdInput.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) updateIncomingPrompt(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "a", "A":
		if m.cb.AcceptConnection != nil {
			m.cb.AcceptConnection(m.icOffer, m.icConn)
		}
		m.screen = ScreenMain
		m.input.Focus()
		return m, textarea.Blink
	case "r", "R", "esc":
		if m.cb.RejectConnection != nil {
			m.cb.RejectConnection(m.icConn)
		}
		m.screen = ScreenMain
		m.input.Focus()
		return m, textarea.Blink
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m Model) View() string {
	if m.width == 0 {
		return "Loading…"
	}
	switch m.screen {
	case ScreenAddContact:
		return m.viewAddContact()
	case ScreenConnectPeer:
		return m.viewConnectPeer()
	case ScreenSettings:
		return m.viewSettings()
	case ScreenIncomingPrompt:
		return m.viewIncomingPrompt()
	}
	return m.viewMain()
}

func (m Model) viewMain() string {
	var body string
	if m.activeView == "chat" {
		body = m.renderChatView()
	} else {
		body = m.renderContactsView()
	}
	status := m.renderStatusBar()
	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderBanner(),
		body,
		status,
	)
}

func (m Model) renderBanner() string {
	onion := m.myOnion
	if onion == "" {
		onion = "starting tor…"
	}
	dot := redStyle.Render("●")
	if m.torOnline {
		dot = greenStyle.Render("●")
	}
	ice := mutedStyle.Render("ice:" + m.iceState)
	id := tealStyle.Render("kin://") + purpleStyle.Render(truncate(onion, 30))
	return bannerStyle.Copy().Width(m.width - 2).Render(
		fmt.Sprintf(" %s Tor %s  %s  %s", dot, dot, id, ice),
	)
}

func (m Model) renderContactsView() string {
	w := m.width - 2
	h := m.bodyHeight

	lines := []string{headerStyle.Render("Contacts")}
	for i, c := range m.contacts {
		label := c.Nickname
		if label == "" {
			label = truncate(c.ID, 20)
		}
		status := "offline"
		if time.Since(c.LastSeen) < 5*time.Minute {
			status = "online"
		}
		
		statusStr := mutedStyle.Render("offline")
		if status == "online" {
			statusStr = greenStyle.Render("online")
		}
		
		contactLine := fmt.Sprintf("  %s (%s)", label, statusStr)
		if i == m.selectedIdx {
			lines = append(lines, selectedContactStyle.Copy().Width(w - 4).Render("▶ " + label + " (" + status + ")"))
		} else {
			lines = append(lines, contactStyle.Render(contactLine))
		}
	}
	if len(m.contacts) == 0 {
		lines = append(lines, "")
		lines = append(lines, mutedStyle.Render("  No contacts yet."))
		lines = append(lines, mutedStyle.Render("  Press Ctrl+N to add a contact."))
	}

	content := strings.Join(lines, "\n")
	keysHelp := mutedStyle.Render("Enter:chat • Ctrl+N:add • Ctrl+X:delete • Ctrl+S:settings • Ctrl+Q:quit")
	
	return sidebarStyle.Copy().Width(w).Height(h).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			content,
			"",
			keysHelp,
		),
	)
}

func (m Model) renderChatView() string {
	w := m.width - 2
	h := m.bodyHeight

	var title string
	if len(m.contacts) > 0 && m.selectedIdx < len(m.contacts) {
		c := m.contacts[m.selectedIdx]
		name := c.Nickname
		if name == "" {
			name = truncate(c.ID, 20)
		}
		title = headerStyle.Render("◀ Back (Esc)  |  Chat: " + name + "  " + mutedStyle.Render(truncate(c.ID, 24)))
	} else {
		title = headerStyle.Render("No active chat")
	}

	focusMode := "Input Mode"
	if !m.input.Focused() {
		focusMode = "Scroll Mode (j/k/up/down)"
	}
	keysStr := fmt.Sprintf("Enter:send • Tab:%s • Esc:contacts • Ctrl+P:connect • Ctrl+L:clear", focusMode)
	keys := mutedStyle.Render(keysStr)

	return chatStyle.Copy().Width(w).Height(h).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			title,
			m.chatVP.View(),
			m.input.View(),
			keys,
		),
	)
}

func (m Model) renderStatusBar() string {
	torStatus := "Tor: " + redStyle.Render("offline")
	if m.torOnline {
		torStatus = "Tor: " + greenStyle.Render("online")
	}
	var lastLog string
	if len(m.logLines) > 0 {
		lastLog = mutedStyle.Render(truncate(m.logLines[len(m.logLines)-1], m.width-30))
	}
	return statusBarStyle.Width(m.width).Render(
		lipgloss.JoinHorizontal(lipgloss.Left, "  ", torStatus, "   ", lastLog),
	)
}

func (m Model) viewAddContact() string {
	label := "Step 1/2 — Enter peer .onion address"
	if m.acStep == 1 {
		label = fmt.Sprintf("Step 2/2 — Nickname for %s", truncate(m.acOnion, 20))
	}
	return center(m.width, m.height, lipgloss.JoinVertical(lipgloss.Left,
		bannerStyle.Render(" ✦ Add Contact"),
		"",
		purpleStyle.Render(label),
		"",
		m.cmdInput.View(),
		"",
		mutedStyle.Render("Enter to confirm  •  Esc to cancel"),
	))
}

func (m Model) viewConnectPeer() string {
	var content string
	switch m.cpStep {
	case 0:
		content = lipgloss.JoinVertical(lipgloss.Left,
			purpleStyle.Render("Connecting to: "+truncate(m.cpPeer, 30)),
			"",
			amberStyle.Render("⟳ Negotiating via Tor hidden service…"),
			"",
			mutedStyle.Render("Esc to cancel"),
		)
	case 1:
		content = lipgloss.JoinVertical(lipgloss.Left,
			purpleStyle.Render("Your offer (share with peer):"),
			"",
			tealStyle.Render(m.cpOffer),
			"",
			purpleStyle.Render("Paste peer's offer below:"),
			m.cmdInput.View(),
			"",
			mutedStyle.Render("Enter to confirm  •  Esc to cancel"),
		)
	}
	return center(m.width, m.height, lipgloss.JoinVertical(lipgloss.Left,
		bannerStyle.Render(" ⟺ Connect Peer"),
		"",
		content,
	))
}

func (m Model) viewSettings() string {
	return center(m.width, m.height, lipgloss.JoinVertical(lipgloss.Left,
		bannerStyle.Render(" ⚙ Settings"),
		"",
		purpleStyle.Render("Your Kin address:"),
		tealStyle.Render(m.myOnion),
		"",
		purpleStyle.Render("Set nickname:"),
		m.cmdInput.View(),
		"",
		mutedStyle.Render("Enter to save  •  Esc to go back"),
	))
}

func (m Model) viewIncomingPrompt() string {
	fromStr := m.icFrom
	if fromStr == "" {
		fromStr = "Unknown peer"
	}
	return center(m.width, m.height, lipgloss.JoinVertical(lipgloss.Left,
		bannerStyle.Render(" ☎ Incoming Connection Request"),
		"",
		purpleStyle.Render("Connection request from:"),
		tealStyle.Render(fromStr),
		"",
		amberStyle.Render("Do you want to establish an ICE P2P connection?"),
		"",
		greenStyle.Render("Press [ a ] to Accept"),
		redStyle.Render("Press [ r ] or [ Esc ] to Reject"),
	))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func wrapText(text string, limit int) string {
	lines := strings.Split(text, "\n")
	var wrappedLines []string
	for _, line := range lines {
		if len(line) <= limit {
			wrappedLines = append(wrappedLines, line)
			continue
		}
		words := strings.Fields(line)
		if len(words) == 0 {
			wrappedLines = append(wrappedLines, "")
			continue
		}
		var currentLine strings.Builder
		for _, word := range words {
			if currentLine.Len()+len(word)+1 > limit {
				if currentLine.Len() > 0 {
					wrappedLines = append(wrappedLines, currentLine.String())
					currentLine.Reset()
				}
				for len(word) > limit {
					wrappedLines = append(wrappedLines, word[:limit])
					word = word[limit:]
				}
			}
			if currentLine.Len() > 0 {
				currentLine.WriteString(" ")
			}
			currentLine.WriteString(word)
		}
		if currentLine.Len() > 0 {
			wrappedLines = append(wrappedLines, currentLine.String())
		}
	}
	return strings.Join(wrappedLines, "\n")
}

func (m *Model) relayout() {
	availableHeight := m.height - 3
	if availableHeight < 5 {
		availableHeight = 5
	}
	m.bodyHeight = availableHeight - 2

	chatW := m.width - 4
	if chatW < 20 {
		chatW = 20
	}

	titleHeight := 2 // title text + bottom border line
	keysHeight := 1
	inputHeight := lipgloss.Height(m.input.View())

	vpH := m.bodyHeight - titleHeight - inputHeight - keysHeight
	if vpH < 2 {
		vpH = 2
	}

	m.chatVP.Width = chatW - 2
	m.chatVP.Height = vpH
	m.input.SetWidth(chatW - 2)
}

func (m *Model) loadHistory(shouldGotoBottom bool) {
	if len(m.contacts) == 0 {
		return
	}
	peer := m.contacts[m.selectedIdx]
	if m.cb.GetHistory != nil {
		m.messages = m.cb.GetHistory(peer.ID)
	}
	m.refreshChat(shouldGotoBottom)
}

func (m *Model) refreshChat(shouldGotoBottom bool) {
	var sb strings.Builder
	maxBubbleWidth := m.chatVP.Width * 3 / 5
	if maxBubbleWidth < 20 {
		maxBubbleWidth = 20
	}
	if maxBubbleWidth > 60 {
		maxBubbleWidth = 60
	}

	for _, msg := range m.messages {
		ts := msg.Timestamp.Format("15:04")
		if msg.From == "me" {
			ticks := " ✓"
			if msg.Read {
				ticks = " ✓✓"
			}
			statusStr := timestampStyle.Render(ts) + purpleStyle.Render(ticks)
			wrapped := wrapText(msg.Body, maxBubbleWidth)
			line := myBubbleStyle.Render(wrapped)
			
			sb.WriteString(lipgloss.NewStyle().Width(m.chatVP.Width).Align(lipgloss.Right).Render(line) + "\n")
			sb.WriteString(lipgloss.NewStyle().Width(m.chatVP.Width).Align(lipgloss.Right).Render(statusStr) + "\n\n")
		} else {
			wrapped := wrapText(msg.Body, maxBubbleWidth)
			line := theirBubbleStyle.Render(wrapped)
			statusStr := timestampStyle.Render(ts)
			
			sb.WriteString(line + "\n")
			sb.WriteString(statusStr + "\n\n")
		}
	}
	m.chatVP.SetContent(sb.String())
	if shouldGotoBottom {
		m.chatVP.GotoBottom()
	}
}

func (m *Model) appendLog(line string) {
	m.logLines = append(m.logLines, line)
	if len(m.logLines) > 200 {
		m.logLines = m.logLines[len(m.logLines)-200:]
	}
}

// ExternalMsg sends an external event into the running program.
func ExternalMsg(p *tea.Program, msg tea.Msg) {
	p.Send(msg)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func center(w, h int, content string) string {
	lines := strings.Split(content, "\n")
	padTop := (h - len(lines)) / 2
	if padTop < 0 {
		padTop = 0
	}
	top := strings.Repeat("\n", padTop)
	return lipgloss.NewStyle().Width(w).Align(lipgloss.Center).Render(top + content)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
