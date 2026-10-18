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
	colorOption = "@cs_color"

	// Theme colors of the dashboard chrome.
	backgroundColor = "#1A1033"
	accentColor     = "#FF5FD7"
	noteColor       = "#FFD75F"
	idleBorderColor = "#3A3A5A"
)

// rainbow colors the title letters in the status bar.
var rainbow = []string{"#FF5F87", "#FF8700", "#FFD700", "#5FFF87", "#00D7FF", "#AF87FF"}

// Pane is one pane of the dashboard.
type Pane struct {
	Slot relay.Slot
	// Agent is the agent's Name, empty for the control panel.
	Agent   string
	Title   string
	Color   string
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

// Create starts the dashboard with one pane per slot, in slot order, and
// configures the server. tmux's tiled layout puts four panes in a 2×2 grid
// and a fifth in a full-width row under it. self is the claude-squad executable, which the Enter
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
		if err := t.setPaneOption(ids[i], colorOption, p.Color); err != nil {
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
	if err := t.FitPanel(); err != nil {
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

// Restyle applies this version's look to a running dashboard: every pane's
// title and color from panes (matched by agent, the panel by having none),
// and the server's options and key bindings.
func (t *Tmux) Restyle(panes []Pane, self string) error {
	running, err := t.Panes()
	if err != nil {
		return err
	}
	for _, state := range running {
		for _, p := range panes {
			if p.Agent != state.Agent {
				continue
			}
			if err := t.setPaneOption(state.ID, titleOption, p.Title); err != nil {
				return err
			}
			if err := t.setPaneOption(state.ID, colorOption, p.Color); err != nil {
				return err
			}
		}
	}
	return t.configure(self)
}

// PanelHeight is the control panel's height in rows: the title, a row per
// agent and one more for usage that wraps, the banner, the broadcast box, a
// notice and the buttons. The agents get the rest.
const PanelHeight = 10

// FitPanel gives the control panel PanelHeight rows and splits the rest
// evenly between the two rows of agents. It runs again whenever a terminal
// is resized or attaches.
func (t *Tmux) FitPanel() error {
	out, err := t.output("list-panes", "-t", Session, "-F",
		"#{pane_id}\t#{"+agentOption+"}\t#{pane_height}\t#{pane_width}\t#{window_width}")
	if err != nil {
		return err
	}
	var panel, top, bottom string
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			continue
		}
		height, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		switch {
		case fields[1] == "" && fields[3] != fields[4]:
			// A dashboard started by an older version has the panel in
			// the grid; its rows are left alone.
			return nil
		case fields[1] == "":
			panel = fields[0]
		case relay.AgentByName(fields[1]).Slot() == relay.SlotTopLeft:
			top = fields[0]
		case relay.AgentByName(fields[1]).Slot() == relay.SlotBottomLeft:
			bottom = fields[0]
		default:
			continue
		}
		total += height
	}
	if panel == "" || top == "" || bottom == "" {
		return errors.New("the dashboard has no panel or left column of agents")
	}
	if err := t.run("resize-pane", "-t", panel, "-y", strconv.Itoa(PanelHeight)); err != nil {
		return err
	}
	// The panel's rows went to or came from the agents above it, and the
	// borders between the rows stay as they are. The top row's size counts
	// its title line, which pane-border-status puts above it.
	return t.run("resize-pane", "-t", top, "-y", strconv.Itoa(max((total-PanelHeight)/2, 1)+1))
}

func (t *Tmux) configure(self string) error {
	hook := fmt.Sprintf(`run-shell "%s hook enter #{pane_id}"`, shellQuote(self))
	resized := fmt.Sprintf(`run-shell "%s hook resized"`, shellQuote(self))
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
		{"set-option", "-g", "pane-border-lines", "heavy"},
		{"set-option", "-g", "pane-border-style", "fg=" + idleBorderColor},
		{"set-option", "-g", "pane-active-border-style", "fg=" + accentColor + ",bold"},
		// The active pane's title is its own color as a filled badge.
		{"set-option", "-g", "pane-border-format",
			"#{?pane_active,#[fg=" + backgroundColor + "#,bg=#{" + colorOption + "}#,bold] ▶,#[fg=#{" + colorOption + "}#,bold] }" +
				" #{" + titleOption + "}#{?pane_dead, 💀停止,} #[default] #[fg=" + noteColor + ",bold]#{" + noteOption + "}#[default]"},
		{"set-option", "-g", "status-style", "bg=" + backgroundColor + ",fg=#E0E0FF"},
		{"set-option", "-g", "status-left", statusTitle()},
		{"set-option", "-g", "status-left-length", "40"},
		{"set-option", "-g", "status-right", "#[fg=#AF87FF] Shift+矢印 / クリックで移動 #[fg=" + idleBorderColor + "]│#[fg=#00D7FF] C-b z 拡大 #[fg=" + idleBorderColor + "]│#[fg=#5FFF87] C-b d デタッチ "},
		{"set-option", "-g", "status-right-length", "80"},
		{"set-option", "-g", "message-style", "bg=" + accentColor + ",fg=" + backgroundColor + ",bold"},
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
		// tmux scales the panes with the window, so the panel is fitted
		// again when a terminal is resized or attaches at another size.
		{"set-hook", "-g", "client-resized", resized},
		{"set-hook", "-g", "client-attached", resized},
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

// statusTitle is " ✦ CLAUDE SQUAD ✦ " with every letter in a rainbow color.
func statusTitle() string {
	var b strings.Builder
	b.WriteString("#[bold]")
	for i, r := range []rune(" ✦ CLAUDE SQUAD ✦ ") {
		fmt.Fprintf(&b, "#[fg=%s]%c", rainbow[i%len(rainbow)], r)
	}
	b.WriteString("#[default] ")
	return b.String()
}

// Toast flashes a message on the status line for a few seconds.
func (t *Tmux) Toast(text string) error {
	return t.run("display-message", "-d", "3000", strings.ReplaceAll(text, "#", "##"))
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
