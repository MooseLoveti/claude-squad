package ui

import (
	"claude-squad/keys"
	"fmt"
	"strings"

	"claude-squad/session"

	"github.com/charmbracelet/lipgloss"
)

var keyStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{
	Light: "#655F5F",
	Dark:  "#7F7A7A",
})

var descStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{
	Light: "#7A7474",
	Dark:  "#9C9494",
})

var sepStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{
	Light: "#DDDADA",
	Dark:  "#3C3C3C",
})

var actionGroupStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))

var separator = " • "
var verticalSeparator = " │ "

var menuStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("205"))

// MenuState represents different states the menu can be in
type MenuState int

const (
	StateDefault MenuState = iota
	StateEmpty
	StateNewInstance
	StatePrompt
)

type Menu struct {
	options       []keys.KeyName
	height, width int
	state         MenuState
	instance      *session.Instance
	activeTab     int
	relayMode     string
	relayCount    int
	relayMax      int
	relayNote     string

	// keyDown is the key which is pressed. The default is -1.
	keyDown keys.KeyName
}

var defaultMenuOptions = []keys.KeyName{keys.KeyNew, keys.KeyPrompt, keys.KeyHelp, keys.KeyQuit}
var newInstanceMenuOptions = []keys.KeyName{keys.KeySubmitName}
var promptMenuOptions = []keys.KeyName{keys.KeySubmitName}

func NewMenu() *Menu {
	return &Menu{
		options:   defaultMenuOptions,
		state:     StateEmpty,
		activeTab: 0,
		keyDown:   -1,
		relayMode: "MANUAL",
	}
}

// SetRelayStatus updates the relay indicator shown above the key menu. note
// adds relay progress, such as which answers are still owed.
func (m *Menu) SetRelayStatus(mode string, count, max int, note string) {
	m.relayMode = mode
	m.relayCount = count
	m.relayMax = max
	m.relayNote = note
}

func (m *Menu) Keydown(name keys.KeyName) {
	m.keyDown = name
}

func (m *Menu) ClearKeydown() {
	m.keyDown = -1
}

// SetState updates the menu state and options accordingly
func (m *Menu) SetState(state MenuState) {
	m.state = state
	m.updateOptions()
}

// SetInstance updates the current instance and refreshes menu options
func (m *Menu) SetInstance(instance *session.Instance) {
	m.instance = instance
	// Only change the state if we're not in a special state (NewInstance or Prompt)
	if m.state != StateNewInstance && m.state != StatePrompt {
		if m.instance != nil {
			m.state = StateDefault
		} else {
			m.state = StateEmpty
		}
	}
	m.updateOptions()
}

// SetActiveTab updates the currently active tab
func (m *Menu) SetActiveTab(tab int) {
	m.activeTab = tab
	m.updateOptions()
}

// updateOptions updates the menu options based on current state and instance
func (m *Menu) updateOptions() {
	switch m.state {
	case StateEmpty:
		m.options = defaultMenuOptions
	case StateDefault:
		if m.instance != nil {
			// When there is an instance, show that instance's options
			m.addInstanceOptions()
		} else {
			// When there is no instance, show the empty state
			m.options = defaultMenuOptions
		}
	case StateNewInstance:
		m.options = newInstanceMenuOptions
	case StatePrompt:
		m.options = promptMenuOptions
	}
}

func (m *Menu) addInstanceOptions() {
	// Loading instances only get minimal options
	if m.instance != nil && m.instance.Status == session.Loading {
		m.options = []keys.KeyName{keys.KeyNew, keys.KeyHelp, keys.KeyQuit}
		return
	}

	// Instance management group
	options := []keys.KeyName{keys.KeyNew, keys.KeyKill}

	// Action group
	actionGroup := []keys.KeyName{keys.KeyEnter, keys.KeySubmit, keys.KeyAsk, keys.KeyRelay}
	if m.instance.Status == session.Paused {
		actionGroup = append(actionGroup, keys.KeyResume)
	} else {
		actionGroup = append(actionGroup, keys.KeyCheckout)
	}

	// Navigation group (when in diff tab)
	if m.activeTab == DiffTab || m.activeTab == TerminalTab {
		actionGroup = append(actionGroup, keys.KeyShiftUp)
	}

	// System group
	systemGroup := []keys.KeyName{keys.KeyTab, keys.KeyRelayMode, keys.KeyHelp, keys.KeyQuit}

	// Combine all groups
	options = append(options, actionGroup...)
	options = append(options, systemGroup...)

	m.options = options
}

// SetSize sets the width of the window. The menu will be centered horizontally within this width.
func (m *Menu) SetSize(width, height int) {
	m.width = width
	m.height = height
}

func (m *Menu) String() string {
	var s strings.Builder

	// Define group boundaries
	groups := []struct {
		start int
		end   int
	}{
		{0, 2},  // Instance management group (n, d)
		{2, 7},  // Action group (enter, submit, ask, relay, pause/resume)
		{7, 11}, // System group (tab, relay mode, help, q)
	}
	if m.state == StateEmpty {
		groups = []struct{ start, end int }{{0, 2}, {2, len(m.options)}}
	}

	for i, k := range m.options {
		binding := keys.GlobalkeyBindings[k]

		var (
			localActionStyle = actionGroupStyle
			localKeyStyle    = keyStyle
			localDescStyle   = descStyle
		)
		if m.keyDown == k {
			localActionStyle = localActionStyle.Underline(true)
			localKeyStyle = localKeyStyle.Underline(true)
			localDescStyle = localDescStyle.Underline(true)
		}

		var inActionGroup bool
		switch m.state {
		case StateEmpty:
			// For empty state, the action group is the first group
			inActionGroup = i <= 1
		default:
			// For other states, the action group is the second group
			inActionGroup = i >= groups[1].start && i < groups[1].end
		}

		if inActionGroup {
			s.WriteString(localActionStyle.Render(binding.Help().Key))
			s.WriteString(" ")
			s.WriteString(localActionStyle.Render(binding.Help().Desc))
		} else {
			s.WriteString(localKeyStyle.Render(binding.Help().Key))
			s.WriteString(" ")
			s.WriteString(localDescStyle.Render(binding.Help().Desc))
		}

		// Add appropriate separator
		if i != len(m.options)-1 {
			isGroupEnd := false
			for _, group := range groups {
				if i == group.end-1 {
					s.WriteString(sepStyle.Render(verticalSeparator))
					isGroupEnd = true
					break
				}
			}
			if !isGroupEnd {
				s.WriteString(sepStyle.Render(separator))
			}
		}
	}

	relayStatus := fmt.Sprintf("Relay: %s", m.relayMode)
	if m.relayMode == "AUTO" {
		relayStatus = fmt.Sprintf("Relay: AUTO %d/%d", m.relayCount, m.relayMax)
	}
	if m.relayNote != "" {
		relayStatus += "  ·  " + m.relayNote
	}
	centeredMenuText := lipgloss.JoinVertical(
		lipgloss.Center,
		menuStyle.Render(relayStatus),
		menuStyle.Render(s.String()),
	)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, centeredMenuText)
}
