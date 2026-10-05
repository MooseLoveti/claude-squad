package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func TestScrollOverlayScrollsAndClosesOnlyOnEscOrQ(t *testing.T) {
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = "line"
	}
	o := NewScrollOverlay("Answers", strings.Join(lines, "\n")+"\nLAST")
	o.SetSize(40, 16)
	require.NotContains(t, o.Render(), "LAST")
	require.Contains(t, o.Render(), "↓ more")

	require.False(t, o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}), "other keys do not close")
	for i := 0; i < 60; i++ {
		require.False(t, o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}))
	}
	require.Contains(t, o.Render(), "LAST")
	require.NotContains(t, o.Render(), "↓ more")

	require.True(t, o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}))
	require.True(t, o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}))
}

func TestScrollOverlayWrapsLongLines(t *testing.T) {
	o := NewScrollOverlay("Answers", strings.Repeat("あ", 60))
	o.SetSize(30, 20)
	for _, line := range strings.Split(o.Render(), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 30, "wide characters count as two cells")
	}
}
