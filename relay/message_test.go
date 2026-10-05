package relay

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuestionFormat(t *testing.T) {
	from := Sender{Agent: AgentClaude, WorktreePath: "/wt/claude", TranscriptPath: "/cfg/relay/claude.md",
		Latest: &Exchange{User: "日本最強の動物は？", Assistant: "ヒグマです。"}}
	q := Question{From: from, FromUser: true, To: AgentCodex, ToDir: "/wt/codex",
		AlsoAsked: []Agent{AgentAntigravity}, Text: " お前らどう思う？ "}
	require.Equal(t,
		"[Claude Codeからの相談（ユーザーの質問）]\n"+
			"Claude Code の作業ディレクトリ（あなたは読み取りのみ）: /wt/claude\n"+
			"Claude Code の会話全文: /cfg/relay/claude.md\n"+
			"--- Claude Code の直近のやり取り ---\n"+
			"ユーザー: 日本最強の動物は？\n"+
			"Claude Code: ヒグマです。\n---\n"+
			"Gemini にも同じ質問をしています。\n"+
			"回答は Claude Code にそのまま渡されます。\n"+
			"\nお前らどう思う？",
		q.Format())
}

func TestQuestionFormatFromAgentInSharedDirectory(t *testing.T) {
	q := Question{From: Sender{Agent: AgentCodex, WorktreePath: "/work"}, To: AgentClaude, ToDir: "/work", Text: "review"}
	require.Equal(t,
		"[Codexからの相談]\n"+
			"作業ディレクトリ（Codex と共有）: /work\n"+
			"回答は Codex にそのまま渡されます。\n"+
			"\nreview",
		q.Format(), "an agent's own question, no history, no other recipients, same directory")
}

func TestFormatAnswersToTheUsersQuestion(t *testing.T) {
	got := FormatAnswers("which container? ", true, []Answer{
		{From: Sender{Agent: AgentCodex, WorktreePath: "/wt/codex"}, Text: "use a map\n"},
		{From: Sender{Agent: AgentAntigravity, WorktreePath: "/wt/agy"}, Text: "use a slice"},
	})
	require.Equal(t,
		"[ユーザーの質問（@codex @gemini 宛て）]\n"+
			"which container?\n"+
			"\n[Codexの意見]\n"+
			"（Codex の作業ディレクトリ: /wt/codex）\n"+
			"use a map\n"+
			"\n[Geminiの意見]\n"+
			"（Gemini の作業ディレクトリ: /wt/agy）\n"+
			"use a slice",
		got)
}

func TestFormatAnswersToAnAgentsQuestion(t *testing.T) {
	require.Equal(t, "[あなたの質問]\nq\n\n[Claude Codeの意見]\na",
		FormatAnswers("q", false, []Answer{{From: Sender{Agent: AgentClaude}, Text: "a"}}))
}

func TestLaunchArgs(t *testing.T) {
	env := LaunchEnv{SharedDir: "/cfg", Self: "/bin/it's cs"}
	args := AgentClaude.LaunchArgs(env)
	require.Equal(t, []string{"--add-dir", "/cfg", "--settings"}, args[:3])
	var settings struct {
		StatusLine struct{ Type, Command string } `json:"statusLine"`
	}
	require.NoError(t, json.Unmarshal([]byte(args[3]), &settings))
	require.Equal(t, "command", settings.StatusLine.Type)
	require.Equal(t, `'/bin/it'\''s cs' statusline`, settings.StatusLine.Command)

	require.Equal(t, []string{"--add-dir", "/cfg"}, AgentClaude.LaunchArgs(LaunchEnv{SharedDir: "/cfg"}),
		"no status line without a claude-squad executable to call")
	require.Nil(t, AgentCodex.LaunchArgs(env))
	require.Nil(t, AgentAntigravity.LaunchArgs(env), "--add-dir would move agy's command cwd")
	require.Nil(t, AgentUnknown.LaunchArgs(env))
}

func TestAgentNamesAndSlots(t *testing.T) {
	slots := map[Slot]bool{}
	for _, agent := range Agents() {
		require.Equal(t, agent, AgentByName(agent.Name()))
		require.False(t, slots[agent.Slot()], "%s shares a slot", agent.Label())
		slots[agent.Slot()] = true
	}
	require.Equal(t, "gemini", AgentAntigravity.Name())
	require.Equal(t, SlotTopLeft, AgentCodex.Slot())
	require.Equal(t, SlotBottomLeft, AgentClaude.Slot())
	require.Equal(t, SlotBottomRight, AgentAntigravity.Slot())
	require.Equal(t, AgentUnknown, AgentByName("aider"))
}

func TestBusyReadsPaneOrLog(t *testing.T) {
	require.True(t, AgentClaude.Busy("/nowhere", "⏺ working\n✻ Thinking… (esc to interrupt)\n"))
	require.False(t, AgentClaude.Busy("/nowhere", "⏺ done\n❯ \n"))
	require.False(t, AgentUnknown.Busy("/nowhere", "esc to interrupt"))
	t.Setenv("HOME", t.TempDir())
	require.False(t, AgentAntigravity.Busy("/nowhere", "esc to interrupt"), "agy reads its log, and no log means idle")
}
