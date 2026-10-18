package panel

import (
	"claude-squad/relay"
	"claude-squad/workspace"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	// nameWidth aligns the agent names.
	nameWidth = 12
	hpCells   = 6
	// wideWidth is the panel width from which reset times are shown;
	// below compactWidth, labels get shorter.
	wideWidth    = 100
	compactWidth = 80

	broadcastHint   = "クリックして全員に一斉送信"
	compactIdleHint = "💡 先頭に @codex や @all で相談！"
	keyHelp         = "i:入力 a:自動Relay u:使用量 l:ログ d:デタッチ"
)

// idleHint lists every agent's tag, so a new agent shows up in it.
var idleHint = func() string {
	var tags []string
	for _, a := range relay.Agents() {
		tags = append(tags, a.Tag())
	}
	return "💡 AIのペインで先頭に " + strings.Join(append(tags, "@all"), " / ") + " と書くと相談できる"
}()

var eventIcons = map[EventKind]string{
	EventInfo:      "💬",
	EventWarn:      "🚨",
	EventAsk:       "📤",
	EventAnswer:    "📥",
	EventDeliver:   "🎯",
	EventBroadcast: "📣",
}

// compact reports whether the panel is too narrow for the long labels.
func (m *Model) compact() bool {
	return m.width < compactWidth
}

// label picks the long or the short form for the panel's width.
func (m *Model) label(long, short string) string {
	if m.compact() {
		return short
	}
	return long
}

func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	s := &screen{width: m.width}
	m.viewTitle(s)
	for _, member := range m.members {
		m.viewUsage(s, member.Agent)
	}
	for _, member := range m.members {
		m.viewAgent(s, member)
	}
	m.viewBanner(s)
	m.viewBroadcast(s)
	footer := m.footer()
	m.viewLog(s, m.height-len(s.rows)-len(footer))
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
	r := s.row().raw(rainbowText("✦ CLAUDE SQUAD ✦", m.frame, true)).plain(" ")
	r.button(m.label("📂作業フォルダを開く", "📂フォルダ"), button(gold), m.openFolder)
	r.add(m.ws.Dir+" ", dimStyle).add("("+kind+")", fg(violet))
}

// name is an agent's emoji and name in its color.
func (m *Model) name(r *row, agent relay.Agent) {
	r.plain(agent.Emoji()+" ").add(padRight(agent.Label(), nameWidth), fg(agent.Color()).Bold(true))
}

func (m *Model) viewUsage(s *screen, agent relay.Agent) {
	r := s.row()
	m.name(r, agent)
	state := m.usage[agent]
	switch {
	case state == nil:
	case errors.Is(state.err, relay.ErrNoUsage):
		r.add("❔ まだ取得していません（一度やり取りすると表示）", dimStyle)
	case state.err != nil:
		r.add("🚨 取得できません: "+state.err.Error(), errorStyle)
	default:
		for i, w := range state.usage.Windows {
			if i > 0 && i%2 == 0 {
				r = s.row().plain(padRight("", nameWidth+3))
			}
			r.add(w.Name+" ", dimStyle).raw(hpBar(w.Remaining, hpCells))
			pct := fg(hpColor(w.Remaining)).Bold(true)
			if w.Remaining < 0.2 && m.frame%4 < 2 {
				pct = pct.Reverse(true)
			}
			r.add(fmt.Sprintf("%4.0f%% ", w.Remaining*100), pct)
			if m.width >= wideWidth {
				r.add(padRight(countdown(w.ResetsAt, m.now), 10), dimStyle)
			}
		}
		for _, b := range state.usage.Balances {
			r.add("残高 ", dimStyle)
			amount := fg(mint).Bold(true)
			if !b.Available {
				amount = fg(hot).Bold(true)
			}
			r.add(b.Symbol()+b.Amount+" ", amount)
			if !b.Available {
				r.add("💸 チャージが必要です ", errorStyle)
			}
		}
		if a := age(state.usage.Updated, m.now); a != "" && m.width >= wideWidth {
			r.add(a, dimStyle)
		}
	}
}

func (m *Model) viewAgent(s *screen, member Member) {
	agent := member.Agent
	r := s.row()
	m.name(r, agent)
	spin := spinner[m.frame%len(spinner)]
	switch {
	case m.dead[agent]:
		r.add("💀停止  ", errorStyle)
	case m.status.Busy[agent]:
		r.add(spin+" 作業中 ", fg(agent.Color()).Bold(true))
	default:
		r.add("💤待機  ", dimStyle)
	}
	r.button("🔄再起動", button(agent.Color()), func() tea.Cmd { return m.restart(member) })
}

func (m *Model) viewBanner(s *screen) {
	r := s.row()
	switch {
	case m.now.Before(m.banner.until) && m.banner.warn:
		r.add(m.banner.text, errorStyle)
	case m.now.Before(m.banner.until):
		r.raw(confettiText(2, m.frame) + " ").raw(rainbowText(m.banner.text, m.frame, true)).raw(" " + confettiText(2, m.frame+5))
	default:
		r.add(m.label(idleHint, compactIdleHint), dimStyle)
	}
}

func (m *Model) viewBroadcast(s *screen) {
	r := s.row()
	r.clickable(m.width, func() tea.Cmd { return m.input.Focus() })
	send := m.label("🚀全員に送信", "🚀送信")
	r.plain("📣 ")
	if m.input.Focused() || m.input.Value() != "" {
		r.raw(m.input.View())
	} else {
		// textinput's own placeholder breaks on wide characters, so the hint
		// is drawn here, padded to put the send button at the right edge.
		r.add(padRight(broadcastHint, max(m.width-r.width-lipgloss.Width(send)-4, 0)), dimStyle)
	}
	r.plain(" ").button(send, button(accent), m.broadcast)
}

func (m *Model) viewLog(s *screen, rows int) {
	if rows <= 0 {
		return
	}
	var lines []*row
	for _, p := range m.status.Pending {
		lines = append(lines, (&row{}).add("⏳ "+p, fg(gold)))
	}
	entries := m.status.Log
	if n := rows - len(lines); n < len(entries) {
		entries = entries[max(len(entries)-n, 0):]
	}
	for _, e := range entries {
		style := lipgloss.NewStyle()
		switch e.Kind {
		case EventWarn:
			style = fg(hot)
		case EventDeliver:
			style = fg(mint)
		}
		if m.now.Sub(e.Time) < freshLogLifetime {
			style = style.Bold(true).Foreground(lipgloss.Color(gold))
		}
		lines = append(lines, (&row{}).plain(eventIcons[e.Kind]+" ").add(e.Time.Format("15:04")+" ", dimStyle).add(e.Text, style))
	}
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	s.rows = append(s.rows, lines...)
	for i := len(lines); i < rows; i++ {
		s.row()
	}
}

// footer is the notice line, when there is a notice, and the button line.
func (m *Model) footer() []*row {
	var rows []*row
	if m.message != "" {
		rows = append(rows, (&row{}).add(m.message, noticeStyle))
	}
	r := &row{}
	if plan := m.switching; plan != nil {
		dir := fg(gold).Bold(true).Render(plan.dir)
		switch {
		case plan.noCommits:
			rows = append(rows, (&row{}).raw("📂 "+dir+noticeStyle.Render(" にはコミットがありません（今のAIは止まります）")))
			r.button("✨初期コミットを作って切り替え", button(gold), func() tea.Cmd { return m.switchTo(workspace.InitialCommit) })
		case plan.kind == workspace.KindShared:
			rows = append(rows, (&row{}).raw("📂 "+dir+noticeStyle.Render(" は git で管理されていないフォルダです（今のAIは止まります）")))
			rows = append(rows, (&row{}).add("🌿 今あるファイルを最初のコミットにして、AIごとに別の作業コピーを作る ／ 🤝 全員でこのフォルダを直接編集する", noticeStyle))
			r.button("🌿git管理にしてAIごとに作業コピー", button(gold), func() tea.Cmd { return m.switchTo(workspace.InitRepo) })
			r.button("🤝全員で直接共有", button(cyan), func() tea.Cmd { return m.switchTo(nil) })
		default:
			rows = append(rows, (&row{}).raw("📂 "+dir+noticeStyle.Render(" に切り替えますか？（今のAIは止まります）")))
			r.button("🔀切り替える", button(gold), func() tea.Cmd { return m.switchTo(nil) })
		}
		r.button("やめる", button(violet), func() tea.Cmd { m.switching = nil; return nil })
		return append(rows, r)
	}
	if m.closing {
		if m.force {
			r.plain("未コミットの変更がある worktree は残して閉じます ")
			r.button("💥それでも閉じる", button(hot), m.closeWorkspace)
		} else {
			r.plain("全AIを止めて worktree を片付けます（ブランチは残ります） ")
			r.button("💥閉じる", button(hot), m.closeWorkspace)
		}
		r.button("やめる", button(violet), func() tea.Cmd { m.closing, m.force = false, false; return nil })
	} else {
		auto, autoStyle := m.label("⚡自動Relay OFF", "⚡自動OFF"), button(violet)
		if m.status.AutoRelay {
			auto = fmt.Sprintf(m.label("⚡自動Relay ON %d/%d", "⚡自動%d/%d"), m.status.AutoCount, autoRelayLimit)
			autoStyle = button(gold)
		}
		r.button(auto, autoStyle, m.toggleAutoRelay)
		r.button(m.label("📜ログ全文", "📜ログ"), button(cyan), m.openLog)
		r.button(m.label("🔄使用量", "🔄"), button(mint), m.refreshUsage)
		r.button(m.label("🚪デタッチ", "🚪抜ける"), button(mint), m.detach)
		r.button(m.label("💥ワークスペースを閉じる", "💥閉じる"), button(hot), func() tea.Cmd { m.closing = true; return nil })
		if r.width+lipgloss.Width(keyHelp) <= m.width {
			r.add(keyHelp, dimStyle)
		}
	}
	return append(rows, r)
}
