package panel

import (
	"claude-squad/log"
	"claude-squad/relay"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	log.Initialize("[test] ")
	defer log.Close()
	os.Exit(m.Run())
}

// fakeTerminal stands in for the dashboard: tests set what each pane shows
// and read back what the relay typed.
type fakeTerminal struct {
	screens   map[string]string
	pasted    map[string][]string
	keys      map[string][]string
	backspace map[string]int
	notes     map[string]string
	pasteErr  error
}

func newFakeTerminal() *fakeTerminal {
	return &fakeTerminal{
		screens:   map[string]string{},
		pasted:    map[string][]string{},
		keys:      map[string][]string{},
		backspace: map[string]int{},
		notes:     map[string]string{},
	}
}

func (f *fakeTerminal) Capture(pane string) (string, error) {
	screen, ok := f.screens[pane]
	if !ok {
		return "", errors.New("no such pane")
	}
	return screen, nil
}

func (f *fakeTerminal) CaptureHistory(pane string) (string, error) {
	return f.Capture(pane)
}

func (f *fakeTerminal) Paste(pane, text string) error {
	if f.pasteErr != nil {
		return f.pasteErr
	}
	f.pasted[pane] = append(f.pasted[pane], text)
	return nil
}

func (f *fakeTerminal) SendKeys(pane string, keys ...string) error {
	f.keys[pane] = append(f.keys[pane], keys...)
	return nil
}

func (f *fakeTerminal) Backspace(pane string, n int) error {
	f.backspace[pane] += n
	return nil
}

func (f *fakeTerminal) SetNote(pane, note string) error {
	f.notes[pane] = note
	return nil
}

const (
	claudePane = "%1"
	codexPane  = "%2"
	geminiPane = "%3"
)

// Screens as each CLI draws them: a finished reply, then the input box.
func claudeScreen(reply, input string) string {
	return "⏺ " + reply + "\n\n❯ " + input + "\n"
}

func claudeBusy() string {
	return "⏺ earlier\n\n✻ Thinking… (esc to interrupt)\n"
}

func codexScreen(replies ...string) string {
	var b strings.Builder
	for _, r := range replies {
		b.WriteString("• " + r + "\n\n")
	}
	return b.String() + "› \n"
}

func codexBusy() string {
	return "• earlier\n\n• Working (3s • esc to interrupt)\n"
}

// isolateAgentHomes keeps the relay from reading this machine's real agent
// session logs.
func isolateAgentHomes(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	return home
}

// writeAgyLog makes agy's conversation index point dir at a log whose last
// step is a reply when done, or the user's prompt when not.
func writeAgyLog(t *testing.T, home, dir, reply string, step int, done bool) {
	t.Helper()
	root := filepath.Join(home, ".gemini", "antigravity-cli")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "cache"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "cache", "last_conversations.json"),
		[]byte(fmt.Sprintf(`{%q:"conv"}`, dir)), 0o644))
	logDir := filepath.Join(root, "brain", "conv", ".system_generated", "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	lines := fmt.Sprintf(`{"step_index":%d,"type":"USER_INPUT","status":"DONE","content":"q"}`+"\n", step)
	if done {
		lines += fmt.Sprintf(`{"step_index":%d,"type":"PLANNER_RESPONSE","status":"DONE","content":%q,"tool_calls":[]}`+"\n", step+1, reply)
	}
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(lines), 0o644))
}

type relayFixture struct {
	t     *testing.T
	term  *fakeTerminal
	relay *Relay
	home  string
	now   time.Time
}

func newRelayFixture(t *testing.T, members ...Member) *relayFixture {
	home := isolateAgentHomes(t)
	term := newFakeTerminal()
	r := NewRelay(term, members, t.TempDir())
	r.settle = 0
	f := &relayFixture{t: t, term: term, relay: r, home: home, now: time.Date(2026, 10, 5, 18, 17, 0, 0, time.UTC)}
	r.now = func() time.Time { return f.now }
	return f
}

var (
	claudeMember = Member{Agent: relay.AgentClaude, Pane: claudePane, Dir: "/wt/claude"}
	codexMember  = Member{Agent: relay.AgentCodex, Pane: codexPane, Dir: "/wt/codex"}
	geminiMember = Member{Agent: relay.AgentAntigravity, Pane: geminiPane, Dir: "/wt/gemini"}
)

func TestUntaggedEnterPasses(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.term.screens[claudePane] = claudeScreen("hi", "ordinary prompt mentioning @codex")
	require.False(t, f.relay.HandleEnter(claudePane), "a tag only counts at the start")
	require.False(t, f.relay.HandleEnter("%9"), "panes without an agent pass")
	f.term.screens[claudePane] = "no input box drawn"
	require.False(t, f.relay.HandleEnter(claudePane))
	require.Zero(t, f.term.backspace[claudePane])
	require.Empty(t, f.relay.Status().Log)
}

func TestTaggedQuestionGoesToTheRecipientAndTheAnswerComesBack(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.term.screens[codexPane] = codexScreen("old reply")
	f.term.screens[claudePane] = claudeScreen("hi", "@codex ここどう思う？")

	require.True(t, f.relay.HandleEnter(claudePane))
	require.Equal(t, len([]rune("@codex ここどう思う？"))+2, f.term.backspace[claudePane], "the typed question is cleared")
	require.Contains(t, f.term.notes[claudePane], "Q: ここどう思う？ → Codex …")
	require.Empty(t, f.term.pasted[codexPane], "nothing is typed until the next poll")

	f.term.screens[claudePane] = claudeScreen("hi", "")
	f.relay.Step()
	require.Len(t, f.term.pasted[codexPane], 1)
	prompt := f.term.pasted[codexPane][0]
	require.True(t, strings.HasPrefix(prompt, "[Claude Codeからの相談（ユーザーの質問）]\n"), prompt)
	require.Contains(t, prompt, "Claude Code の作業ディレクトリ（あなたは読み取りのみ）: /wt/claude\n")
	require.True(t, strings.HasSuffix(prompt, "\nここどう思う？"))
	require.Equal(t, []string{"Enter"}, f.term.keys[codexPane])
	require.Equal(t, "← Claude Code の相談に回答中", f.term.notes[codexPane])

	// The old reply is still on screen, then Codex works on the question.
	f.relay.Step()
	f.term.screens[codexPane] = codexBusy()
	f.relay.Step()
	require.Empty(t, f.term.pasted[claudePane])

	// A new reply counts once a second poll still shows it.
	f.term.screens[codexPane] = codexScreen("old reply", "新しい意見です")
	f.relay.Step()
	require.Empty(t, f.term.pasted[claudePane])
	f.relay.Step()
	require.Equal(t, []string{"[ユーザーの質問（@codex 宛て）]\nここどう思う？\n\n[Codexの意見]\n（Codex の作業ディレクトリ: /wt/codex）\n新しい意見です"},
		f.term.pasted[claudePane])
	require.Equal(t, []string{"Enter"}, f.term.keys[claudePane])
	require.Empty(t, f.term.notes[claudePane], "the note clears once answered")
	require.Empty(t, f.term.notes[codexPane])

	var texts []string
	for _, e := range f.relay.Status().Log {
		texts = append(texts, e.Text)
	}
	require.Equal(t, []string{"Claude Code → Codex: ここどう思う？", "Codex が回答しました", "Claude Code に回答を返しました"}, texts)
	logFile, err := os.ReadFile(f.relay.LogFile())
	require.NoError(t, err)
	require.Contains(t, string(logFile), "=== Codex へ ===\n[Claude Codeからの相談", "the full prompt is logged")
}

func TestAllWaitsForEveryRecipient(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember, geminiMember)
	f.term.screens[codexPane] = codexScreen("old")
	f.term.screens[geminiPane] = "> \n"
	writeAgyLog(t, f.home, geminiMember.Dir, "old gemini", 0, true)
	f.term.screens[claudePane] = claudeScreen("hi", "@all 意見ください")

	require.True(t, f.relay.HandleEnter(claudePane))
	f.relay.Step()
	require.Len(t, f.term.pasted[codexPane], 1)
	require.Len(t, f.term.pasted[geminiPane], 1)
	require.Contains(t, f.term.pasted[codexPane][0], "Gemini にも同じ質問をしています。")
	require.Contains(t, f.term.pasted[geminiPane][0], "Codex にも同じ質問をしています。")

	writeAgyLog(t, f.home, geminiMember.Dir, "", 2, false)
	f.term.screens[codexPane] = codexScreen("old", "codex answer")
	f.relay.Step()
	f.relay.Step()
	require.Empty(t, f.term.pasted[claudePane], "Gemini has not answered yet")
	require.Contains(t, f.term.notes[claudePane], "Codex ✓ · Gemini …")

	writeAgyLog(t, f.home, geminiMember.Dir, "gemini answer", 2, true)
	f.relay.Step()
	f.relay.Step()
	require.Len(t, f.term.pasted[claudePane], 1)
	answer := f.term.pasted[claudePane][0]
	require.True(t, strings.HasPrefix(answer, "[ユーザーの質問（@codex @gemini 宛て）]\n意見ください\n"), answer)
	require.Contains(t, answer, "[Codexの意見]\n（Codex の作業ディレクトリ: /wt/codex）\ncodex answer")
	require.Contains(t, answer, "[Geminiの意見]\n（Gemini の作業ディレクトリ: /wt/gemini）\ngemini answer")
}

func TestQuestionWaitsWhileTheRecipientIsBusy(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.term.screens[codexPane] = codexBusy()
	f.term.screens[claudePane] = claudeScreen("hi", "@codex 質問")
	require.True(t, f.relay.HandleEnter(claudePane))
	f.relay.Step()
	f.relay.Step()
	require.Empty(t, f.term.pasted[codexPane])

	f.term.screens[codexPane] = codexScreen("finished its own work")
	f.relay.Step()
	require.Len(t, f.term.pasted[codexPane], 1)
}

func TestAnswersWaitWhileTheAskerIsBusy(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.term.screens[codexPane] = codexScreen("old")
	f.term.screens[claudePane] = claudeScreen("hi", "@codex 質問")
	require.True(t, f.relay.HandleEnter(claudePane))
	f.relay.Step()
	f.term.screens[claudePane] = claudeBusy()
	f.term.screens[codexPane] = codexScreen("old", "answer")
	f.relay.Step()
	f.relay.Step()
	require.Empty(t, f.term.pasted[claudePane])

	f.term.screens[claudePane] = claudeScreen("done", "")
	f.relay.Step()
	require.Len(t, f.term.pasted[claudePane], 1)
}

func TestUnansweredQuestionTimesOut(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.term.screens[codexPane] = codexScreen("old")
	f.term.screens[claudePane] = claudeScreen("hi", "@codex 質問")
	require.True(t, f.relay.HandleEnter(claudePane))
	f.relay.Step()
	f.now = f.now.Add(consultTimeout + time.Second)
	f.relay.Step()
	f.relay.Step()
	require.Len(t, f.term.pasted[claudePane], 1)
	require.Contains(t, f.term.pasted[claudePane][0], "[Codexの意見]\n（Codex の作業ディレクトリ: /wt/codex）\n（回答なし: 時間内に返答がありませんでした）")
}

func TestFailedPasteStillAnswers(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.term.screens[codexPane] = codexScreen("old")
	f.term.screens[claudePane] = claudeScreen("hi", "@codex 質問")
	require.True(t, f.relay.HandleEnter(claudePane))
	f.term.pasteErr = errors.New("pane is gone")
	f.relay.Step()
	f.term.pasteErr = nil
	f.relay.Step()
	require.Len(t, f.term.pasted[claudePane], 1)
	require.Contains(t, f.term.pasted[claudePane][0], "（回答なし: 送信に失敗しました: pane is gone）")
}

func TestTaggedInputThatCannotBeAskedStays(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.term.screens[claudePane] = claudeScreen("hi", "@gemini 質問")
	require.True(t, f.relay.HandleEnter(claudePane), "the Enter is kept from Claude")
	require.Zero(t, f.term.backspace[claudePane], "the input stays for the user to fix")
	require.Equal(t, "@gemini はこのワークスペースで動いていません", f.relay.Status().Log[0].Text)

	f.term.screens[claudePane] = claudeScreen("hi", "@codex")
	require.True(t, f.relay.HandleEnter(claudePane))
	require.Zero(t, f.term.backspace[claudePane])
	require.Equal(t, "@タグの後に質問がありません", f.relay.Status().Log[1].Text)

	f = newRelayFixture(t, claudeMember)
	f.term.screens[claudePane] = claudeScreen("hi", "@all 質問")
	require.True(t, f.relay.HandleEnter(claudePane))
	require.Equal(t, "@all で聞ける相手がいません", f.relay.Status().Log[0].Text)
}

func TestBroadcastTypesIntoEveryPane(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.relay.Broadcast("  みんなでレビューして  ")
	require.Equal(t, []string{"みんなでレビューして"}, f.term.pasted[claudePane])
	require.Equal(t, []string{"みんなでレビューして"}, f.term.pasted[codexPane])
	require.Equal(t, []string{"Enter"}, f.term.keys[codexPane])
	f.relay.Broadcast("   ")
	require.Len(t, f.term.pasted[claudePane], 1, "blank text is not sent")
}

func TestAutoRelayAsksWhatANewReplyTags(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.term.screens[codexPane] = codexScreen("old")
	f.term.screens[claudePane] = claudeScreen("a reply that says @codex before auto relay", "")
	f.relay.Step()
	require.Empty(t, f.term.pasted[codexPane], "auto relay is off")

	f.relay.SetAutoRelay(true)
	f.relay.Step()
	require.Empty(t, f.term.pasted[codexPane], "replies from before turning it on do not count")

	f.term.screens[claudePane] = claudeScreen("確認お願いします @codex このテストを見て", "")
	f.relay.Step()
	require.Empty(t, f.term.pasted[codexPane], "seen once")
	f.relay.Step()
	f.relay.Step()
	require.Len(t, f.term.pasted[codexPane], 1)
	require.True(t, strings.HasPrefix(f.term.pasted[codexPane][0], "[Claude Codeからの相談]\n"))
	require.True(t, strings.HasSuffix(f.term.pasted[codexPane][0], "\nこのテストを見て"))

	f.term.screens[codexPane] = codexScreen("old", "見ました @claude ここが変")
	f.relay.Step()
	f.relay.Step()
	require.Equal(t, []string{"[あなたの質問]\nこのテストを見て\n\n[Codexの意見]\n（Codex の作業ディレクトリ: /wt/codex）\n見ました @claude ここが変"},
		f.term.pasted[claudePane])
	f.relay.Step()
	f.relay.Step()
	require.Len(t, f.term.pasted[claudePane], 1, "an answer is not scanned for tags again")
	require.Equal(t, 1, f.relay.Status().AutoCount)
}

func TestAutoRelayStopsAtTheLimit(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember)
	f.relay.SetAutoRelay(true)
	f.term.screens[codexPane] = codexScreen("old")
	f.term.screens[claudePane] = claudeScreen("start", "")
	f.relay.Step()
	f.relay.autoCount = autoRelayLimit - 1
	f.term.screens[claudePane] = claudeScreen("@codex one more", "")
	f.relay.Step()
	f.relay.Step()
	status := f.relay.Status()
	require.False(t, status.AutoRelay)
	require.Equal(t, fmt.Sprintf("自動Relayを%d回で止めました", autoRelayLimit), status.Log[len(status.Log)-1].Text)

	f.relay.SetAutoRelay(true)
	require.Zero(t, f.relay.Status().AutoCount, "turning it on again starts a new count")
}

func TestSecondQuestionToTheSameAgentWaitsForTheFirst(t *testing.T) {
	f := newRelayFixture(t, claudeMember, codexMember, geminiMember)
	f.term.screens[codexPane] = codexScreen("old")
	f.term.screens[geminiPane] = "> \n"
	f.term.screens[claudePane] = claudeScreen("hi", "@codex first")
	require.True(t, f.relay.HandleEnter(claudePane))
	f.term.screens[geminiPane] = "> @codex second\n"
	require.True(t, f.relay.HandleEnter(geminiPane))
	f.relay.Step()
	require.Len(t, f.term.pasted[codexPane], 1)
	require.True(t, strings.HasSuffix(f.term.pasted[codexPane][0], "\nfirst"))

	f.term.screens[codexPane] = codexScreen("old", "first answer")
	f.relay.Step()
	f.relay.Step()
	require.Len(t, f.term.pasted[codexPane], 2, "the second question goes out once the first is answered")
	require.True(t, strings.HasSuffix(f.term.pasted[codexPane][1], "\nsecond"))
}

func TestHookRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.sock")
	require.False(t, AskHook(path, claudePane), "no panel listening")

	// A socket file left by a dead panel is replaced.
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	var asked []string
	stop, err := ServeHook(path, func(pane string) bool {
		asked = append(asked, pane)
		return pane == claudePane
	})
	require.NoError(t, err)
	require.True(t, AskHook(path, claudePane))
	require.False(t, AskHook(path, codexPane))
	require.Equal(t, []string{claudePane, codexPane}, asked)
	stop()
	require.False(t, AskHook(path, claudePane))
	require.NoFileExists(t, path)
}
