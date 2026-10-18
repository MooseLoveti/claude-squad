// Package panel is the dashboard's control pane: usage meters, per-agent
// buttons, a broadcast box and the relay that carries @tag questions between
// the agents' panes.
package panel

import (
	"claude-squad/config"
	"claude-squad/dashboard"
	"claude-squad/log"
	"claude-squad/picker"
	"claude-squad/relay"
	"claude-squad/workspace"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	statusInterval = time.Second
	// paneInterval is how often stopped agents are looked for.
	paneInterval = 5 * time.Second
	// frameInterval paces the animations.
	frameInterval = 150 * time.Millisecond
	// messageLifetime is how long a one-line notice stays above the footer.
	messageLifetime = 8 * time.Second
	// bannerLifetime is how long a celebration stays up.
	bannerLifetime = 6 * time.Second
	// freshLogLifetime is how long a new log line stays highlighted.
	freshLogLifetime = 3 * time.Second
)

type usageState struct {
	usage relay.Usage
	err   error
	// gen tells a scheduled refresh from one made obsolete by a manual refresh.
	gen int
}

// switchPlan is a folder chosen to work in next, waiting for confirmation.
type switchPlan struct {
	dir  string
	kind workspace.Kind
	// noCommits marks a repository that needs a first commit for worktrees.
	noCommits bool
}

// banner is the celebration line.
type banner struct {
	text  string
	warn  bool
	until time.Time
}

// Model is the control panel's bubbletea model.
type Model struct {
	ws        *workspace.Workspace
	state     *config.State
	tmux      *dashboard.Tmux
	relay     *Relay
	members   []Member
	env       relay.LaunchEnv
	configDir string
	// toast flashes a message on the dashboard's status line.
	toast func(string)
	// pickFolder opens the desktop's folder dialog.
	pickFolder func(ctx context.Context, title, initial string) (string, error)

	width, height int
	input         textinput.Model
	usage         map[relay.Agent]*usageState
	dead          map[relay.Agent]bool
	status        Status
	now           time.Time
	frame         int
	// lastSeq is the newest relay log entry already celebrated.
	lastSeq int
	banner  banner

	message   string
	messageAt time.Time
	// closing is set while the user confirms closing the workspace; force
	// is set when closing must skip worktrees with uncommitted changes.
	closing bool
	force   bool
	// switching is set while the user confirms working in another folder.
	switching *switchPlan

	zones []zone
}

type statusTickMsg struct{}

type frameMsg struct{}

type paneTickMsg struct{}

// panesMsg reports which agents' programs have exited.
type panesMsg struct {
	dead map[relay.Agent]bool
}

type usageTickMsg struct {
	agent relay.Agent
	gen   int
}

type usageMsg struct {
	agent relay.Agent
	gen   int
	usage relay.Usage
	err   error
}

// noticeMsg shows the outcome of a button above the footer.
type noticeMsg struct {
	text string
	err  error
}

// closeFailedMsg means closing the workspace needs confirming.
type closeFailedMsg struct{ err error }

// folderPickedMsg is the folder dialog's result.
type folderPickedMsg struct {
	dir string
	err error
}

func newModel(ws *workspace.Workspace, state *config.State, tmux *dashboard.Tmux, r *Relay, members []Member,
	env relay.LaunchEnv, configDir string, toast func(string)) *Model {
	input := textinput.New()
	input.Prompt = "› "
	m := &Model{
		ws: ws, state: state, tmux: tmux, relay: r, members: members, env: env, configDir: configDir,
		toast: toast, pickFolder: picker.Folder,
		input: input,
		usage: map[relay.Agent]*usageState{},
		dead:  map[relay.Agent]bool{},
		now:   time.Now(),
	}
	for _, member := range members {
		m.usage[member.Agent] = &usageState{err: relay.ErrNoUsage}
	}
	return m
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.statusTick(), m.frameTick(), m.refreshPanes()}
	for _, member := range m.members {
		cmds = append(cmds, m.fetchUsage(member.Agent, 0))
	}
	return tea.Batch(cmds...)
}

func (m *Model) statusTick() tea.Cmd {
	return tea.Tick(statusInterval, func(time.Time) tea.Msg { return statusTickMsg{} })
}

func (m *Model) frameTick() tea.Cmd {
	return tea.Tick(frameInterval, func(time.Time) tea.Msg { return frameMsg{} })
}

func (m *Model) program(agent relay.Agent) string {
	if member, ok := m.ws.Member(agent.Name()); ok {
		return member.Program
	}
	return ""
}

func (m *Model) fetchUsage(agent relay.Agent, gen int) tea.Cmd {
	env := relay.UsageEnv{ConfigDir: m.configDir, Program: m.program(agent)}
	return func() tea.Msg {
		usage, err := agent.Usage(context.Background(), env)
		return usageMsg{agent: agent, gen: gen, usage: usage, err: err}
	}
}

func (m *Model) refreshUsage() tea.Cmd {
	var cmds []tea.Cmd
	for agent, state := range m.usage {
		state.gen++
		cmds = append(cmds, m.fetchUsage(agent, state.gen))
	}
	m.notice("🔄 使用量を更新中…", nil)
	return tea.Batch(cmds...)
}

func (m *Model) refreshPanes() tea.Cmd {
	tmux := m.tmux
	return func() tea.Msg {
		msg := panesMsg{dead: map[relay.Agent]bool{}}
		if tmux == nil {
			return msg
		}
		if panes, err := tmux.Panes(); err == nil {
			for _, p := range panes {
				if agent := relay.AgentByName(p.Agent); agent != relay.AgentUnknown {
					msg.dead[agent] = p.Dead
				}
			}
		}
		return msg
	}
}

func (m *Model) notice(text string, err error) {
	if err != nil {
		text = text + ": " + err.Error()
	}
	m.message, m.messageAt = text, m.now
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(m.width-20, 10)
	case frameMsg:
		m.frame++
		m.now = time.Now()
		return m, m.frameTick()
	case statusTickMsg:
		m.now = time.Now()
		m.status = m.relay.Status()
		m.celebrateNew()
		if m.message != "" && m.now.Sub(m.messageAt) > messageLifetime {
			m.message = ""
		}
		return m, m.statusTick()
	case panesMsg:
		m.dead = msg.dead
		return m, tea.Tick(paneInterval, func(time.Time) tea.Msg { return paneTickMsg{} })
	case paneTickMsg:
		return m, m.refreshPanes()
	case usageMsg:
		state := m.usage[msg.agent]
		if state == nil || msg.gen != state.gen {
			return m, nil
		}
		state.usage, state.err = msg.usage, msg.err
		interval := msg.agent.UsagePollInterval()
		return m, tea.Tick(interval, func(time.Time) tea.Msg { return usageTickMsg{agent: msg.agent, gen: msg.gen} })
	case usageTickMsg:
		if state := m.usage[msg.agent]; state != nil && msg.gen == state.gen {
			return m, m.fetchUsage(msg.agent, msg.gen)
		}
	case noticeMsg:
		m.notice(msg.text, msg.err)
	case folderPickedMsg:
		m.planSwitch(msg)
	case closeFailedMsg:
		m.closing, m.force = true, true
		m.notice("🚧 まだ閉じていません", msg.err)
	case tea.MouseMsg:
		return m, m.handleMouse(msg)
	case tea.KeyMsg:
		return m, m.handleKey(msg)
	}
	return m, nil
}

// celebrateNew shows the relay events since the last status, the most
// recent one as a banner.
func (m *Model) celebrateNew() {
	for _, e := range m.status.Log {
		if e.Seq <= m.lastSeq {
			continue
		}
		m.lastSeq = e.Seq
		m.celebrate(e)
	}
}

func (m *Model) celebrate(e LogEntry) {
	who := e.Agent.Emoji() + " " + e.Agent.Label()
	b := banner{until: m.now.Add(bannerLifetime)}
	switch e.Kind {
	case EventAsk:
		b.text = fmt.Sprintf("📤 %s が %d人に相談！", who, e.Count)
	case EventAnswer:
		b.text = fmt.Sprintf("📥 %s が回答！", who)
	case EventDeliver:
		b.text = fmt.Sprintf("🎉 %s に意見が届いた！", who)
		m.flash(b.text)
	case EventBroadcast:
		b.text = "📣 全員に一斉送信！"
	case EventWarn:
		b.text, b.warn = "🚨 "+e.Text, true
	default:
		return
	}
	m.banner = b
}

func (m *Model) flash(text string) {
	if m.toast != nil {
		m.toast(text)
	}
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return nil
	}
	// A button on a clickable row wins over the row: the narrowest zone does.
	var hit *zone
	for i, z := range m.zones {
		if z.row == msg.Y && msg.X >= z.x0 && msg.X < z.x1 && z.action != nil && (hit == nil || z.x1-z.x0 < hit.x1-hit.x0) {
			hit = &m.zones[i]
		}
	}
	if hit == nil {
		return nil
	}
	return hit.action()
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.input.Focused() {
		switch msg.Type {
		case tea.KeyEnter:
			return m.broadcast()
		case tea.KeyEsc:
			m.input.Blur()
			return nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}
	switch msg.String() {
	case "i", "tab":
		return m.input.Focus()
	case "a":
		return m.toggleAutoRelay()
	case "u":
		return m.refreshUsage()
	case "l":
		return m.openLog()
	case "d":
		return m.detach()
	}
	// The panel is not quit by keys: without it, @tag questions stop.
	return nil
}

func (m *Model) broadcast() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	m.input.SetValue("")
	r := m.relay
	return func() tea.Msg {
		r.Broadcast(text)
		return nil
	}
}

func (m *Model) toggleAutoRelay() tea.Cmd {
	on := !m.status.AutoRelay
	m.relay.SetAutoRelay(on)
	m.status = m.relay.Status()
	if on {
		m.notice(fmt.Sprintf("⚡ 自動Relay ON: AIの返答の@タグも相手に聞きます（%d回まで）", autoRelayLimit), nil)
	} else {
		m.notice("自動Relay OFF", nil)
	}
	return nil
}

func (m *Model) openLog() tea.Cmd {
	tmux, dir, file := m.tmux, m.ws.Dir, m.relay.LogFile()
	return func() tea.Msg {
		if err := tmux.Popup(dir, "touch "+shellQuote(file)+" && LESSCHARSET=utf-8 less -R +G "+shellQuote(file)); err != nil {
			return noticeMsg{text: "ログを開けませんでした", err: err}
		}
		return nil
	}
}

func (m *Model) detach() tea.Cmd {
	tmux := m.tmux
	return func() tea.Msg {
		if err := tmux.Detach(); err != nil {
			return noticeMsg{text: "デタッチできませんでした", err: err}
		}
		return nil
	}
}

func (m *Model) restart(member Member) tea.Cmd {
	ws, tmux, env := m.ws, m.tmux, m.env
	return func() tea.Msg {
		wm, _ := ws.Member(member.Agent.Name())
		if err := tmux.Respawn(member.Pane, wm.Dir, dashboard.AgentCommand(ws, wm, env)); err != nil {
			return noticeMsg{text: member.Agent.Label() + " を再起動できませんでした", err: err}
		}
		return noticeMsg{text: "🔄 " + member.Agent.Label() + " を再起動しました"}
	}
}

// openFolder opens the folder dialog, starting at the current workspace.
func (m *Model) openFolder() tea.Cmd {
	pick, dir := m.pickFolder, m.ws.Dir
	m.closing, m.force, m.switching = false, false, nil
	m.notice("📂 フォルダを選んでください（Windows のダイアログが開きます）", nil)
	return func() tea.Msg {
		chosen, err := pick(context.Background(), "claude-squad で作業するフォルダを選んでください", dir)
		return folderPickedMsg{dir: chosen, err: err}
	}
}

// planSwitch turns the chosen folder into a switch to confirm.
func (m *Model) planSwitch(msg folderPickedMsg) {
	switch {
	case errors.Is(msg.err, picker.ErrCancelled):
		m.notice("フォルダ選択をやめました", nil)
		return
	case msg.err != nil:
		m.notice("フォルダを選べませんでした", msg.err)
		return
	}
	kind, root, err := workspace.Inspect(msg.dir)
	noCommits := errors.Is(err, workspace.ErrNoCommits)
	if err != nil && !noCommits {
		m.notice("そのフォルダは開けません", err)
		return
	}
	if root == m.ws.Dir {
		m.notice("📂 今と同じフォルダです", nil)
		return
	}
	m.message = ""
	m.switching = &switchPlan{dir: root, kind: kind, noCommits: noCommits}
}

// switchTo stops this dashboard and has the attached claude-squad reopen it
// on the planned folder, after preparing the folder with prepare.
func (m *Model) switchTo(prepare func(string) error) tea.Cmd {
	plan, tmux, configDir := m.switching, m.tmux, m.configDir
	m.switching = nil
	return func() tea.Msg {
		if prepare != nil {
			if err := prepare(plan.dir); err != nil {
				return noticeMsg{text: "切り替えの準備に失敗しました", err: err}
			}
		}
		if err := workspace.RequestSwitch(configDir, plan.dir); err != nil {
			return noticeMsg{text: "切り替えられませんでした", err: err}
		}
		if err := tmux.Kill(); err != nil {
			return noticeMsg{text: "切り替えられませんでした", err: err}
		}
		return nil
	}
}

// closeWorkspace stops every agent and removes the worktrees, keeping the
// branches. Worktrees with uncommitted changes stop it unless forced.
func (m *Model) closeWorkspace() tea.Cmd {
	ws, state, tmux, force := m.ws, m.state, m.tmux, m.force
	return func() tea.Msg {
		var dirty *workspace.DirtyError
		if err := workspace.Close(state, ws, force); errors.As(err, &dirty) {
			return closeFailedMsg{err: err}
		} else if err != nil {
			log.WarningLog.Printf("close workspace: %v", err)
		}
		if err := tmux.Kill(); err != nil {
			return noticeMsg{text: "終了できませんでした", err: err}
		}
		return nil
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
