package relay

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectAgent(t *testing.T) {
	require.Equal(t, AgentClaude, DetectAgent("/home/user/.local/bin/claude"))
	require.Equal(t, AgentCodex, DetectAgent("codex --model gpt-5"))
	require.Equal(t, AgentUnknown, DetectAgent("aider"))
}

func TestLatestResponseClaude(t *testing.T) {
	pane := "❯ previous prompt\n⏺ old response\n❯ current prompt\n● @codex\n  review auth.go\n\n  keep comments\n\n✻ Sautéed for 1s\n────────────\n❯ "
	got, err := LatestResponse(AgentClaude, pane)
	require.NoError(t, err)
	require.Equal(t, "@codex\n  review auth.go\n\n  keep comments", got)
}

func TestLatestResponseCodexStripsANSI(t *testing.T) {
	pane := "› prompt\n\x1b[32m• @claude\x1b[0m\n  fix the race\n› "
	got, err := LatestResponse(AgentCodex, pane)
	require.NoError(t, err)
	require.Equal(t, "@claude\n  fix the race", got)
}

func TestLatestResponseIDIgnoresDynamicFooter(t *testing.T) {
	first := "❯ prompt\n● @codex\nreview auth.go\n✻ Brewed for 1s\nstatus one\n    ● high · /effort"
	second := "❯ prompt\n● @codex\nreview auth.go\n✻ Brewed for 2s\nstatus two\n    ● low · /effort"
	_, firstID, err := LatestResponseWithID(AgentClaude, first)
	require.NoError(t, err)
	_, secondID, err := LatestResponseWithID(AgentClaude, second)
	require.NoError(t, err)
	require.Equal(t, firstID, secondID)
}

func TestLatestResponseIDDistinguishesRepeatedResponse(t *testing.T) {
	first := "› prompt one\n• same reply\n› "
	second := first + "\n› prompt two\n• same reply\n› "
	_, firstID, err := LatestResponseWithID(AgentCodex, first)
	require.NoError(t, err)
	_, secondID, err := LatestResponseWithID(AgentCodex, second)
	require.NoError(t, err)
	require.NotEqual(t, firstID, secondID)
}

func TestTaggedMessageUsesLastTag(t *testing.T) {
	got := TaggedMessage("example @codex stale\n@codex\nreview auth.go", "@codex")
	require.Equal(t, "review auth.go", got)
}

func TestFormatMessage(t *testing.T) {
	require.Equal(t, "[Message from Claude Code]\n\nreview auth.go", FormatMessage(AgentClaude, " review auth.go "))
}
