package panel

import (
	"claude-squad/cmd/cmd_test"
	"claude-squad/config"
	"claude-squad/dashboard"
	"claude-squad/picker"
	"claude-squad/relay"
	"claude-squad/workspace"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

type testPanel struct {
	*Model
	term   *fakeTerminal
	toasts []string
}

func newTestModel(t *testing.T, width, height int) *testPanel {
	f := newRelayFixture(t, claudeMember, codexMember)
	ws := &workspace.Workspace{Dir: "/repo", Kind: workspace.KindGit, Members: []workspace.Member{
		{Agent: "claude", Dir: claudeMember.Dir, Branch: "t/claude", Program: "claude"},
		{Agent: "codex", Dir: codexMember.Dir, Branch: "t/codex", Program: "codex"},
	}}
	state := config.LoadStateFrom(filepath.Join(t.TempDir(), "state.json"))
	p := &testPanel{term: f.term}
	p.Model = newModel(ws, state, nil, f.relay, []Member{claudeMember, codexMember}, relay.LaunchEnv{}, t.TempDir(),
		func(s string) { p.toasts = append(p.toasts, s) })
	p.Update(tea.WindowSizeMsg{Width: width, Height: height})
	p.now = time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)
	return p
}

func (p *testPanel) plain() string {
	return xansi.Strip(p.View())
}

// click presses the left button on the first occurrence of label.
func (p *testPanel) click(t *testing.T, label string) tea.Cmd {
	t.Helper()
	lines := strings.Split(p.plain(), "\n")
	for y, line := range lines {
		col := strings.Index(line, label)
		if col < 0 {
			continue
		}
		_, cmd := p.Update(tea.MouseMsg{X: lipgloss.Width(line[:col]), Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		return cmd
	}
	t.Fatalf("no %q on screen:\n%s", label, strings.Join(lines, "\n"))
	return nil
}

func TestViewFitsThePane(t *testing.T) {
	for _, size := range [][2]int{{110, 30}, {60, 14}, {40, 10}} {
		p := newTestModel(t, size[0], size[1])
		lines := strings.Split(p.View(), "\n")
		require.Len(t, lines, size[1], "%v", size)
		for _, line := range lines {
			require.LessOrEqual(t, lipgloss.Width(line), size[0], "%v: %q", size, xansi.Strip(line))
		}
	}
	small := newTestModel(t, 60, 13).plain()
	for _, want := range []string{"⚡自動OFF", "🚪抜ける", "💥閉じる", compactIdleHint, "🔄再起動"} {
		require.Contains(t, small, want, "short labels fit a narrow panel")
	}
	plain := newTestModel(t, 110, 30).plain()
	for _, want := range []string{"✦ CLAUDE SQUAD ✦  📂作業フォルダを開く  /repo (git worktree)", "🧠 Claude Code", "🤖 Codex", "💤待機", "まだ取得していません",
		"🔄再起動", "📣 クリックして全員に一斉送信", "🚀全員に送信", "⚡自動Relay OFF",
		"🔄使用量", "🚪デタッチ", "💥ワークスペースを閉じる", idleHint} {
		require.Contains(t, plain, want)
	}
	require.Contains(t, newTestModel(t, 140, 30).plain(), keyHelp, "key help shows when it fits")
	require.Contains(t, newTestModel(t, 110, 4).plain(), "💥ワークスペースを閉じる", "the buttons stay when rows are cut")
	for _, gone := range []string{"Lv.", "XP", "通算"} {
		require.NotContains(t, plain, gone)
	}
}

func TestClickingTogglesAutoRelay(t *testing.T) {
	p := newTestModel(t, 110, 30)
	require.Nil(t, p.click(t, "⚡自動Relay OFF"))
	require.True(t, p.relay.Status().AutoRelay)
	require.Contains(t, p.plain(), "⚡自動Relay ON 0/6")
	p.Update(key("a"))
	require.False(t, p.relay.Status().AutoRelay, "a toggles it back")
}

func TestBroadcastIsTypedAndCelebrated(t *testing.T) {
	p := newTestModel(t, 110, 30)
	p.click(t, "クリックして全員に一斉送信")
	require.True(t, p.input.Focused())
	for _, r := range "テスト" {
		p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := p.Update(key("enter"))
	require.NotNil(t, cmd)
	cmd()
	require.Equal(t, []string{"テスト"}, p.term.pasted[claudePane])
	require.Equal(t, []string{"テスト"}, p.term.pasted[codexPane])
	require.Empty(t, p.input.Value())

	p.Update(statusTickMsg{})
	require.Contains(t, p.plain(), "📣 全員に一斉送信！")

	_, cmd = p.Update(key("enter"))
	require.Nil(t, cmd, "nothing to send")
	p.Update(key("esc"))
	require.False(t, p.input.Focused())
}

func TestEventsShowAsBannersAndDeliveriesToast(t *testing.T) {
	p := newTestModel(t, 110, 30)
	p.celebrate(LogEntry{Kind: EventAsk, Agent: relay.AgentClaude, Count: 2})
	require.Contains(t, p.plain(), "📤 🧠 Claude Code が 2人に相談！")
	p.celebrate(LogEntry{Kind: EventAnswer, Agent: relay.AgentCodex})
	require.Contains(t, p.plain(), "📥 🤖 Codex が回答！")
	require.Empty(t, p.toasts, "only deliveries flash on the status line")
	p.celebrate(LogEntry{Kind: EventDeliver, Agent: relay.AgentClaude, Count: 2})
	require.Equal(t, []string{"🎉 🧠 Claude Code に意見が届いた！"}, p.toasts)
	require.Contains(t, p.plain(), "🎉 🧠 Claude Code に意見が届いた！")
}

func TestWarningsShowWithoutScore(t *testing.T) {
	p := newTestModel(t, 110, 30)
	p.celebrate(LogEntry{Kind: EventWarn, Text: "@gemini はこのワークスペースで動いていません"})
	require.Contains(t, p.plain(), "🚨 @gemini はこのワークスペースで動いていません")
	p.celebrate(LogEntry{Kind: EventInfo, Text: "info"})
	require.Contains(t, p.plain(), "🚨 @gemini", "info does not replace the banner")
	p.now = p.now.Add(bannerLifetime + time.Second)
	require.Contains(t, p.plain(), idleHint, "the banner goes away")
}

func TestUsageShowsHealthBarsAndIgnoresStaleRefreshes(t *testing.T) {
	p := newTestModel(t, 110, 30)
	usage := relay.Usage{Updated: p.now.Add(-3 * time.Hour), Windows: []relay.UsageWindow{
		{Name: "5h", Remaining: 0.5, ResetsAt: p.now.Add(90 * time.Minute)},
		{Name: "週", Remaining: 0.9, ResetsAt: p.now.Add(50 * time.Hour)},
	}}
	_, cmd := p.Update(usageMsg{agent: relay.AgentCodex, usage: usage})
	require.NotNil(t, cmd, "the next refresh is scheduled")
	plain := p.plain()
	require.Contains(t, plain, "5h ▰▰▰▱▱▱  50% 1h30m")
	require.Regexp(t, `🤖 Codex +💤待機 +🔄再起動 +5h ▰▰▰▱▱▱  50%`, plain, "state and usage share the agent's row")
	require.Contains(t, plain, "週 ▰▰▰▰▰▱  90% 2d2h")
	require.Contains(t, plain, "(3時間前)")

	narrow := newTestModel(t, 60, 14)
	narrow.Update(usageMsg{agent: relay.AgentCodex, usage: usage})
	require.NotContains(t, narrow.plain(), "1h30m", "reset times only show when there is room")

	p.Update(usageMsg{agent: relay.AgentClaude, err: errors.New("boom")})
	require.Contains(t, p.plain(), "🚨 取得できません: boom")

	p.refreshUsage()
	_, cmd = p.Update(usageMsg{agent: relay.AgentCodex, gen: 0, err: errors.New("stale")})
	require.Nil(t, cmd)
	require.Contains(t, p.plain(), "50%", "a reply to a superseded refresh is dropped")
	_, cmd = p.Update(usageTickMsg{agent: relay.AgentCodex, gen: 0})
	require.Nil(t, cmd)
}

func TestUsageShowsAPrepaidBalance(t *testing.T) {
	p := newTestModel(t, 110, 30)
	p.Update(usageMsg{agent: relay.AgentCodex, usage: relay.Usage{Updated: p.now, Balances: []relay.Balance{
		{Amount: "9.87", Currency: "USD", Available: true},
	}}})
	require.Contains(t, p.plain(), "残高 $9.87")
	require.NotContains(t, p.plain(), "チャージが必要です")

	p.Update(usageMsg{agent: relay.AgentCodex, usage: relay.Usage{Updated: p.now, Balances: []relay.Balance{
		{Amount: "0.00", Currency: "CNY", Available: false},
	}}})
	require.Contains(t, p.plain(), "残高 ¥0.00 💸 チャージが必要です")
}

func TestAgentStates(t *testing.T) {
	p := newTestModel(t, 110, 30)
	p.Update(panesMsg{dead: map[relay.Agent]bool{relay.AgentCodex: true}})
	p.status.Busy = map[relay.Agent]bool{relay.AgentClaude: true}
	plain := p.plain()
	require.Contains(t, plain, "🧠 Claude Code ⠋ 作業中  🔄再起動")
	require.Contains(t, plain, "🤖 Codex       💀停止   🔄再起動")
	for _, gone := range []string{"差分", "マージ", "push", "+0", "🔍", "🔀"} {
		require.NotContains(t, plain, gone)
	}
}

func TestFolderButtonSwitchesAfterConfirming(t *testing.T) {
	p := newTestModel(t, 110, 30)
	next := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", next, "init", "-q").Run())
	require.NoError(t, exec.Command("git", "-C", next, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "i").Run())
	var asked string
	p.pickFolder = func(_ context.Context, _, initial string) (string, error) {
		asked = initial
		return next, nil
	}
	var ran []string
	p.tmux = dashboard.New(cmd_test.MockCmdExec{
		RunFunc:    func(c *exec.Cmd) error { ran = append(ran, strings.Join(c.Args[3:], " ")); return nil },
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return nil, nil },
	})

	cmd := p.click(t, "📂作業フォルダを開く")
	require.Contains(t, p.plain(), "Windows のダイアログが開きます")
	p.Update(cmd())
	require.Equal(t, "/repo", asked, "the dialog starts at the current workspace")
	require.Contains(t, p.plain(), next+" に切り替えますか？")
	require.Empty(t, ran, "nothing stops before confirming")

	p.click(t, "やめる")
	require.Nil(t, p.switching)

	p.Update(p.openFolder()())
	cmd = p.click(t, "🔀切り替える")
	require.Nil(t, cmd())
	require.Equal(t, []string{"kill-server"}, ran)
	dir, ok := workspace.TakeSwitch(p.configDir)
	require.True(t, ok)
	require.Equal(t, next, dir, "the attached claude-squad is asked to reopen on the folder")
}

func TestFolderChoicesThatNeedPreparing(t *testing.T) {
	p := newTestModel(t, 110, 30)
	plain := t.TempDir()
	p.Update(folderPickedMsg{dir: plain})
	view := p.plain()
	require.Contains(t, view, "git で管理されていないフォルダです")
	require.Contains(t, view, "🌿git管理にしてAIごとに作業コピー")
	require.Contains(t, view, "🤝全員で直接共有")

	empty := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", empty, "init", "-q").Run())
	p.Update(folderPickedMsg{dir: empty})
	require.Contains(t, p.plain(), "✨初期コミットを作って切り替え")

	p.Update(folderPickedMsg{err: picker.ErrCancelled})
	require.Contains(t, p.plain(), "フォルダ選択をやめました")
	p.Update(folderPickedMsg{err: picker.ErrUnsupported})
	require.Contains(t, p.plain(), "フォルダを選べませんでした")
	p.Update(folderPickedMsg{dir: filepath.Join(plain, "missing")})
	require.Contains(t, p.plain(), "そのフォルダは開けません")
	p.Update(folderPickedMsg{dir: "/repo"})
}

func TestNarrowPanelFolderButton(t *testing.T) {
	require.Contains(t, newTestModel(t, 60, 13).plain(), "📂フォルダ")
}

func TestClosingAsksFirst(t *testing.T) {
	p := newTestModel(t, 110, 30)
	p.click(t, "💥ワークスペースを閉じる")
	require.True(t, p.closing)
	require.Contains(t, p.plain(), "ブランチは残ります")
	p.click(t, "やめる")
	require.False(t, p.closing)

	p.Update(closeFailedMsg{err: &workspace.DirtyError{Dirs: []string{"/wt/claude"}}})
	plain := p.plain()
	require.Contains(t, plain, "💥それでも閉じる")
	require.Contains(t, plain, "/wt/claude")
}

func TestNoticesExpire(t *testing.T) {
	p := newTestModel(t, 110, 30)
	p.Update(noticeMsg{text: "done"})
	require.Contains(t, p.plain(), "done")
	p.messageAt = time.Now().Add(-messageLifetime - time.Second)
	p.Update(statusTickMsg{})
	require.NotContains(t, p.plain(), "done")
}

func TestScreenZonesFollowWideCharacters(t *testing.T) {
	s := &screen{width: 20}
	hit := false
	r := s.row().plain("日本語")
	r.button("押す", button(accent), func() tea.Cmd { hit = true; return nil })
	r.button("見えない", button(accent), func() tea.Cmd { return nil })
	_, zones := s.render(5)
	require.Len(t, zones, 2, "a button cut off at the edge keeps its visible part")
	require.Equal(t, [3]int{0, 6, 12}, [3]int{zones[0].row, zones[0].x0, zones[0].x1})
	require.Equal(t, 20, zones[1].x1)
	zones[0].action()
	require.True(t, hit)

	s = &screen{width: 5}
	s.row().plain("abcde").button("x", button(accent), nil)
	_, zones = s.render(5)
	require.Empty(t, zones, "a button past the edge is not clickable")
}

func TestButtonsWinOverClickableRows(t *testing.T) {
	p := newTestModel(t, 110, 30)
	p.click(t, "🚀全員に送信")
	require.False(t, p.input.Focused(), "the send button sends instead of focusing the box")
}

func TestFormatting(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	require.Equal(t, "", countdown(time.Time{}, now))
	require.Equal(t, "リセット済", countdown(now.Add(-time.Minute), now))
	require.Equal(t, "45m", countdown(now.Add(44*time.Minute+10*time.Second), now))
	require.Equal(t, "2h05m", countdown(now.Add(125*time.Minute), now))
	require.Equal(t, "3d4h", countdown(now.Add(76*time.Hour), now))
	require.Equal(t, "", age(now.Add(-10*time.Minute), now))
	require.Equal(t, "(2時間前)", age(now.Add(-2*time.Hour), now))
	require.Equal(t, "(3日前)", age(now.Add(-75*time.Hour), now))
	require.Equal(t, "▰▰▰▰", xansi.Strip(hpBar(1, 4)))
	require.Equal(t, "▱▱▱▱", xansi.Strip(hpBar(0, 4)))
	require.Equal(t, hot, hpColor(0.1))
	require.Equal(t, gold, hpColor(0.3))
	require.Equal(t, mint, hpColor(0.8))
	require.Equal(t, "ABC", xansi.Strip(rainbowText("ABC", 7, true)))
	require.Equal(t, 3, lipgloss.Width(confettiText(3, 1)))
	require.Equal(t, "日本  ", padRight("日本", 6))
	require.Equal(t, "toolong", padRight("toolong", 3))
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestRestartUsesTheCurrentConfig(t *testing.T) {
	p := newTestModel(t, 110, 30)
	statePath := filepath.Join(t.TempDir(), "state.json")
	p.state = config.LoadStateFrom(statePath)
	cfg := `{"profiles":[{"name":"codex","program":"/opt/codex --dangerously-bypass-approvals-and-sandbox"}]}`
	require.NoError(t, os.WriteFile(filepath.Join(p.configDir, config.ConfigFileName), []byte(cfg), 0o644))
	var ran []string
	p.tmux = dashboard.New(cmd_test.MockCmdExec{
		RunFunc:    func(c *exec.Cmd) error { ran = append(ran, strings.Join(c.Args[3:], " ")); return nil },
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return nil, nil },
	})

	p.Update(p.restart(codexMember)())
	require.Len(t, ran, 1)
	require.Contains(t, ran[0], "respawn-pane")
	require.Contains(t, ran[0], "/opt/codex --dangerously-bypass-approvals-and-sandbox")
	require.Contains(t, p.plain(), "Codex を再起動しました")

	saved, err := workspace.Load(config.LoadStateFrom(statePath))
	require.NoError(t, err)
	codex, _ := saved.Member("codex")
	require.Equal(t, "/opt/codex --dangerously-bypass-approvals-and-sandbox", codex.Program, "the saved workspace follows")
	claude, _ := saved.Member("claude")
	require.Equal(t, "claude", claude.Program, "other agents keep their command until they restart")
}
