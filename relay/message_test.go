package relay

import (
	"encoding/json"
	"strings"
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
	require.Nil(t, AgentCodex.LaunchArgs(env), "nothing to pass without a briefing")
	require.Nil(t, AgentAntigravity.LaunchArgs(env), "--add-dir would move agy's command cwd")
	require.Nil(t, AgentUnknown.LaunchArgs(env))

	env.Briefing = "line \"one\"\nline two"
	require.Equal(t, []string{"--add-dir", "/cfg", "--append-system-prompt", env.Briefing, "--settings"}, AgentClaude.LaunchArgs(env)[:5])
	codex := AgentCodex.LaunchArgs(env)
	require.Len(t, codex, 2)
	require.Equal(t, "-c", codex[0])
	value, ok := strings.CutPrefix(codex[1], "developer_instructions=")
	require.True(t, ok, codex[1])
	var decoded string
	require.NoError(t, json.Unmarshal([]byte(value), &decoded), "the value is one quoted string, as TOML reads it")
	require.Equal(t, env.Briefing, decoded)
	require.Nil(t, AgentAntigravity.LaunchArgs(env), "agy reads its briefing from GEMINI.md")
}

func TestNoticeKeys(t *testing.T) {
	// As Codex 0.160 draws it in a 115-column pane.
	notice := "  Welcome.\n\n\n  Set up security for Daybreak mode\n  Set up Advanced Account Security with a hardware security key. You can keep using Codex while you finish\n  setup.\n\n› 1. Set up security\n\n  Press a number to choose · esc to dismiss · type to continue\n\n\n› Ask Codex to do anything\n\n  GPT-6.1-Sol default · ~/wt/codex\n  ? for shortcuts      ⚠ 2 warnings · f2 to view\n\n\n"
	require.Equal(t, []string{"Escape"}, AgentCodex.NoticeKeys(notice))
	require.Nil(t, AgentCodex.NoticeKeys(codexIdle), "no notice, nothing to dismiss")
	require.Nil(t, AgentCodex.NoticeKeys("Set up security for Daybreak mode\n"+strings.Repeat("• reply line\n", noticeTailLines)),
		"a notice scrolled far up is no longer showing")
	require.Nil(t, AgentClaude.NoticeKeys(notice), "only Codex shows this notice")

	update := "  ✨ Update available! 0.162.0 -> 0.163.0\n\n› 1. Update now (runs `sh -c 'curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh'`)\n  2. Skip\n  3. Skip until next version\n\n  enter continue · esc skip\n"
	require.Equal(t, []string{"Escape"}, AgentCodex.NoticeKeys(update), "skipped this time only")

	codexTrust := "  Trust this folder? Codex can read, edit, and run files here, subject to your permission settings.\n› 1. Trust and continue\n  2. Quit\n  enter continue · esc quit\n"
	require.Equal(t, []string{"Enter"}, AgentCodex.NoticeKeys(codexTrust))
	require.Nil(t, AgentCodex.NoticeKeys("  Trust this folder?\n  1. Trust and continue\n› 2. Quit\n"), "Enter would quit")

	claudeTrust := " Quick safety check: Is this a project you created or one you trust?\n Security guide\n ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"
	require.Equal(t, []string{"Down"}, AgentClaude.NoticeKeys(claudeTrust), "move to the trust choice first")
	claudeTrust = strings.Replace(strings.Replace(claudeTrust, "❯ No", "  No", 1), "  Yes", "❯ Yes", 1)
	require.Equal(t, []string{"Enter"}, AgentClaude.NoticeKeys(claudeTrust), "confirm once it is selected")

	agyTrust := "Do you trust the contents of this project?\nAntigravity CLI requires permission to read, edit, and execute files here.\n> Yes, I trust this folder\n  No, exit\n  ↑/↓ Navigate · enter Confirm\n"
	require.Equal(t, []string{"Enter"}, AgentAntigravity.NoticeKeys(agyTrust))
	agyTrust = strings.Replace(strings.Replace(agyTrust, "> Yes", "  Yes", 1), "  No, exit", "> No, exit", 1)
	require.Equal(t, []string{"Up"}, AgentAntigravity.NoticeKeys(agyTrust), "Enter would exit")

	trust := "In a trusted folder, edits and shell commands run without asking, here and in `reasonix run`:\n/wt/deepseek\n" +
		"Trust this folder? (`reasonix trust --revoke` undoes it) [y/N]: "
	require.Equal(t, []string{"y", "Enter"}, AgentDeepSeek.NoticeKeys(trust))
	require.Nil(t, AgentDeepSeek.NoticeKeys("  Reasonix asks \"Trust this folder?\" once per worktree.\n ❯ \n"),
		"a reply about the prompt is not the prompt")
}

const codexIdle = "• done\n\n› Ask Codex to do anything\n"

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
	for _, agent := range Agents() {
		require.NotEmpty(t, agent.Emoji(), agent.Label())
		require.Regexp(t, `^#[0-9A-F]{6}$`, agent.Color(), agent.Label())
	}
}

func TestBusyReadsPaneOrLog(t *testing.T) {
	require.True(t, AgentClaude.Busy("/nowhere", "⏺ working\n✻ Thinking… (esc to interrupt)\n"))
	require.False(t, AgentClaude.Busy("/nowhere", "⏺ done\n❯ \n"))
	require.False(t, AgentUnknown.Busy("/nowhere", "esc to interrupt"))
	t.Setenv("HOME", t.TempDir())
	require.False(t, AgentAntigravity.Busy("/nowhere", "esc to interrupt"), "agy reads its log, and no log means idle")
}
