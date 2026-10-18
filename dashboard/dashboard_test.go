package dashboard

import (
	"claude-squad/cmd/cmd_test"
	"claude-squad/relay"
	"claude-squad/workspace"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// recorder fakes tmux: it records every command and answers -P (print pane
// id) and list-panes requests.
type recorder struct {
	calls   [][]string
	stdin   []string
	nextID  int
	failOn  string
	listOut string
}

func (r *recorder) exec() cmd_test.MockCmdExec {
	record := func(c *exec.Cmd) error {
		args := c.Args[3:] // drop "tmux -L claudesquad"
		r.calls = append(r.calls, args)
		if c.Stdin != nil {
			b, _ := io.ReadAll(c.Stdin)
			r.stdin = append(r.stdin, string(b))
		}
		if r.failOn != "" && args[0] == r.failOn {
			return errors.New("tmux failed")
		}
		return nil
	}
	return cmd_test.MockCmdExec{
		RunFunc: record,
		OutputFunc: func(c *exec.Cmd) ([]byte, error) {
			if err := record(c); err != nil {
				return nil, err
			}
			switch c.Args[3] {
			case "new-session", "split-window":
				r.nextID++
				return []byte("%" + string(rune('0'+r.nextID)) + "\n"), nil
			case "list-panes":
				return []byte(r.listOut), nil
			}
			return nil, nil
		},
	}
}

func (r *recorder) commands() []string {
	out := make([]string, len(r.calls))
	for i, c := range r.calls {
		out[i] = strings.Join(c, " ")
	}
	return out
}

func testPanes() []Pane {
	return []Pane{
		{Slot: relay.SlotBottomRight, Agent: "gemini", Title: "Gemini", Dir: "/wt/g", Command: "agy"},
		{Slot: relay.SlotTopRight, Title: PanelTitle, Dir: "/repo", Command: "'/bin/cs' panel"},
		{Slot: relay.SlotTopLeft, Agent: "codex", Title: "Codex", Dir: "/wt/x", Command: "codex"},
		{Slot: relay.SlotBottomLeft, Agent: "claude", Title: "Claude Code", Dir: "/wt/c", Command: "claude"},
	}
}

func TestCreateLaysOutPanesBySlot(t *testing.T) {
	rec := &recorder{}
	tmux := &Tmux{exec: rec.exec(), socket: Socket}
	require.NoError(t, tmux.Create(testPanes(), 200, 50, "/bin/cs"))

	cmds := rec.commands()
	// Panes are created top-left, top-right, bottom-left, bottom-right, which
	// is the order a tiled 2×2 layout places them in.
	require.Equal(t, "new-session -d -s squad -x 200 -y 50 -c /wt/x -P -F #{pane_id} codex", cmds[0])
	require.Equal(t, "select-layout -t squad tiled", cmds[1])
	require.Equal(t, "split-window -t squad -c /repo -P -F #{pane_id} '/bin/cs' panel", cmds[2])
	require.Equal(t, "split-window -t squad -c /wt/c -P -F #{pane_id} claude", cmds[4])
	require.Equal(t, "split-window -t squad -c /wt/g -P -F #{pane_id} agy", cmds[6])
	require.Contains(t, cmds, "set-option -p -t %1 @cs_agent codex")
	require.Contains(t, cmds, "set-option -p -t %2 @cs_title "+PanelTitle)
	require.NotContains(t, cmds, "set-option -p -t %2 @cs_agent ", "the panel is no agent")
	require.Contains(t, cmds, "set-option -p -t %3 @cs_agent claude")
	require.Contains(t, cmds, "set-option -p -t %4 @cs_agent gemini")
	require.Contains(t, cmds, `bind-key -n Enter if-shell -F #{@cs_agent} run-shell "'/bin/cs' hook enter #{pane_id}" send-keys Enter`)
	require.Contains(t, cmds, "set-option -g mouse on")
	require.Contains(t, cmds, "set-option -g pane-border-lines heavy")
	require.Contains(t, cmds, "bind-key -n S-Left select-pane -L")
	require.Contains(t, cmds, "bind-key -n S-Down select-pane -D")
	require.Equal(t, "select-pane -t %1", cmds[len(cmds)-1], "the first agent pane starts selected")
}

func TestCreateCleansUpWhenASplitFails(t *testing.T) {
	rec := &recorder{failOn: "split-window"}
	tmux := &Tmux{exec: rec.exec(), socket: Socket}
	require.Error(t, tmux.Create(testPanes(), 200, 50, "/bin/cs"))
	require.Equal(t, "kill-server", rec.commands()[len(rec.calls)-1])

	require.Error(t, (&Tmux{exec: (&recorder{}).exec()}).Create(nil, 1, 1, "cs"))
}

func TestPasteLoadsABufferAndPastesIt(t *testing.T) {
	rec := &recorder{}
	tmux := &Tmux{exec: rec.exec(), socket: Socket}
	require.NoError(t, tmux.Paste("%3", "line one\nline two"))
	require.Equal(t, []string{
		"if-shell -F -t %3 #{pane_in_mode} send-keys -X -t %3 cancel",
		"load-buffer -b cs3 -",
		"paste-buffer -d -p -r -b cs3 -t %3",
	}, rec.commands())
	require.Equal(t, []string{"line one\nline two"}, rec.stdin)
}

func TestKeysNotesAndPaneList(t *testing.T) {
	rec := &recorder{listOut: "%1\tcodex\t0\n%2\t\t0\n%3\tclaude\t1\nbroken line\n"}
	tmux := &Tmux{exec: rec.exec(), socket: Socket}
	require.NoError(t, tmux.SendKeys("%1", "Enter"))
	require.NoError(t, tmux.Backspace("%1", 5))
	require.NoError(t, tmux.Backspace("%1", 0))
	require.NoError(t, tmux.SetNote("%1", "Q: #1 → Codex …"))
	panes, err := tmux.Panes()
	require.NoError(t, err)
	require.Equal(t, []PaneState{{ID: "%1", Agent: "codex"}, {ID: "%2"}, {ID: "%3", Agent: "claude", Dead: true}}, panes)
	require.Equal(t, []string{
		"if-shell -F -t %1 #{pane_in_mode} send-keys -X -t %1 cancel",
		"send-keys -t %1 Enter",
		"send-keys -t %1 -N 5 BSpace",
		"set-option -p -t %1 @cs_note Q: ##1 → Codex …",
		"list-panes -s -t squad -F #{pane_id}\t#{@cs_agent}\t#{pane_dead}",
	}, rec.commands())
}

func TestCommandErrorsCarryTmuxOutput(t *testing.T) {
	rec := &recorder{failOn: "send-keys"}
	tmux := &Tmux{exec: rec.exec(), socket: Socket}
	err := tmux.Backspace("%1", 1)
	require.ErrorContains(t, err, "tmux send-keys")
	require.False(t, (&Tmux{exec: (&recorder{failOn: "has-session"}).exec()}).Running())
	require.True(t, tmux.Running())
}

func TestRestyleUpdatesRunningPanes(t *testing.T) {
	rec := &recorder{listOut: "%1\tcodex\t0\n%2\t\t0\n"}
	tmux := &Tmux{exec: rec.exec(), socket: Socket}
	require.NoError(t, tmux.Restyle(testPanes(), "/bin/cs"))
	cmds := rec.commands()
	require.Contains(t, cmds, "set-option -p -t %1 @cs_title Codex")
	require.Contains(t, cmds, "set-option -p -t %2 @cs_title "+PanelTitle)
	require.Contains(t, cmds, "set-option -g pane-border-lines heavy")
	require.NotContains(t, strings.Join(cmds, "\n"), "Claude Code", "panes that are not running are skipped")

	require.Error(t, (&Tmux{exec: (&recorder{failOn: "list-panes"}).exec()}).Restyle(nil, "cs"))
}

func TestToastAndStatusTitle(t *testing.T) {
	rec := &recorder{}
	tmux := &Tmux{exec: rec.exec(), socket: Socket}
	require.NoError(t, tmux.Toast("🎉 #1 届いた"))
	require.Equal(t, []string{"display-message -d 3000 🎉 ##1 届いた"}, rec.commands())
	require.Contains(t, statusTitle(), "#[fg=#FF5F87] ")
	require.Contains(t, statusTitle(), "S")
}

func TestShellCommandQuotesArguments(t *testing.T) {
	require.Equal(t, `claude '--add-dir' '/cfg' '--settings' '{"a":"it'\''s"}'`,
		ShellCommand("claude", []string{"--add-dir", "/cfg", "--settings", `{"a":"it's"}`}))
	require.Equal(t, "codex --search", ShellCommand("codex --search", nil))
}

func TestLayoutPutsThePanelInTheFreeSlot(t *testing.T) {
	ws := &workspace.Workspace{Dir: "/repo", Kind: workspace.KindGit, Members: []workspace.Member{
		{Agent: "claude", Dir: "/wt/c", Branch: "me/claude", Program: "/bin/claude"},
		{Agent: "codex", Dir: "/wt/x", Branch: "me/codex", Program: "codex --search"},
		{Agent: "gemini", Dir: "/wt/g", Program: ""},
	}}
	panes := Layout(ws, "/bin/cs", "/cfg")
	bySlot := map[relay.Slot]Pane{}
	for _, p := range panes {
		bySlot[p.Slot] = p
	}
	require.Len(t, bySlot, 4)
	require.Equal(t, "codex", bySlot[relay.SlotTopLeft].Agent)
	require.Equal(t, "🤖 Codex ⎇ me/codex", bySlot[relay.SlotTopLeft].Title)
	require.Equal(t, relay.AgentCodex.Color(), bySlot[relay.SlotTopLeft].Color)
	codex := bySlot[relay.SlotTopLeft].Command
	require.True(t, strings.HasPrefix(codex, "codex --search '-c' 'developer_instructions=\"[claude-squad] "), codex)
	require.Contains(t, codex, "/wt/c", "Codex is told where Claude Code works")
	require.NotContains(t, codex, "/wt/g", "an agent that is not installed is not a peer")
	require.Equal(t, "claude", bySlot[relay.SlotBottomLeft].Agent)
	claude := bySlot[relay.SlotBottomLeft].Command
	require.True(t, strings.HasPrefix(claude, "/bin/claude '--add-dir' '/cfg' '--append-system-prompt' '[claude-squad] "), claude)
	require.Contains(t, claude, "- Codex: /wt/x（ブランチ me/codex）")
	require.Contains(t, claude, "'\\''/bin/cs'\\'' transcript codex", "the transcript command is quoted inside the quoted argument")
	require.Equal(t, PanelTitle, bySlot[relay.SlotTopRight].Title)
	require.Equal(t, "'/bin/cs' panel", bySlot[relay.SlotTopRight].Command)
	require.Equal(t, "/repo", bySlot[relay.SlotTopRight].Dir)
	require.Contains(t, bySlot[relay.SlotBottomRight].Command, "Gemini (agy) が見つかりません", "a missing agent explains itself")
	require.Contains(t, bySlot[relay.SlotBottomRight].Command, "config.json")
}
