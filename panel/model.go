// Package panel is the dashboard's control pane: usage meters, per-agent
// buttons, a broadcast box and the relay that carries @tag questions between
// the agents' panes.
package panel

import (
	"claude-squad/config"
	"claude-squad/dashboard"
	"claude-squad/log"
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
	diffInterval   = 5 * time.Second
	// messageLifetime is how long a one-line notice stays in the footer.
	messageLifetime = 8 * time.Second
	meterCells      = 8
	// labelWidth aligns the agent names in the usage and agent sections.
	labelWidth = 12
	// broadcastHint fills the empty broadcast box.
	broadcastHint = "ここをクリックして全AIに同じ文を送る（Enterで送信）"
)

type usageState struct {
	usage relay.Usage
	err   error
	// gen tells a scheduled refresh from one made obsolete by a manual refresh.
	gen int
}

type diffState struct {
	added, removed int
	err            error
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

	width, height int
	input         textinput.Model
	usage         map[relay.Agent]*usageState
	diffs         map[relay.Agent]diffState
	dead          map[relay.Agent]bool
	status        Status
	now           time.Time

	message   string
	messageAt time.Time
	// closing is set while the user confirms closing the workspace; force
	// is set when closing must skip worktrees with uncommitted changes.
	closing bool
	force   bool

	zones []zone
}

type statusTickMsg struct{}

type diffTickMsg struct{}

type diffMsg struct {
	diffs map[relay.Agent]diffState
	dead  map[relay.Agent]bool
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

// noticeMsg shows the outcome of a button in the footer.
type noticeMsg struct {
	text string
	err  error
}

// closeFailedMsg means closing the workspace kept some worktrees.
type closeFailedMsg struct{ err error }

func newModel(ws *workspace.Workspace, state *config.State, tmux *dashboard.Tmux, r *Relay, members []Member,
	env relay.LaunchEnv, configDir string) *Model {
	input := textinput.New()
	input.Prompt = "> "
	m := &Model{
		ws: ws, state: state, tmux: tmux, relay: r, members: members, env: env, configDir: configDir,
		input: input,
		usage: map[relay.Agent]*usageState{},
		diffs: map[relay.Agent]diffState{},
		dead:  map[relay.Agent]bool{},
		now:   time.Now(),
	}
	for _, member := range members {
		m.usage[member.Agent] = &usageState{err: relay.ErrNoUsage}
	}
	return m
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.statusTick(), m.refreshDiffs()}
	for _, member := range m.members {
		cmds = append(cmds, m.fetchUsage(member.Agent, 0))
	}
	return tea.Batch(cmds...)
}

func (m *Model) statusTick() tea.Cmd {
	return tea.Tick(statusInterval, func(time.Time) tea.Msg { return statusTickMsg{} })
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
	m.notice("使用量を更新しています…", nil)
	return tea.Batch(cmds...)
}

func (m *Model) refreshDiffs() tea.Cmd {
	ws, tmux, members := m.ws, m.tmux, m.members
	return func() tea.Msg {
		msg := diffMsg{diffs: map[relay.Agent]diffState{}, dead: map[relay.Agent]bool{}}
		if ws.Kind == workspace.KindGit {
			for _, member := range members {
				wm, ok := ws.Member(member.Agent.Name())
				if !ok {
					continue
				}
				added, removed, err := ws.DiffStats(wm)
				msg.diffs[member.Agent] = diffState{added: added, removed: removed, err: err}
			}
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
		m.input.Width = max(m.width-12, 10)
	case statusTickMsg:
		m.now = time.Now()
		m.status = m.relay.Status()
		if m.message != "" && m.now.Sub(m.messageAt) > messageLifetime {
			m.message = ""
		}
		return m, m.statusTick()
	case diffMsg:
		m.diffs, m.dead = msg.diffs, msg.dead
		return m, tea.Tick(diffInterval, func(time.Time) tea.Msg { return diffTickMsg{} })
	case diffTickMsg:
		return m, m.refreshDiffs()
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
	case closeFailedMsg:
		m.closing, m.force = true, true
		m.notice("まだ閉じていません", msg.err)
	case tea.MouseMsg:
		return m, m.handleMouse(msg)
	case tea.KeyMsg:
		return m, m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return nil
	}
	for _, z := range m.zones {
		if z.row == msg.Y && msg.X >= z.x0 && msg.X < z.x1 {
			return z.action()
		}
	}
	return nil
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
		return noticeMsg{text: "全員に送信しました"}
	}
}

func (m *Model) toggleAutoRelay() tea.Cmd {
	on := !m.status.AutoRelay
	m.relay.SetAutoRelay(on)
	m.status = m.relay.Status()
	if on {
		m.notice(fmt.Sprintf("自動Relay ON: AIの返答の@タグも相手に聞きます（%d回まで）", autoRelayLimit), nil)
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

func (m *Model) showDiff(member Member) tea.Cmd {
	ws, tmux := m.ws, m.tmux
	return func() tea.Msg {
		wm, _ := ws.Member(member.Agent.Name())
		command, err := ws.DiffCommand(wm)
		if err == nil {
			err = tmux.Popup(wm.Dir, command)
		}
		if err != nil {
			return noticeMsg{text: member.Agent.Label() + " の差分を開けませんでした", err: err}
		}
		return nil
	}
}

func (m *Model) merge(member Member) tea.Cmd {
	ws := m.ws
	m.notice(member.Agent.Label()+" のブランチをマージしています…", nil)
	return func() tea.Msg {
		wm, _ := ws.Member(member.Agent.Name())
		if err := ws.Merge(wm); err != nil {
			return noticeMsg{text: member.Agent.Label() + " のマージを中断しました（" + ws.Dir + " で解決してください）", err: err}
		}
		return noticeMsg{text: member.Agent.Label() + " のブランチをマージしました"}
	}
}

func (m *Model) push(member Member) tea.Cmd {
	ws := m.ws
	m.notice(member.Agent.Label()+" のブランチを push しています…", nil)
	return func() tea.Msg {
		wm, _ := ws.Member(member.Agent.Name())
		if err := ws.Push(wm); err != nil {
			return noticeMsg{text: member.Agent.Label() + " を push できませんでした", err: err}
		}
		return noticeMsg{text: member.Agent.Label() + " のブランチを push しました"}
	}
}

func (m *Model) restart(member Member) tea.Cmd {
	ws, tmux, env := m.ws, m.tmux, m.env
	return func() tea.Msg {
		wm, _ := ws.Member(member.Agent.Name())
		if err := tmux.Respawn(member.Pane, wm.Dir, dashboard.AgentCommand(wm, env)); err != nil {
			return noticeMsg{text: member.Agent.Label() + " を再起動できませんでした", err: err}
		}
		return noticeMsg{text: member.Agent.Label() + " を再起動しました"}
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

func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	s := &screen{width: m.width}
	m.viewTitle(s)
	m.viewUsage(s)
	m.viewAgents(s)
	m.viewBroadcast(s)
	footer := m.footer()
	m.viewRelay(s, m.height-len(s.rows)-len(footer))
	s.rows = append(s.rows, footer...)
	view, zones := s.render(m.height)
	m.zones = zones
	return view
}

func (m *Model) viewTitle(s *screen) {
	kind := "git worktree"
	switch {
	case m.ws.Scratch:
		kind = "使い捨て"
	case m.ws.Kind == workspace.KindShared:
		kind = "共有ディレクトリ"
	}
	s.row().add("claude-squad ", titleStyle).add(m.ws.Dir+" ", dimStyle).add("("+kind+")", dimStyle)
}

func (m *Model) viewUsage(s *screen) {
	s.header("残り使用量", func(r *row) { r.button("更新", buttonStyle, m.refreshUsage) })
	for _, member := range m.members {
		state := m.usage[member.Agent]
		r := s.row().plain(padRight(member.Agent.Label(), labelWidth))
		switch {
		case state.err != nil && errors.Is(state.err, relay.ErrNoUsage):
			r.add("まだ取得していません（一度やり取りすると表示）", dimStyle)
			continue
		case state.err != nil:
			r.add("取得できません: "+state.err.Error(), errorStyle)
			continue
		}
		// Two windows fit on a line; more wrap under the name.
		for i, w := range state.usage.Windows {
			if i > 0 && i%2 == 0 {
				r = s.row().plain(strings.Repeat(" ", labelWidth))
			}
			r.plain(w.Name+" ").plain(meter(w.Remaining, meterCells)).
				plain(fmt.Sprintf(" %3.0f%% ", w.Remaining*100)).add(padRight(countdown(w.ResetsAt, m.now), 10)+" ", dimStyle)
		}
		if a := age(state.usage.Updated, m.now); a != "" {
			r.add(a, dimStyle)
		}
	}
}

func (m *Model) viewAgents(s *screen) {
	s.header("エージェント")
	for _, member := range m.members {
		member := member
		r := s.row()
		switch {
		case m.dead[member.Agent]:
			r.add("■ ", errorStyle).plain(padRight(member.Agent.Label(), labelWidth)).add(padRight("停止", 7), errorStyle)
		case m.status.Busy[member.Agent]:
			r.add("● ", busyStyle).plain(padRight(member.Agent.Label(), labelWidth)).add(padRight("作業中", 7), busyStyle)
		default:
			r.add("○ ", idleStyle).plain(padRight(member.Agent.Label(), labelWidth)).add(padRight("待機", 7), idleStyle)
		}
		if m.ws.Kind == workspace.KindGit {
			d := m.diffs[member.Agent]
			if d.err == nil {
				r.add(fmt.Sprintf("+%d", d.added), addedStyle).plain(" ").add(padRight(fmt.Sprintf("-%d", d.removed), 6), removedStyle)
			} else {
				r.plain(padRight("", 8))
			}
			r.button("差分", buttonStyle, func() tea.Cmd { return m.showDiff(member) })
			r.button("マージ", buttonStyle, func() tea.Cmd { return m.merge(member) })
			r.button("push", buttonStyle, func() tea.Cmd { return m.push(member) })
		}
		r.button("再起動", buttonStyle, func() tea.Cmd { return m.restart(member) })
	}
}

func (m *Model) viewBroadcast(s *screen) {
	s.header("一斉送信")
	r := s.row()
	focus := func() tea.Cmd { return m.input.Focus() }
	r.zones = append(r.zones, zone{x0: 0, x1: m.width, action: focus})
	if m.input.Focused() || m.input.Value() != "" {
		r.plain(m.input.View())
	} else {
		// textinput's own placeholder breaks on wide characters, so the hint
		// is drawn here.
		r.plain(m.input.Prompt).add(broadcastHint, dimStyle)
	}
	r = s.row()
	r.button("全員に送信", buttonStyle, m.broadcast)
	r.add("@タグはAIのペインで先頭に書く（例: @codex どう思う？ / @all）", dimStyle)
}

func (m *Model) viewRelay(s *screen, rows int) {
	label := "自動Relay: OFF"
	style := buttonStyle
	if m.status.AutoRelay {
		label = fmt.Sprintf("自動Relay: ON (%d/%d)", m.status.AutoCount, autoRelayLimit)
		style = activeButtonStyle
	}
	s.header("Relay", func(r *row) {
		r.button(label, style, m.toggleAutoRelay)
		r.button("ログ全文", buttonStyle, m.openLog)
	})
	rows--
	if rows <= 0 {
		return
	}
	lines := make([]string, 0, len(m.status.Pending)+len(m.status.Log))
	for _, p := range m.status.Pending {
		lines = append(lines, "⏳ "+p)
	}
	entries := m.status.Log
	if n := rows - len(lines); n < len(entries) {
		entries = entries[max(len(entries)-n, 0):]
	}
	for _, e := range entries {
		lines = append(lines, e.Time.Format("15:04")+" "+e.Text)
	}
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	for _, line := range lines {
		s.row().plain(line)
	}
	for i := len(lines); i < rows; i++ {
		s.row()
	}
}

// footer is the notice line, when there is a notice, and the button line.
func (m *Model) footer() []*row {
	var rows []*row
	if m.message != "" {
		rows = append(rows, (&row{}).add(m.message, busyStyle))
	}
	r := &row{}
	if m.closing {
		if m.force {
			r.plain("未コミットの変更がある worktree は残して閉じます ")
			r.button("それでも閉じる", dangerButtonStyle, m.closeWorkspace)
		} else {
			r.plain("全AIを止めて worktree を片付けます（ブランチは残ります） ")
			r.button("閉じる", dangerButtonStyle, m.closeWorkspace)
		}
		r.button("やめる", buttonStyle, func() tea.Cmd { m.closing, m.force = false, false; return nil })
	} else {
		r.button("デタッチ", buttonStyle, m.detach)
		r.button("ワークスペースを閉じる", dangerButtonStyle, func() tea.Cmd { m.closing = true; return nil })
		r.add("i:入力 a:自動Relay u:使用量 l:ログ d:デタッチ", dimStyle)
	}
	return append(rows, r)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
