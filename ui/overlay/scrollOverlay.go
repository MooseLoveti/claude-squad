package overlay

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	scrollTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))
	scrollHintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// ScrollOverlay shows text too long for one screen. Unlike TextOverlay it
// scrolls, and only Esc or q close it, so reading never dismisses it by accident.
type ScrollOverlay struct {
	title    string
	content  string
	viewport viewport.Model
}

func NewScrollOverlay(title, content string) *ScrollOverlay {
	return &ScrollOverlay{title: title, content: content, viewport: viewport.New(0, 0)}
}

// SetSize fits the overlay into width x height cells, rewrapping the content.
func (s *ScrollOverlay) SetSize(width, height int) {
	// Border (2) and padding (2 per side) take width; title, hint and their
	// blank lines plus the border take height.
	innerWidth := max(width-6, 10)
	s.viewport.Width = innerWidth
	s.viewport.Height = max(height-6, 3)
	s.viewport.SetContent(lipgloss.NewStyle().Width(innerWidth).Render(s.content))
}

// HandleKeyPress scrolls, and reports true when the overlay should close.
func (s *ScrollOverlay) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "esc", "q":
		return true
	}
	s.viewport, _ = s.viewport.Update(msg)
	return false
}

func (s *ScrollOverlay) Render(opts ...WhitespaceOption) string {
	hint := "↑/↓ PgUp/PgDn scroll · esc close"
	if !s.viewport.AtBottom() {
		hint = "↓ more · " + hint
	}
	body := lipgloss.JoinVertical(lipgloss.Left,
		scrollTitleStyle.Render(s.title),
		"",
		s.viewport.View(),
		"",
		scrollHintStyle.Render(hint),
	)
	// The width covers content plus padding, so the title and hint wrap too
	// instead of widening the box past the size it was given.
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(0, 2).
		Width(s.viewport.Width + 4).
		Render(body)
}
