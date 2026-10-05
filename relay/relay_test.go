package relay

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectAgent(t *testing.T) {
	require.Equal(t, AgentClaude, DetectAgent("/home/user/.local/bin/claude"))
	require.Equal(t, AgentCodex, DetectAgent("codex --model gpt-5"))
	require.Equal(t, AgentAntigravity, DetectAgent("agy --model flash"))
	require.Equal(t, AgentUnknown, DetectAgent("gemini"))
	require.Equal(t, AgentUnknown, DetectAgent("aider"))
}

func TestLatestPaneReplyClaude(t *testing.T) {
	pane := "❯ previous prompt\n⏺ old response\n❯ current prompt\n● @codex\n  review auth.go\n\n  keep comments\n\n✻ Sautéed for 1s\n────────────\n❯ "
	got, _, err := latestPaneReply(AgentClaude, pane)
	require.NoError(t, err)
	require.Equal(t, "@codex\n  review auth.go\n\n  keep comments", got)
}

func TestLatestPaneReplyCodexStripsANSI(t *testing.T) {
	pane := "› prompt\n\x1b[32m• @claude\x1b[0m\n  fix the race\n› "
	got, _, err := latestPaneReply(AgentCodex, pane)
	require.NoError(t, err)
	require.Equal(t, "@claude\n  fix the race", got)
}

func TestLatestPaneReplyDropsCodexFooter(t *testing.T) {
	pane := "› 1+1は？\n• 2\n\n  Worked for 4s • 20:41\n› Ask Codex to do anything"
	got, _, err := latestPaneReply(AgentCodex, pane)
	require.NoError(t, err)
	require.Equal(t, "2", got)

	pane = "› explain\n• Timing matters:\n  Worked for 4s is how long it took.\n  done\n› "
	got, _, err = latestPaneReply(AgentCodex, pane)
	require.NoError(t, err)
	require.Equal(t, "Timing matters:\n  Worked for 4s is how long it took.\n  done", got, "only trailing footers are trimmed")
}

func TestLatestPaneReplyKeepsIndentedPromptGlyphs(t *testing.T) {
	pane := "› prompt\n• quoting the input line:\n  › not a boundary when indented\n› "
	got, _, err := latestPaneReply(AgentCodex, pane)
	require.NoError(t, err)
	require.Equal(t, "quoting the input line:\n  › not a boundary when indented", got)
}

func TestLatestPaneReplyIDIgnoresDynamicFooter(t *testing.T) {
	first := "❯ prompt\n● @codex\nreview auth.go\n✻ Brewed for 1s\nstatus one\n    ● high · /effort"
	second := "❯ prompt\n● @codex\nreview auth.go\n✻ Brewed for 2s\nstatus two\n    ● low · /effort"
	_, firstID, err := latestPaneReply(AgentClaude, first)
	require.NoError(t, err)
	_, secondID, err := latestPaneReply(AgentClaude, second)
	require.NoError(t, err)
	require.Equal(t, firstID, secondID)
}

func TestLatestPaneReplyIDDistinguishesRepeatedText(t *testing.T) {
	first := "› prompt one\n• same reply\n› "
	second := first + "\n› prompt two\n• same reply\n› "
	_, firstID, err := latestPaneReply(AgentCodex, first)
	require.NoError(t, err)
	_, secondID, err := latestPaneReply(AgentCodex, second)
	require.NoError(t, err)
	require.NotEqual(t, firstID, secondID)
}

func TestLatestReplyUsesPaneOrLog(t *testing.T) {
	got, _, err := AgentCodex.LatestReply("/unused", "› hi\n• hello\n› ")
	require.NoError(t, err)
	require.Equal(t, "hello", got)

	t.Setenv("HOME", t.TempDir())
	_, _, err = AgentAntigravity.LatestReply("/wt/agy", "✦ a pane reply is ignored for agy")
	require.Error(t, err, "agy replies come from its session log, which does not exist here")

	_, _, err = AgentUnknown.LatestReply("/wt", "")
	require.Error(t, err)
}

func TestLatestReplyWaitsWhileBusy(t *testing.T) {
	working := "› question\n• Working (12s • esc to interrupt)\n› Ask Codex to do anything"
	_, _, err := AgentCodex.LatestReply("/unused", working)
	require.ErrorContains(t, err, "still working", "Codex's working status line is not a reply")

	done := "› question\n• The answer.\n› Ask Codex to do anything"
	got, _, err := AgentCodex.LatestReply("/unused", done)
	require.NoError(t, err)
	require.Equal(t, "The answer.", got)

	quoted := "› what does esc to interrupt do?\n• It stops the turn.\n" + strings.Repeat("\n", 1) +
		strings.Repeat("filler\n", busyTailLines) + "› "
	_, _, err = AgentCodex.LatestReply("/unused", quoted)
	require.NoError(t, err, "a marker scrolled above the status area does not count")

}

func TestRouteUsesLastTag(t *testing.T) {
	to := Route(AgentClaude, "example @codex stale\n@codex\nreview auth.go")
	require.Equal(t, Addressing{Agents: []Agent{AgentCodex}, Message: "review auth.go"}, to)
}

func TestRouteGroupsAdjacentTags(t *testing.T) {
	to := Route(AgentCodex, "@Gemini、@claude\ncompare your approaches")
	require.Equal(t, []Agent{AgentClaude, AgentAntigravity}, to.Agents, "ordered by agent, not by mention")
	require.Equal(t, "compare your approaches", to.Message)

	to = Route(AgentCodex, "@claude と @agy レビューして")
	require.Equal(t, []Agent{AgentClaude, AgentAntigravity}, to.Agents)
}

func TestRouteAll(t *testing.T) {
	to := Route(AgentAntigravity, "@All これまでの流れを確認して")
	require.True(t, to.All)
	require.Empty(t, to.Agents)
	require.True(t, to.Tagged())
	require.Equal(t, "これまでの流れを確認して", to.Message)

	to = Route(AgentClaude, "@codex @all check")
	require.True(t, to.All)
	require.Equal(t, []Agent{AgentCodex}, to.Agents)

	to = Route(AgentClaude, "mentioned @all earlier.\n@codex check")
	require.False(t, to.All, "a separated @all is quoted, not addressed")
}

func TestRouteIgnoresEarlierSeparatedTags(t *testing.T) {
	to := Route(AgentClaude, "I asked @codex earlier and it agreed.\n@gemini\nplease double-check")
	require.Equal(t, []Agent{AgentAntigravity}, to.Agents)
	require.Equal(t, "please double-check", to.Message)
}

func TestRouteIgnoresOwnTagAndUntagged(t *testing.T) {
	to := Route(AgentAntigravity, "as @gemini I think this is fine")
	require.False(t, to.Tagged())
	require.Empty(t, to.Message)
	require.False(t, Route(AgentCodex, "no tag here").Tagged())
}

func TestFormatAnswers(t *testing.T) {
	got := FormatAnswers("which container? ", true, []Answer{
		{From: Sender{Agent: AgentClaude, WorktreePath: "/wt/claude", TranscriptPath: "/cfg/relay/claude.md"}, Text: "use a map\n"},
		{From: Sender{Agent: AgentAntigravity, WorktreePath: "/wt/agy"}, Text: "use a slice"},
	})
	require.Equal(t,
		"[Answers from Claude Code, Gemini]\n"+
			"Question (asked by the user): which container?\n"+
			"\n=== Claude Code's answer ===\n"+
			"Claude Code worktree (read only for you): /wt/claude\n"+
			"Claude Code full session transcript: /cfg/relay/claude.md\n"+
			"\nuse a map\n"+
			"\n=== Gemini's answer ===\n"+
			"Gemini worktree (read only for you): /wt/agy\n"+
			"\nuse a slice",
		got)

	require.Contains(t, FormatAnswers("q", false, []Answer{{From: Sender{Agent: AgentCodex}, Text: "a"}}),
		"Question (asked by you): q\n")
}

func TestLaunchArgs(t *testing.T) {
	require.Equal(t, []string{"--add-dir", "/cfg"}, AgentClaude.LaunchArgs("/cfg"))
	require.Nil(t, AgentCodex.LaunchArgs("/cfg"))
	require.Nil(t, AgentAntigravity.LaunchArgs("/cfg"), "--add-dir would move agy's command cwd")
	require.Nil(t, AgentUnknown.LaunchArgs("/cfg"))
}

func TestRepliesFromLog(t *testing.T) {
	require.True(t, AgentAntigravity.RepliesFromLog())
	require.False(t, AgentClaude.RepliesFromLog())
	require.False(t, AgentCodex.RepliesFromLog())
	require.False(t, AgentUnknown.RepliesFromLog())
}

func TestTags(t *testing.T) {
	require.Equal(t, "@claude", AgentClaude.Tag())
	require.Equal(t, "@gemini", AgentAntigravity.Tag())
	require.Empty(t, AgentUnknown.Tag())
}

func TestLeading(t *testing.T) {
	to := Leading(AgentCodex, "@Gemini @claude あなたたちはどう思う？")
	require.Equal(t, Addressing{Agents: []Agent{AgentClaude, AgentAntigravity}, Message: "あなたたちはどう思う？"}, to)

	to = Leading(AgentCodex, "  @codex、@all 確認して")
	require.True(t, to.All, "the source's own tag is skipped, not a stop")
	require.Empty(t, to.Agents)
	require.Equal(t, "確認して", to.Message)

	to = Leading(AgentClaude, "@codex what did @gemini say?")
	require.Equal(t, []Agent{AgentCodex}, to.Agents, "only leading tags count, unlike Route")
	require.Equal(t, "what did @gemini say?", to.Message)

	require.False(t, Leading(AgentClaude, "fix the @codex parser").Tagged(), "a tag mid-text is an ordinary prompt")
	require.False(t, Leading(AgentClaude, "@allison said hi").Tagged(), "tags must be whole words")
	require.False(t, Leading(AgentClaude, "@codex-squad").Tagged())
	require.False(t, Leading(AgentCodex, "@codex hello").Tagged(), "only the source's own tag")
	require.Equal(t, Addressing{Agents: []Agent{AgentCodex}}, Leading(AgentClaude, "@codex"))
}

func TestInputText(t *testing.T) {
	claude := "● answer\n─────────\n❯ @codex テストです\n─────────\n  ⏵⏵ auto mode on"
	got, ok := AgentClaude.InputText(claude)
	require.True(t, ok)
	require.Equal(t, "@codex テストです", got)

	codex := "› earlier prompt\n• reply\n› @claude first line\n  second line\n\n  GPT-6.1-Sol default · ~/repo\n"
	got, ok = AgentCodex.InputText(codex)
	require.True(t, ok)
	require.Equal(t, "@claude first line\nsecond line", got, "continuation lines join; the blank line ends the input")

	agy := "> old\n  reply\n──────────\n> @all 確認して\n──────────\n? for shortcuts"
	got, ok = AgentAntigravity.InputText(agy)
	require.True(t, ok)
	require.Equal(t, "@all 確認して", got)

	_, ok = AgentCodex.InputText("no prompt here")
	require.False(t, ok)
	_, ok = AgentUnknown.InputText("› text")
	require.False(t, ok)
}

func TestQuoteTruncatesLongText(t *testing.T) {
	long := strings.Repeat("あ", quoteLimit+10)
	got := quote(long)
	require.True(t, strings.HasSuffix(got, " …(truncated; see the transcript)"))
	require.Equal(t, quoteLimit, len([]rune(strings.TrimSuffix(got, " …(truncated; see the transcript)"))))
	require.Equal(t, "short", quote("  short \n"))
}

func TestQuestionFormat(t *testing.T) {
	from := Sender{Agent: AgentClaude, WorktreePath: "/wt/claude", TranscriptPath: "/cfg/relay/claude.md",
		Latest: &Exchange{User: "日本最強の動物は？", Assistant: "ヒグマです。"}}
	q := Question{From: from, FromUser: true, To: AgentCodex, ToWorktree: "/wt/codex",
		AlsoAsked: []Agent{AgentAntigravity}, Text: " お前らどう思う？ "}
	require.Equal(t,
		"[Question from the user, asked in the Claude Code session]\n"+
			"You are Codex, consulted in a fresh session. Your own worktree: /wt/codex\n"+
			"Claude Code worktree (read only for you): /wt/claude\n"+
			"Claude Code full session transcript: /cfg/relay/claude.md\n"+
			"--- Latest exchange in the Claude Code session ---\n"+
			"User: 日本最強の動物は？\n"+
			"Claude Code: ヒグマです。\n---\n"+
			"Also asked: Gemini.\n"+
			"Answer directly; your reply is passed back to Claude Code.\n"+
			"\nお前らどう思う？",
		q.Format())

	q = Question{From: Sender{Agent: AgentCodex, WorktreePath: "/wt/codex"}, To: AgentClaude, ToWorktree: "/wt/claude", Text: "review"}
	require.Equal(t,
		"[Message from Codex]\n"+
			"You are Claude Code, consulted in a fresh session. Your own worktree: /wt/claude\n"+
			"Codex worktree (read only for you): /wt/codex\n"+
			"Answer directly; your reply is passed back to Codex.\n"+
			"\nreview",
		q.Format(), "an agent's message without a session history or other recipients")
}
