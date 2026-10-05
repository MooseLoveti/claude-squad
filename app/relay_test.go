package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"claude-squad/relay"
	"claude-squad/session"
	"claude-squad/ui"

	"github.com/stretchr/testify/require"
)

func TestAutoRelayStopsAtLimit(t *testing.T) {
	h := newRelayHome()
	h.relayMode = relayAuto

	for i := 1; i < autoRelayLimit; i++ {
		require.False(t, h.recordAutoRelay())
		require.Equal(t, relayAuto, h.relayMode)
		require.Equal(t, i, h.autoRelayCount)
	}

	require.True(t, h.recordAutoRelay())
	require.Equal(t, relayManual, h.relayMode)
	require.Equal(t, autoRelayLimit, h.autoRelayCount)
}

func newRelayHome() *home {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	return &home{menu: ui.NewMenu(), list: ui.NewList(&sp, false), errBox: ui.NewErrBox()}
}

func newRelayInstance(t *testing.T, title, program string) *session.Instance {
	t.Helper()
	instance, err := session.NewInstance(session.InstanceOptions{Title: title, Path: t.TempDir(), Program: program})
	require.NoError(t, err)
	return instance
}

func TestPickDestinations(t *testing.T) {
	claude := newRelayInstance(t, "claude", "claude")
	codex := newRelayInstance(t, "codex", "codex --search")
	agy := newRelayInstance(t, "gemini", "agy")
	to := func(agents ...relay.Agent) relay.Addressing { return relay.Addressing{Agents: agents} }

	got, err := pickDestinations([]*session.Instance{codex, agy}, to(relay.AgentAntigravity))
	require.NoError(t, err)
	require.Equal(t, []*session.Instance{agy}, got)

	got, err = pickDestinations([]*session.Instance{claude, agy}, to(relay.AgentClaude, relay.AgentAntigravity))
	require.NoError(t, err)
	require.Equal(t, []*session.Instance{claude, agy}, got)

	got, err = pickDestinations([]*session.Instance{codex}, to())
	require.NoError(t, err, "an untagged reply goes to the only other agent")
	require.Equal(t, []*session.Instance{codex}, got)

	_, err = pickDestinations([]*session.Instance{codex, agy}, to())
	require.Error(t, err, "an untagged reply is ambiguous with two other agents")

	_, err = pickDestinations([]*session.Instance{codex}, to(relay.AgentCodex, relay.AgentAntigravity))
	require.Error(t, err, "every tagged agent must be running")

	other := newRelayInstance(t, "codex-2", "codex")
	_, err = pickDestinations([]*session.Instance{codex, other}, to(relay.AgentCodex))
	require.Error(t, err, "two sessions of the tagged agent are ambiguous")

	got, err = pickDestinations([]*session.Instance{claude, codex, agy}, relay.Addressing{All: true})
	require.NoError(t, err)
	require.Equal(t, []*session.Instance{claude, codex, agy}, got, "@all reaches every other running agent")
	_, err = pickDestinations(nil, relay.Addressing{All: true})
	require.Error(t, err)
}

func newGatherHome(t *testing.T) (*home, *relayGather, *session.Instance, *session.Instance) {
	t.Helper()
	asker := newRelayInstance(t, "codex", "codex")
	claude := newRelayInstance(t, "claude", "claude")
	agy := newRelayInstance(t, "gemini", "agy")
	gather := newRelayGather(asker, "which container?", true, []*session.Instance{claude, agy})
	h := newRelayHome()
	h.pendingAsks = []*relayGather{gather}
	return h, gather, claude, agy
}

func answerFrom(gather *relayGather, recipient *session.Instance, text string) consultAnswer {
	agent := relay.DetectAgent(recipient.Program)
	return consultAnswer{gather: gather, recipient: recipient, answer: relay.Answer{From: relay.Sender{Agent: agent}, Text: text}}
}

func TestUserQuestionAnswersStayOutOfTheAskingAgent(t *testing.T) {
	h, gather, claude, agy := newGatherHome(t)

	require.NoError(t, h.recordAnswer(answerFrom(gather, agy, "use a slice")), "Claude still owes an answer")
	require.Nil(t, h.lastAnswers)
	require.Len(t, h.pendingAsks, 1)

	// The asker is not running in this test, so typing anything into it would
	// fail; no error means nothing was sent to it.
	require.NoError(t, h.recordAnswer(answerFrom(gather, claude, "use a map")))
	require.Empty(t, h.pendingAsks)
	require.True(t, h.answersUnseen, "shown on the main screen instead")
	require.Equal(t, "Answers to your question from codex", h.lastAnswers.title)
	require.Equal(t, "Question: which container?\n\n■ Claude Code\nuse a map\n\n■ Gemini\nuse a slice", h.lastAnswers.text,
		"in ask order")
}

func TestAgentQuestionAnswersGoBackToTheAgent(t *testing.T) {
	h, gather, claude, agy := newGatherHome(t)
	gather.fromUser = false
	_ = h.recordAnswer(answerFrom(gather, agy, "slice"))
	require.ErrorContains(t, h.recordAnswer(answerFrom(gather, claude, "map")), "return answers",
		"the agent that asked gets the answers typed in (it is not running in this test)")
	require.False(t, h.answersUnseen, "they do not pop up for the user")
	require.Equal(t, "Answers to codex's question (also sent to it)", h.lastAnswers.title, "v still shows them")
}

func TestCollectAnswersDrainsFinishedConsultations(t *testing.T) {
	h, gather, claude, agy := newGatherHome(t)
	h.consultAnswers = make(chan consultAnswer, 4)
	h.consultAnswers <- answerFrom(gather, claude, "map")
	h.consultAnswers <- answerFrom(gather, agy, "slice")

	require.Empty(t, h.collectAnswers())
	require.Empty(t, h.pendingAsks)
	require.True(t, h.answersUnseen)
	require.Empty(t, h.collectAnswers(), "nothing left to drain")
}

func TestAskConsultsEachAgentAndGathersTheAnswers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	codex := filepath.Join(bin, "codex")
	// A stand-in for codex exec: write the answer to the file after -o.
	script := `#!/bin/sh
out=""; prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
cat > /dev/null
echo "fresh answer" > "$out"
`
	require.NoError(t, os.WriteFile(codex, []byte(script), 0o755))

	h := newRelayHome()
	h.ctx = context.Background()
	asker := newRelayInstance(t, "claude", "claude")
	recipient := newRelayInstance(t, "codex", codex)
	require.NoError(t, h.ask(asker, relay.AgentClaude, []*session.Instance{recipient}, "what changed?", true))
	require.Len(t, h.pendingAsks, 1)
	require.Equal(t, "Q: what changed? → Codex …", askNote(h.pendingAsks[0]))

	deadline := time.Now().Add(10 * time.Second)
	for len(h.pendingAsks) > 0 && time.Now().Before(deadline) {
		h.collectAnswers()
		time.Sleep(20 * time.Millisecond)
	}
	require.Empty(t, h.pendingAsks, "the consultation finished")
	require.Contains(t, h.lastAnswers.text, "■ Codex\nfresh answer")
}

func TestFailedConsultationStillCompletesTheGather(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newRelayHome()
	h.ctx = context.Background()
	asker := newRelayInstance(t, "claude", "claude")
	broken := newRelayInstance(t, "codex", filepath.Join(t.TempDir(), "codex"))
	require.NoError(t, h.ask(asker, relay.AgentClaude, []*session.Instance{broken}, "q", true))

	deadline := time.Now().Add(10 * time.Second)
	for len(h.pendingAsks) > 0 && time.Now().Before(deadline) {
		h.collectAnswers()
		time.Sleep(20 * time.Millisecond)
	}
	require.Empty(t, h.pendingAsks)
	require.Contains(t, h.lastAnswers.text, "■ Codex\n(no answer: ", "the failure is reported as the answer")
}

func TestUntaggedAutoReplyIsNotRelayed(t *testing.T) {
	h := newRelayHome()
	sent, err := h.deliverReply(newRelayInstance(t, "codex", "codex"), relay.AgentCodex, "just thinking aloud", false)
	require.NoError(t, err)
	require.False(t, sent)

	_, err = h.deliverReply(newRelayInstance(t, "codex", "codex"), relay.AgentCodex, "", true)
	require.Error(t, err)
}

func TestSubmitPrompt(t *testing.T) {
	h := newRelayHome()
	agy := newRelayInstance(t, "gemini", "agy")

	// None of these instances runs, so a prompt that reaches SendPrompt fails
	// with "not started"; that error shows where the prompt went.
	require.ErrorContains(t, h.submitPrompt(agy, "テスト"), "not started", "untagged text goes to the selected session")
	require.ErrorContains(t, h.submitPrompt(newRelayInstance(t, "sh", "bash"), "@codex hi"), "not started",
		"a non-agent session receives tags as plain text")
	require.ErrorContains(t, h.submitPrompt(agy, "@codex"), "write a question")
	require.ErrorContains(t, h.submitPrompt(agy, "@all これまでの流れを確認して"), "no other running agents")
	require.ErrorContains(t, h.submitPrompt(agy, "@codex これまでの流れを確認して"), "exactly one running Codex")
	require.ErrorContains(t, h.submitPrompt(agy, "@codex what did @claude say?"), "running Codex",
		"leading tags pick the recipients, not tags quoted later")
	require.ErrorContains(t, h.submitPrompt(agy, "これについて @claude どう思う"), "running Claude Code",
		"without leading tags the last tag still addresses")
}

func TestCompletedAnswersShowWhenIdle(t *testing.T) {
	h, gather, claude, agy := newGatherHome(t)
	h.windowWidth, h.windowHeight = 100, 40

	_ = h.recordAnswer(answerFrom(gather, agy, "use a slice"))
	h.state = statePrompt
	_ = h.recordAnswer(answerFrom(gather, claude, "use a map"))
	require.True(t, h.answersUnseen)

	h.openAnswersIfIdle()
	require.Equal(t, statePrompt, h.state, "never pops up over a prompt the user is typing")

	h.state = stateDefault
	h.openAnswersIfIdle()
	require.Equal(t, stateAnswers, h.state)
	require.False(t, h.answersUnseen)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, stateAnswers, h.state, "scrolling keeps the answers open")
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.Equal(t, stateDefault, h.state)
	require.Nil(t, h.answersOverlay)

	h.openAnswersIfIdle()
	require.Equal(t, stateDefault, h.state, "seen answers do not reopen by themselves")
}

func TestAnswersWhileAttachedOpenAPopup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h, gather, claude, agy := newGatherHome(t)
	h.attached = newRelayInstance(t, "gemini-attached", "agy")
	_ = h.recordAnswer(answerFrom(gather, agy, "slice"))
	err := h.recordAnswer(answerFrom(gather, claude, "map"))
	require.ErrorContains(t, err, "show answers over", "the popup goes to the attached session (not running in this test)")
	require.True(t, h.answersUnseen, "when the popup cannot open, the main screen shows the answers after detaching")

	written, readErr := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude-squad", "relay", "answers.txt"))
	require.NoError(t, readErr)
	require.Contains(t, string(written), "Answers to your question from codex   (q: close")
	require.Contains(t, string(written), "■ Claude Code\nmap")
}

func TestRelayNote(t *testing.T) {
	h, gather, _, agy := newGatherHome(t)
	require.Equal(t, "Waiting for answers: Claude Code … · Gemini …", h.relayNote())

	gather.answers[agy] = relay.Answer{Text: "done"}
	require.Equal(t, "Waiting for answers: Claude Code … · Gemini ✓", h.relayNote())

	h.pendingAsks = nil
	require.Empty(t, h.relayNote())
	h.lastAnswers = &shownAnswers{}
	require.Equal(t, "v: last answers", h.relayNote())
}

func TestAskNote(t *testing.T) {
	_, gather, claude, _ := newGatherHome(t)
	require.Equal(t, "Q: which container? → Claude Code … · Gemini …", askNote(gather))

	gather.answers[claude] = relay.Answer{Text: "map"}
	require.Equal(t, "Q: which container? → Claude Code ✓ · Gemini …", askNote(gather))

	gather.question = "これは\nとても長い質問なので、ステータスバーに収まるように途中で切られるはずです"
	require.Equal(t, "Q: これは とても長い質問なので、ステータスバーに収まるように途… → Claude Code ✓ · Gemini …",
		askNote(gather), "line breaks fold into spaces and long questions are cut")
}
