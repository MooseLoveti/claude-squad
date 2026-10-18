package relay

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBriefingPointsAtEachPeersWorktreeAndTranscript(t *testing.T) {
	got := Briefing(AgentClaude, "/wt/claude", []Peer{
		{Agent: AgentCodex, Dir: "/wt/codex", Branch: "me/codex"},
		{Agent: AgentAntigravity, Dir: "/wt/gemini", Branch: "me/gemini"},
	}, "/bin/it's cs")
	require.True(t, strings.HasPrefix(got, "[claude-squad] あなたは claude-squad で動いている Claude Code です。同じプロジェクトで Codex・Gemini も並行して作業しています。\n"), got)
	require.Contains(t, got, "あなたの作業ディレクトリ: /wt/claude\n")
	require.Contains(t, got, "ほかのAIの作業ディレクトリ（読み取りのみ。編集しないこと）:\n- Codex: /wt/codex（ブランチ me/codex）\n- Gemini: /wt/gemini（ブランチ me/gemini）\n")
	require.Contains(t, got, "- Codex: '/bin/it'\\''s cs' transcript codex\n- Gemini: '/bin/it'\\''s cs' transcript gemini\n")
	require.Contains(t, got, "「ほかのAIの記録は参照できない」とは答えないでください。")
	require.NotContains(t, got, "@", "quoting the briefing must never route a message")
}

func TestBriefingInASharedDirectory(t *testing.T) {
	got := Briefing(AgentCodex, "/proj", []Peer{
		{Agent: AgentClaude, Dir: "/proj"},
		{Agent: AgentAntigravity, Dir: "/proj"},
	}, "cs")
	require.Contains(t, got, "あなたの作業ディレクトリ: /proj（Claude Code・Gemini と共有。同じファイルを同時に編集しないよう注意）\n")
	require.NotContains(t, got, "読み取りのみ", "there is no other directory to read")
	require.Contains(t, got, "- Claude Code: 'cs' transcript claude\n")
}

func TestBriefingWithoutExecutableLeavesOutCommands(t *testing.T) {
	got := Briefing(AgentAntigravity, "/wt/g", []Peer{{Agent: AgentCodex, Dir: "/wt/x"}}, "")
	require.Contains(t, got, "- Codex: /wt/x\n", "no branch, no branch note")
	require.NotContains(t, got, "transcript")
}
