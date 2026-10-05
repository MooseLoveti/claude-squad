package tmux

import (
	cmd2 "claude-squad/cmd"
	"claude-squad/log"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"claude-squad/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	log.Initialize(false)
	defer log.Close()
	os.Exit(m.Run())
}

type MockPtyFactory struct {
	t *testing.T

	// Array of commands and the corresponding file handles representing PTYs.
	cmds  []*exec.Cmd
	files []*os.File
}

func (pt *MockPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	filePath := filepath.Join(pt.t.TempDir(), fmt.Sprintf("pty-%s-%d", pt.t.Name(), rand.Int31()))
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0644)
	if err == nil {
		pt.cmds = append(pt.cmds, cmd)
		pt.files = append(pt.files, f)
	}
	return f, err
}

func (pt *MockPtyFactory) Close() {}

func NewMockPtyFactory(t *testing.T) *MockPtyFactory {
	return &MockPtyFactory{
		t: t,
	}
}

func TestSanitizeName(t *testing.T) {
	session := NewTmuxSession("asdf", "program")
	require.Equal(t, TmuxPrefix+"asdf", session.sanitizedName)

	session = NewTmuxSession("a sd f . . asdf", "program")
	require.Equal(t, TmuxPrefix+"asdf__asdf", session.sanitizedName)
}

func TestStartTmuxSession(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)

	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("output"), nil
		},
	}

	workdir := t.TempDir()
	session := newTmuxSession("test-session", "claude", ptyFactory, cmdExec)

	err := session.Start(workdir)
	require.NoError(t, err)
	require.Equal(t, 2, len(ptyFactory.cmds))
	require.Equal(t, fmt.Sprintf("tmux new-session -d -s claudesquad_test-session -c %s claude", workdir),
		cmd2.ToString(ptyFactory.cmds[0]))
	require.Equal(t, "tmux attach-session -t claudesquad_test-session",
		cmd2.ToString(ptyFactory.cmds[1]))

	require.Equal(t, 2, len(ptyFactory.files))

	// File should be closed.
	_, err = ptyFactory.files[0].Stat()
	require.Error(t, err)
	// File should be open
	_, err = ptyFactory.files[1].Stat()
	require.NoError(t, err)
}

func TestSendKeysLeavesCopyModeFirst(t *testing.T) {
	var events []string
	ptmx, err := os.CreateTemp(t.TempDir(), "ptmx")
	require.NoError(t, err)
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			events = append(events, cmd2.ToString(cmd))
			return fmt.Errorf("tmux failed")
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return nil, nil },
	}
	session := newTmuxSession("test-session", "agy", NewMockPtyFactory(t), cmdExec)
	session.ptmx = ptmx

	require.NoError(t, session.SendKeys("テスト"), "failing to leave copy mode does not block sending")

	require.Equal(t, []string{"tmux copy-mode -q -t claudesquad_test-session"}, events)
	written, err := os.ReadFile(ptmx.Name())
	require.NoError(t, err)
	require.Equal(t, "テスト", string(written))
}

func TestStartTmuxSessionAppendsQuotedLaunchArgs(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("output"), nil
		},
	}

	session := newTmuxSession("test-session", "/bin/claude --model opus", ptyFactory, cmdExec)
	session.SetLaunchArgs([]string{"--add-dir", "/home/o'neil/my dir"})
	require.NoError(t, session.Start(t.TempDir()))

	args := ptyFactory.cmds[0].Args
	require.Equal(t, `/bin/claude --model opus '--add-dir' '/home/o'\''neil/my dir'`, args[len(args)-1])
	require.Equal(t, "/bin/claude --model opus", session.program, "program stays unchanged for prompt detection")
}

// A tmux server that has gone away (reboot, crash, `tmux kill-server`) takes every session
// with it. attach-session against a missing session still forks successfully, so Restore
// has to check for the session itself or it reports success while attached to nothing.
func TestRestoreReturnsErrSessionNotFoundWhenSessionIsGone(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") {
				return fmt.Errorf("can't find session")
			}
			return nil
		},
	}

	session := NewTmuxSessionWithDeps("gone", "program", ptyFactory, cmdExec)
	err := session.Restore()

	require.ErrorIs(t, err, ErrSessionNotFound)
	require.Empty(t, ptyFactory.cmds, "should not have opened a PTY for a session that does not exist")
}

func TestRestoreAttachesWhenSessionExists(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error { return nil },
	}

	session := NewTmuxSessionWithDeps("alive", "program", ptyFactory, cmdExec)
	require.NoError(t, session.Restore())
	require.Len(t, ptyFactory.cmds, 1)
	require.Contains(t, ptyFactory.cmds[0].String(), "attach-session")
}

func newInputTestSession(t *testing.T) (*TmuxSession, func() string) {
	t.Helper()
	ptmx, err := os.CreateTemp(t.TempDir(), "ptmx")
	require.NoError(t, err)
	session := newTmuxSession("input", "codex", NewMockPtyFactory(t), cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { return nil },
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return nil, nil },
	})
	session.ptmx = ptmx
	return session, func() string {
		b, err := os.ReadFile(ptmx.Name())
		require.NoError(t, err)
		return string(b)
	}
}

func TestHandleInputFindsCtrlQAnywhere(t *testing.T) {
	for name, input := range map[string]string{
		"alone":            "\x11",
		"after text":       "ab\x11",
		"kitty encoding":   "ab\x1b[113;5u",
		"xterm encoding":   "ab\x1b[27;5;113~",
		"before more text": "ab\x11cd",
	} {
		t.Run(name, func(t *testing.T) {
			session, written := newInputTestSession(t)
			require.True(t, session.handleInput([]byte(input)))
			if input == "\x11" {
				require.Empty(t, written())
			} else {
				require.Equal(t, "ab", written(), "input before Ctrl+Q still reaches the program")
			}
		})
	}

	session, written := newInputTestSession(t)
	require.False(t, session.handleInput([]byte("q")))
	require.Equal(t, "q", written())
}

func TestHandleInputEnterHook(t *testing.T) {
	session, written := newInputTestSession(t)
	calls := 0
	consume := true
	session.SetEnterHook(func() bool { calls++; return consume })

	require.False(t, session.handleInput([]byte("\r")))
	require.Equal(t, 1, calls)
	require.Empty(t, written(), "a consumed Enter never reaches the program")

	consume = false
	require.False(t, session.handleInput([]byte("\r")))
	require.Equal(t, "\r", written())

	require.False(t, session.handleInput([]byte("pasted\r")))
	require.Equal(t, 2, calls, "Enter inside a paste is not a submit")

	session.SetEnterHook(nil)
	require.False(t, session.handleInput([]byte("\r")))
	require.Equal(t, 2, calls)
	require.Equal(t, "\rpasted\r\r", written())
}

func TestSetStatusNote(t *testing.T) {
	var ran []string
	session := newTmuxSession("notes", "codex", NewMockPtyFactory(t), cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { ran = append(ran, cmd2.ToString(cmd)); return nil },
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return nil, nil },
	})

	require.NoError(t, session.SetStatusNote("Q: #1? → Codex ✓"))
	require.Equal(t, []string{
		"tmux set-option -t claudesquad_notes status-right-length 23",
		"tmux set-option -t claudesquad_notes status-right Q: ##1? → Codex ✓ ",
	}, ran, "# is escaped so tmux shows it literally")

	ran = nil
	require.NoError(t, session.SetStatusNote(""))
	require.Equal(t, []string{
		"tmux set-option -u -t claudesquad_notes status-right",
		"tmux set-option -u -t claudesquad_notes status-right-length",
	}, ran)
}

func TestShowPopup(t *testing.T) {
	popup := make(chan string, 1)
	release := make(chan struct{})
	clients := "4100 /dev/pts/4\n4242 /dev/pts/12\n"
	session := newTmuxSession("pop", "codex", NewMockPtyFactory(t), cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			popup <- cmd2.ToString(cmd)
			<-release // display-popup blocks until the popup closes
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			require.Equal(t, "tmux list-clients -t claudesquad_pop -F #{client_pid} #{client_name}", cmd2.ToString(cmd))
			return []byte(clients), nil
		},
	})
	session.clientPid = 4242

	require.NoError(t, session.ShowPopup("/cfg/relay/it's.txt"), "returns while the popup is still open")
	require.Equal(t,
		`tmux display-popup -c /dev/pts/12 -w 90% -h 85% -E LESSCHARSET=utf-8 less -R '-Ps(q to close)' '-Pm(q to close)' -- '/cfg/relay/it'\''s.txt'`,
		<-popup, "the popup opens on this claude-squad's own client, not another process's")
	close(release)

	session.clientPid = 9999
	require.ErrorContains(t, session.ShowPopup("/x"), "has no client", "never falls back to another process's client")
	session.clientPid = 0
	require.ErrorContains(t, session.ShowPopup("/x"), "has no client")

	failing := newTmuxSession("gone", "codex", NewMockPtyFactory(t), cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { return nil },
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return nil, fmt.Errorf("no server") },
	})
	require.ErrorContains(t, failing.ShowPopup("/x"), "find the client")
}
