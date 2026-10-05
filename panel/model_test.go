package panel

import (
	"claude-squad/config"
	"claude-squad/relay"
	"claude-squad/workspace"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newTestModel(t *testing.T) (*Model, *fakeTerminal) {
	f := newRelayFixture(t, claudeMember, codexMember)
	ws := &workspace.Workspace{Dir: "/repo", Kind: workspace.KindGit, Members: []workspace.Member{
		{Agent: "claude", Dir: claudeMember.Dir, Branch: "t/claude", Program: "claude"},
		{Agent: "codex", Dir: codexMember.Dir, Branch: "t/codex", Program: "codex"},
	}}
	state := config.LoadStateFrom(filepath.Join(t.TempDir(), "state.json"))
	m := newModel(ws, state, nil, f.relay, []Member{claudeMember, codexMember}, relay.LaunchEnv{}, t.TempDir())
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	m.now = time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)
	return m, f.term
}

// click presses the left button on the first zone whose row shows label.
func click(t *testing.T, m *Model, label string) tea.Cmd {
	t.Helper()
	lines := strings.Split(xansi.Strip(m.View()), "\n")
	for row, line := range lines {
		col := strings.Index(line, label)
		if col < 0 {
			continue
		}
		x := lipgloss.Width(line[:col])
		_, cmd := m.Update(tea.MouseMsg{X: x, Y: row, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
		return cmd
	}
	t.Fatalf("no %q on screen:\n%s", label, strings.Join(lines, "\n"))
	return nil
}

func TestViewFitsThePane(t *testing.T) {
	m, _ := newTestModel(t)
	view := m.View()
	lines := strings.Split(view, "\n")
	require.Len(t, lines, 30)
	for _, line := range lines {
		require.LessOrEqual(t, lipgloss.Width(line), 110)
	}
	plain := xansi.Strip(view)
	for _, want := range []string{"claude-squad /repo (git worktree)", "残り使用量", "まだ取得していません", "エージェント", "一斉送信", "Relay", "自動Relay: OFF", "デタッチ"} {
		require.Contains(t, plain, want)
	}
}

func TestClickingTogglesAutoRelay(t *testing.T) {
	m, _ := newTestModel(t)
	require.Nil(t, click(t, m, "自動Relay: OFF"))
	require.True(t, m.relay.Status().AutoRelay)
	require.Contains(t, xansi.Strip(m.View()), "自動Relay: ON (0/6)")
	m.Update(key("a"))
	require.False(t, m.relay.Status().AutoRelay, "a toggles it back")
}

func TestBroadcastFromTheInput(t *testing.T) {
	m, term := newTestModel(t)
	click(t, m, "全AIに同じ文を送る")
	require.True(t, m.input.Focused())
	for _, r := range "テスト" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := m.Update(key("enter"))
	require.NotNil(t, cmd)
	msg := cmd()
	require.Equal(t, noticeMsg{text: "全員に送信しました"}, msg)
	require.Equal(t, []string{"テスト"}, term.pasted[claudePane])
	require.Equal(t, []string{"テスト"}, term.pasted[codexPane])
	require.Empty(t, m.input.Value())
	_, cmd = m.Update(key("enter"))
	require.Nil(t, cmd, "nothing to send")
	m.Update(key("esc"))
	require.False(t, m.input.Focused())
}

func TestUsageShowsMetersAndIgnoresStaleRefreshes(t *testing.T) {
	m, _ := newTestModel(t)
	usage := relay.Usage{Updated: m.now.Add(-3 * time.Hour), Windows: []relay.UsageWindow{
		{Name: "5h", Remaining: 0.25, ResetsAt: m.now.Add(90 * time.Minute)},
		{Name: "週", Remaining: 0.9, ResetsAt: m.now.Add(50 * time.Hour)},
	}}
	_, cmd := m.Update(usageMsg{agent: relay.AgentCodex, usage: usage})
	require.NotNil(t, cmd, "the next refresh is scheduled")
	plain := xansi.Strip(m.View())
	require.Contains(t, plain, "5h ██░░░░░░  25% 1h30m")
	require.Contains(t, plain, "週 ███████░  90% 2d2h")
	require.Contains(t, plain, "(3時間前)")

	m.Update(usageMsg{agent: relay.AgentClaude, err: errors.New("boom")})
	require.Contains(t, xansi.Strip(m.View()), "取得できません: boom")

	m.refreshUsage()
	_, cmd = m.Update(usageMsg{agent: relay.AgentCodex, gen: 0, err: errors.New("stale")})
	require.Nil(t, cmd)
	require.NotContains(t, xansi.Strip(m.View()), "stale", "a reply to a superseded refresh is dropped")
	_, cmd = m.Update(usageTickMsg{agent: relay.AgentCodex, gen: 0})
	require.Nil(t, cmd)
}

func TestAgentRowsShowStateAndDiff(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(diffMsg{
		diffs: map[relay.Agent]diffState{relay.AgentClaude: {added: 12, removed: 3}, relay.AgentCodex: {err: errors.New("x")}},
		dead:  map[relay.Agent]bool{relay.AgentCodex: true},
	})
	m.status.Busy = map[relay.Agent]bool{relay.AgentClaude: true}
	plain := xansi.Strip(m.View())
	require.Contains(t, plain, "● Claude Code 作業中 +12 -3")
	require.Contains(t, plain, "■ Codex       停止")
	require.Contains(t, plain, " 差分 ")
	require.Contains(t, plain, " マージ ")
}

func TestClosingAsksFirst(t *testing.T) {
	m, _ := newTestModel(t)
	click(t, m, "ワークスペースを閉じる")
	require.True(t, m.closing)
	require.Contains(t, xansi.Strip(m.View()), "ブランチは残ります")
	click(t, m, "やめる")
	require.False(t, m.closing)

	m.Update(closeFailedMsg{err: &workspace.DirtyError{Dirs: []string{"/wt/claude"}}})
	plain := xansi.Strip(m.View())
	require.Contains(t, plain, "それでも閉じる")
	require.Contains(t, plain, "/wt/claude")
}

func TestNoticesExpire(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(noticeMsg{text: "done"})
	require.Contains(t, xansi.Strip(m.View()), "done")
	m.messageAt = time.Now().Add(-messageLifetime - time.Second)
	m.Update(statusTickMsg{})
	require.NotContains(t, xansi.Strip(m.View()), "done")
}

func TestScreenZonesFollowWideCharacters(t *testing.T) {
	s := &screen{width: 20}
	hit := false
	r := s.row().plain("日本語")
	r.button("押す", buttonStyle, func() tea.Cmd { hit = true; return nil })
	r.button("見えない", buttonStyle, func() tea.Cmd { return nil })
	_, zones := s.render(5)
	require.Len(t, zones, 2, "a button cut off at the edge keeps its visible part")
	require.Equal(t, zone{row: 0, x0: 6, x1: 12}, zone{row: zones[0].row, x0: zones[0].x0, x1: zones[0].x1})
	require.Equal(t, 20, zones[1].x1)
	zones[0].action()
	require.True(t, hit)

	s = &screen{width: 5}
	s.row().plain("abcde").button("x", buttonStyle, nil)
	_, zones = s.render(5)
	require.Empty(t, zones, "a button past the edge is not clickable")
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
	require.Equal(t, "████", xansi.Strip(meter(1, 4)))
	require.Equal(t, "░░░░", xansi.Strip(meter(0, 4)))
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
