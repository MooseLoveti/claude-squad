// Package dashboard drives the tmux server that shows every agent and the
// control panel side by side. It runs on its own socket, so the user's own
// tmux sessions, options and key bindings are never touched.
package dashboard

import (
	"bytes"
	"claude-squad/cmd"
	"claude-squad/relay"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	// Socket names the dashboard's tmux server (tmux -L).
	Socket = "claudesquad"
	// Session is the dashboard's only tmux session.
	Session = "squad"

	// agentOption holds the agent name on an agent's pane; the Enter key
	// binding only consults the hook on panes that have it.
	agentOption = "@cs_agent"
	titleOption = "@cs_title"
	noteOption  = "@cs_note"
)

// Pane is one pane of the dashboard.
type Pane struct {
	Slot relay.Slot
	// Agent is the agent's Name, empty for the control panel.
	Agent   string
	Title   string
	Dir     string
	Command string
}

// PaneState is a running pane as tmux reports it.
type PaneState struct {
	ID    string
	Agent string
	Dead  bool
}

// Tmux runs tmux commands against the dashboard's server.
type Tmux struct {
	exec   cmd.Executor
	socket string
}

func New(exec cmd.Executor) *Tmux {
	return &Tmux{exec: exec, socket: Socket}
}

func (t *Tmux) command(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-L", t.socket}, args...)...)
}

func (t *Tmux) run(args ...string) error {
	c := t.command(args...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := t.exec.Run(c); err != nil {
		return fmt.Errorf("tmux %s: %w %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (t *Tmux) output(args ...string) (string, error) {
	c := t.command(args...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := t.exec.Output(c)
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Running reports whether the dashboard session exists.
func (t *Tmux) Running() bool {
	return t.run("has-session", "-t", "="+Session) == nil
}

// Create starts the dashboard with one pane per slot, laid out 2×2, and
// configures the server. self is the claude-squad executable, which the Enter
// key binding calls as `self hook enter <pane>`.
func (t *Tmux) Create(panes []Pane, width, height int, self string) error {
	if len(panes) == 0 {
		return errors.New("no panes to show")
	}
	ordered := append([]Pane(nil), panes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Slot < ordered[j].Slot })

	ids := make([]string, len(ordered))
	for i, p := range ordered {
		args := []string{"split-window", "-t", Session, "-c", p.Dir, "-P", "-F", "#{pane_id}", p.Command}
		if i == 0 {
			args = []string{"new-session", "-d", "-s", Session, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height),
				"-c", p.Dir, "-P", "-F", "#{pane_id}", p.Command}
		}
		out, err := t.output(args...)
		if err != nil {
			if i > 0 {
				_ = t.Kill()
			}
			return err
		}
		ids[i] = strings.TrimSpace(out)
		// Splitting a small pane fails, so re-tile after every split.
		if err := t.run("select-layout", "-t", Session, "tiled"); err != nil {
			_ = t.Kill()
			return err
		}
	}
	for i, p := range ordered {
		if err := t.setPaneOption(ids[i], titleOption, p.Title); err != nil {
			return err
		}
		if p.Agent != "" {
			if err := t.setPaneOption(ids[i], agentOption, p.Agent); err != nil {
				return err
			}
		}
	}
	if err := t.configure(self); err != nil {
		_ = t.Kill()
		return err
	}
	// Start with the first agent pane selected, ready to type into.
	for i, p := range ordered {
		if p.Agent != "" {
			return t.run("select-pane", "-t", ids[i])
		}
	}
	return nil
}

func (t *Tmux) configure(self string) error {
	hook := fmt.Sprintf(`run-shell "%s hook enter #{pane_id}"`, shellQuote(self))
	settings := [][]string{
		{"set-option", "-g", "mouse", "on"},
		{"set-option", "-g", "history-limit", "50000"},
		// The CLIs read Esc on its own; tmux's default 500ms delay makes it lag.
		{"set-option", "-s", "escape-time", "10"},
		{"set-option", "-as", "terminal-features", ",*:RGB"},
		// Claude Code tracks focus to know when the user is looking.
		{"set-option", "-g", "focus-events", "on"},
		// A pane whose agent exits stays, so it can be restarted from the panel.
		{"set-option", "-g", "remain-on-exit", "on"},
		{"set-option", "-g", "pane-border-status", "top"},
		{"set-option", "-g", "pane-border-format",
			"#{?pane_active,#[reverse],} #{" + titleOption + "}#{?pane_dead, (停止),} #[default] #{" + noteOption + "}"},
		{"set-option", "-g", "status-left", " claude-squad "},
		{"set-option", "-g", "status-left-length", "20"},
		{"set-option", "-g", "status-right", " Shift+矢印/クリックで切替 · C-b z 拡大 · C-b d デタッチ "},
		{"set-option", "-g", "status-right-length", "60"},
		{"set-option", "-g", "window-status-format", ""},
		{"set-option", "-g", "window-status-current-format", ""},
		// Enter on an agent pane goes through the hook, which diverts input
		// that starts with an @tag; tmux holds later keys until it returns.
		{"bind-key", "-n", "Enter", "if-shell", "-F", "#{" + agentOption + "}", hook, "send-keys Enter"},
		// Shift+arrows move between the panes; plain arrows stay with the
		// CLIs, which use them for history and menus. (Windows Terminal keeps
		// Alt+arrows for its own panes.)
		{"bind-key", "-n", "S-Left", "select-pane", "-L"},
		{"bind-key", "-n", "S-Right", "select-pane", "-R"},
		{"bind-key", "-n", "S-Up", "select-pane", "-U"},
		{"bind-key", "-n", "S-Down", "select-pane", "-D"},
	}
	for _, args := range settings {
		if err := t.run(args...); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tmux) setPaneOption(pane, option, value string) error {
	return t.run("set-option", "-p", "-t", pane, option, value)
}

// Panes lists the dashboard's panes.
func (t *Tmux) Panes() ([]PaneState, error) {
	out, err := t.output("list-panes", "-s", "-t", Session, "-F", "#{pane_id}\t#{"+agentOption+"}\t#{pane_dead}")
	if err != nil {
		return nil, err
	}
	var panes []PaneState
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		panes = append(panes, PaneState{ID: fields[0], Agent: fields[1], Dead: fields[2] == "1"})
	}
	return panes, nil
}

// Capture returns the visible pane with colors.
func (t *Tmux) Capture(pane string) (string, error) {
	return t.output("capture-pane", "-p", "-e", "-J", "-t", pane)
}

// CaptureHistory returns the pane's whole scrollback and screen.
func (t *Tmux) CaptureHistory(pane string) (string, error) {
	return t.output("capture-pane", "-p", "-e", "-J", "-S", "-", "-E", "-", "-t", pane)
}

// leaveCopyMode makes sure typed text reaches the program, not tmux's
// scrollback view, which a mouse wheel scroll enters.
func (t *Tmux) leaveCopyMode(pane string) error {
	return t.run("if-shell", "-F", "-t", pane, "#{pane_in_mode}", "send-keys -X -t "+pane+" cancel")
}

// Paste types text into a pane as one paste, so newlines stay inside the
// message instead of submitting it line by line.
func (t *Tmux) Paste(pane, text string) error {
	if err := t.leaveCopyMode(pane); err != nil {
		return err
	}
	buffer := "cs" + strings.TrimPrefix(pane, "%")
	load := t.command("load-buffer", "-b", buffer, "-")
	load.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	load.Stderr = &stderr
	if err := t.exec.Run(load); err != nil {
		return fmt.Errorf("tmux load-buffer: %w %s", err, strings.TrimSpace(stderr.String()))
	}
	return t.run("paste-buffer", "-d", "-p", "-r", "-b", buffer, "-t", pane)
}

// SendKeys sends tmux key names, such as "Enter", to a pane.
func (t *Tmux) SendKeys(pane string, keys ...string) error {
	if err := t.leaveCopyMode(pane); err != nil {
		return err
	}
	return t.run(append([]string{"send-keys", "-t", pane}, keys...)...)
}

// Backspace deletes n characters before the cursor.
func (t *Tmux) Backspace(pane string, n int) error {
	if n <= 0 {
		return nil
	}
	return t.run("send-keys", "-t", pane, "-N", strconv.Itoa(n), "BSpace")
}

// SetNote shows a note next to the pane's title.
func (t *Tmux) SetNote(pane, note string) error {
	return t.setPaneOption(pane, noteOption, strings.ReplaceAll(note, "#", "##"))
}

// Respawn restarts the pane's program.
func (t *Tmux) Respawn(pane, dir, command string) error {
	return t.run("respawn-pane", "-k", "-t", pane, "-c", dir, command)
}

// Popup runs a command in a popup over the dashboard and returns when the
// popup closes, so callers that must stay responsive run it in a goroutine.
func (t *Tmux) Popup(dir, command string) error {
	return t.run("display-popup", "-E", "-w", "90%", "-h", "85%", "-d", dir, command)
}

// Detach detaches every client, leaving the agents running.
func (t *Tmux) Detach() error {
	return t.run("detach-client", "-s", Session)
}

// Kill stops the dashboard server and every program in it.
func (t *Tmux) Kill() error {
	return t.run("kill-server")
}

// Attach attaches the terminal to the dashboard until the user detaches.
// Inside another tmux, TMUX is dropped so the nested client is allowed.
func (t *Tmux) Attach() error {
	c := t.command("attach-session", "-t", Session)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") {
			c.Env = append(c.Env, kv)
		}
	}
	return t.exec.Run(c)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellCommand joins a program and extra arguments into a shell command,
// quoting the arguments.
func ShellCommand(program string, args []string) string {
	command := program
	for _, arg := range args {
		command += " " + shellQuote(arg)
	}
	return command
}
