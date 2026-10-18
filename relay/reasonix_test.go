package relay

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Event lines as Reasonix 2.35 writes them to a session's .wire.jsonl, cut
// down to the fields that matter.
const (
	rxStarted     = `{"kind":"turn_started","text":"review auth.go","authoredTurn":1,"seq":1}`
	rxAttempt     = `{"kind":"stream_attempt","source":"executor","streamAttempt":{"id":"sa-1-1","action":"begin"},"seq":2}`
	rxPreamble    = `{"kind":"message","text":"Let me read it.","reasoning":"look first","source":"executor","seq":3}`
	rxToolCall    = `{"kind":"tool_dispatch","tool":{"id":"call_1","name":"read_file","args":"{\"path\": \"auth.go\"}"},"seq":4}`
	rxToolResult  = `{"kind":"tool_result","tool":{"id":"call_1","name":"read_file","output":"1→package auth"},"seq":5}`
	rxThinking    = `{"kind":"message","reasoning":"only thinking, no text","source":"executor","seq":6}`
	rxReply       = `{"kind":"message","text":"Missing a nil check.","reasoning":"found it","source":"executor","seq":7}`
	rxUsage       = `{"kind":"usage","usage":{"promptTokens":7930,"completionTokens":37},"seq":8}`
	rxDone        = `{"kind":"turn_done","checkpointTurn":0,"seq":9}`
	rxCancelled   = `{"kind":"turn_done","err":"context canceled","cancelled":true,"checkpointTurn":1,"seq":9}`
	rxNextStarted = `{"kind":"turn_started","text":"and the tests?","authoredTurn":2,"seq":10}`
)

func rxLog(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func TestFindReasonixLogPicksTheNewestWireLog(t *testing.T) {
	root := t.TempDir()
	cwd := "/wt/deep seek [1]"
	dir := filepath.Join(root, "projects", "-wt-deep seek [1]", "sessions")
	now := time.Now()
	writeLog(t, filepath.Join(dir, "20261018-010000-deepseek-flash.wire.jsonl"), rxLog(rxStarted), now.Add(-time.Hour))
	writeLog(t, filepath.Join(dir, "20261018-020000-deepseek-flash.wire.jsonl"), rxLog(rxStarted), now)
	writeLog(t, filepath.Join(dir, "20261018-020000-deepseek-flash.jsonl"), "{}", now.Add(time.Minute))
	writeLog(t, filepath.Join(dir, "20261018-020000-deepseek-flash.events.jsonl"), "{}", now.Add(time.Minute))

	got, err := findReasonixLogIn(root, cwd)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "20261018-020000-deepseek-flash.wire.jsonl"), got,
		"the event log, found although the path holds glob characters")

	_, err = findReasonixLogIn(root, "/wt/other")
	require.ErrorContains(t, err, "no Reasonix session log")
}

func TestParseReasonixLogKeepsTheConversation(t *testing.T) {
	turns, err := parseReasonixLog(strings.NewReader(rxLog(
		rxStarted, rxAttempt, rxPreamble, rxToolCall, rxToolResult, rxThinking, rxReply, rxUsage, rxDone, rxNextStarted)))
	require.NoError(t, err)
	require.Equal(t, []turn{
		{role: roleUser, text: "review auth.go"},
		{role: roleAssistant, text: "Let me read it.\n\nMissing a nil check."},
		{role: roleUser, text: "and the tests?"},
	}, turns)

	_, err = parseReasonixLog(strings.NewReader(rxLog(rxAttempt, rxUsage)))
	require.Error(t, err, "no conversation")
}

func TestReasonixUserTextUnwrapsPastes(t *testing.T) {
	pasted := "[Pasted text #1 · 2 lines]\n\n--- Begin [Pasted text #1 · 2 lines] ---\n[Codexからの相談]\nreview auth.go\n--- End [Pasted text #1 · 2 lines] ---"
	require.Equal(t, "[Codexからの相談]\nreview auth.go", reasonixUserText(pasted))

	mixed := "see [Pasted text #1 · 2 lines] and [Pasted text #2 · 1 lines]\n\n" +
		"--- Begin [Pasted text #1 · 2 lines] ---\na\nb\n--- End [Pasted text #1 · 2 lines] ---\n\n" +
		"--- Begin [Pasted text #2 · 1 lines] ---\nc\n--- End [Pasted text #2 · 1 lines] ---"
	require.Equal(t, "see a\nb and c", reasonixUserText(mixed))

	require.Equal(t, "typed [Pasted text #9] by hand", reasonixUserText("typed [Pasted text #9] by hand"), "no block, nothing to unwrap")
}

func TestLatestReasonixReply(t *testing.T) {
	for name, tc := range map[string]struct {
		log   string
		reply string
		busy  bool
	}{
		"finished":                {rxLog(rxStarted, rxPreamble, rxToolCall, rxToolResult, rxThinking, rxReply, rxUsage, rxDone), "Missing a nil check.", false},
		"answering":               {rxLog(rxStarted, rxPreamble, rxToolCall, rxToolResult), "", true},
		"waiting on its last say": {rxLog(rxStarted, rxReply), "", true},
		"cancelled":               {rxLog(rxStarted, rxPreamble, rxCancelled), "", false},
		"no text at all":          {rxLog(rxStarted, rxThinking, rxDone), "", false},
		"next prompt sent":        {rxLog(rxStarted, rxReply, rxDone, rxNextStarted), "", true},
		"empty":                   {"", "", false},
		"line being written":      {rxLog(rxStarted, rxReply, rxDone) + `{"kind":"turn_sta`, "Missing a nil check.", false},
	} {
		t.Run(name, func(t *testing.T) {
			reply, key, err := latestReasonixReply(strings.NewReader(tc.log))
			if tc.reply == "" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.reply, reply)
				require.Equal(t, "9", key, "the turn's end identifies the reply")
			}
			require.Equal(t, tc.busy, reasonixEventsBusy(strings.NewReader(tc.log)))
		})
	}
}

func TestDeepSeekTranscriptAndLatestReplyFromReasonixHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	log := filepath.Join(home, "projects", "-wt-deepseek", "sessions", "20261018-020000-deepseek-flash.wire.jsonl")
	writeLog(t, log, rxLog(rxStarted, rxPreamble, rxToolCall, rxToolResult, rxReply, rxDone), time.Now())

	transcript, err := AgentDeepSeek.Transcript("/wt/deepseek")
	require.NoError(t, err)
	require.Contains(t, transcript, "# DeepSeek session transcript")
	require.Contains(t, transcript, "\n## User\n\nreview auth.go\n\n## DeepSeek\n\nLet me read it.\n\nMissing a nil check.\n")

	reply, id, err := AgentDeepSeek.LatestReply("/wt/deepseek", "  Missing a nil check.\n ❯ ")
	require.NoError(t, err)
	require.Equal(t, "Missing a nil check.", reply)
	require.NotEqual(t, [32]byte{}, id)
	require.False(t, AgentDeepSeek.Busy("/wt/deepseek", ""))

	writeLog(t, log, rxLog(rxStarted, rxReply, rxDone, rxNextStarted), time.Now())
	require.True(t, AgentDeepSeek.Busy("/wt/deepseek", ""), "the log shows the next prompt in progress")
	_, _, err = AgentDeepSeek.LatestReply("/wt/deepseek", "")
	require.Error(t, err)

	require.False(t, AgentDeepSeek.Busy("/wt/never-started", ""), "no log means idle")
}

func TestDeepSeekAgent(t *testing.T) {
	require.Equal(t, AgentDeepSeek, DetectAgent("/home/user/.local/bin/reasonix"))
	require.Equal(t, AgentDeepSeek, AgentByName("deepseek"))
	require.Equal(t, SlotTopRight, AgentDeepSeek.Slot())
	require.Equal(t, "REASONIX.local.md", AgentDeepSeek.BriefingFile())
	require.Empty(t, AgentDeepSeek.LaunchArgs(LaunchEnv{SharedDir: "/cfg", Self: "/bin/cs", Briefing: "hi"}),
		"no --add-dir, which would make peers' worktrees writable; the briefing goes in a file")

	to := Route(AgentClaude, "@deepseek review auth.go")
	require.Equal(t, Addressing{Agents: []Agent{AgentDeepSeek}, Message: "review auth.go"}, to)
	to = Route(AgentDeepSeek, "@Reasonix、@codex compare")
	require.Equal(t, []Agent{AgentCodex}, to.Agents, "an agent never asks itself")

	pane := "  Missing a nil check.\n───────\n ❯ @codex 1行目\n   2行目\n───────\n   Ask  · ready\n"
	got, ok := AgentDeepSeek.InputText(pane)
	require.True(t, ok)
	require.Equal(t, "@codex 1行目\n2行目", got)
}
