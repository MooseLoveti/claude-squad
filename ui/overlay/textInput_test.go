package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func typeText(o *TextInputOverlay, text string) {
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

func TestEnterAddsNewlineByDefault(t *testing.T) {
	o := NewTextInputOverlay("Enter prompt", "")
	typeText(o, "line one")

	closed, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.False(t, closed)
	require.False(t, o.IsSubmitted())
	typeText(o, "two")
	require.Equal(t, "line one\ntwo", o.GetValue())
}

func TestSubmitOnEnter(t *testing.T) {
	o := NewTextInputOverlay("Ask", "")
	o.SubmitOnEnter = true
	typeText(o, "@codex これまでの流れを確認して")

	closed, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, closed)
	require.True(t, o.IsSubmitted())
	require.Equal(t, "@codex これまでの流れを確認して", o.GetValue())
}
