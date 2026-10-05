package launcher

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/workspace"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	log.Initialize("[test] ")
	defer log.Close()
	os.Exit(m.Run())
}

func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return home
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(m *model, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		_, cmd = m.Update(key(k))
	}
	return cmd
}

func newTestModel(t *testing.T, state *config.State) *model {
	home := isolate(t)
	cfg := &config.Config{ProjectsDir: filepath.Join(home, "src")}
	if state == nil {
		state = config.LoadStateFrom(filepath.Join(home, "state.json"))
	}
	m := newModel(cfg, state)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

func selectKind(t *testing.T, m *model, kind itemKind) {
	for i, it := range m.items {
		if it.kind == kind {
			m.cursor = i
			m.focusInput()
			return
		}
	}
	t.Fatalf("no item of kind %d", kind)
}

func TestOpeningAPlainDirectoryAsksHowToUseIt(t *testing.T) {
	m := newTestModel(t, nil)
	dir := t.TempDir()
	selectKind(t, m, itemOpen)
	m.open.SetValue(dir)
	press(m, "enter")
	require.Equal(t, confirmNotGit, m.confirm)
	require.Contains(t, m.View(), "git リポジトリではありません")

	press(m, "x")
	require.Equal(t, confirmNone, m.confirm, "any other key goes back")
	require.Nil(t, m.choice)

	press(m, "enter", "s")
	require.Equal(t, &Choice{Dir: dir}, m.choice, "s shares the directory as is")

	m = newTestModel(t, nil)
	other := t.TempDir()
	selectKind(t, m, itemOpen)
	m.open.SetValue(other)
	press(m, "enter", "g")
	require.Equal(t, &Choice{Dir: other}, m.choice)
	kind, _, err := workspace.Inspect(other)
	require.NoError(t, err)
	require.Equal(t, workspace.KindGit, kind, "g makes it a repository with a commit")
}

func TestOpeningARepositoryWithoutCommits(t *testing.T) {
	m := newTestModel(t, nil)
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "-q").Run())
	selectKind(t, m, itemOpen)
	m.open.SetValue(dir)
	press(m, "enter")
	require.Equal(t, confirmNoCommits, m.confirm)
	press(m, "y")
	require.Equal(t, &Choice{Dir: dir}, m.choice)
}

func TestOpenErrorsStayOnScreen(t *testing.T) {
	m := newTestModel(t, nil)
	selectKind(t, m, itemOpen)
	m.open.SetValue(filepath.Join(t.TempDir(), "missing"))
	press(m, "enter")
	require.Nil(t, m.choice)
	require.Contains(t, m.View(), "no such file or directory")
	press(m, "down")
	require.NotContains(t, m.View(), "no such file or directory", "moving clears the error")
}

func TestCreateMakesARepository(t *testing.T) {
	m := newTestModel(t, nil)
	selectKind(t, m, itemCreate)
	require.True(t, strings.HasSuffix(m.create.Value(), "/src/"), "starts in the projects directory")
	press(m, "enter")
	require.Contains(t, m.View(), "名前を入れてください")

	dir := filepath.Join(t.TempDir(), "newproj")
	m.create.SetValue(dir)
	press(m, "enter")
	require.Equal(t, &Choice{Dir: dir}, m.choice)
	require.DirExists(t, filepath.Join(dir, ".git"))
}

func TestScratchMakesAThrowawayDirectory(t *testing.T) {
	m := newTestModel(t, nil)
	selectKind(t, m, itemScratch)
	cmd := press(m, "enter")
	require.NotNil(t, cmd)
	require.True(t, m.choice.Scratch)
	require.DirExists(t, m.choice.Dir)
	require.Contains(t, m.choice.Dir, filepath.Join(".claude-squad", "scratch"))
}

func TestResumeAndRecentItems(t *testing.T) {
	home := isolate(t)
	projects := filepath.Join(home, "src")
	for _, name := range []string{"beta", "alpha", "notrepo"} {
		require.NoError(t, os.MkdirAll(filepath.Join(projects, name), 0o755))
	}
	for _, name := range []string{"alpha", "beta"} {
		require.NoError(t, os.MkdirAll(filepath.Join(projects, name, ".git"), 0o755))
	}
	recent := t.TempDir()
	state := config.LoadStateFrom(filepath.Join(home, "state.json"))
	state.Recent = []string{recent, filepath.Join(home, "gone"), filepath.Join(projects, "beta")}
	state.Workspace = []byte(`{"dir":"/repo","kind":"shared","scratch":true}`)

	m := newModel(&config.Config{ProjectsDir: projects}, state)
	require.Equal(t, itemResume, m.items[0].kind)
	var dirs []string
	for _, it := range m.items {
		if it.kind == itemDir {
			dirs = append(dirs, it.dir)
		}
	}
	require.Equal(t, []string{recent, filepath.Join(projects, "beta"), filepath.Join(projects, "alpha")}, dirs,
		"recent first, missing ones dropped, then repositories under the projects directory")

	press(m, "enter")
	require.Equal(t, &Choice{Dir: "/repo", Scratch: true}, m.choice)
}

func TestMouseClickActivatesAnItem(t *testing.T) {
	m := newTestModel(t, nil)
	m.View()
	var row int
	for r, i := range m.itemRows {
		if m.items[i].kind == itemScratch {
			row = r
		}
	}
	m.Update(tea.MouseMsg{X: 6, Y: row, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	require.NotNil(t, m.choice)
	require.True(t, m.choice.Scratch)
}

func TestEscQuitsWithoutAChoice(t *testing.T) {
	m := newTestModel(t, nil)
	cmd := press(m, "esc")
	require.NotNil(t, cmd)
	require.Nil(t, m.choice)
}

func TestTypingGoesIntoThePathBox(t *testing.T) {
	m := newTestModel(t, nil)
	selectKind(t, m, itemOpen)
	m.open.SetValue("/tmp")
	press(m, "/", "x")
	require.Equal(t, "/tmp/x", m.open.Value())
	press(m, "up")
	require.NotEqual(t, itemOpen, m.items[m.cursor].kind)
}

func TestComplete(t *testing.T) {
	home := isolate(t)
	base := filepath.Join(home, "p")
	for _, name := range []string{"project-a", "project-b", "solo", ".hidden"} {
		require.NoError(t, os.MkdirAll(filepath.Join(base, name), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(base, "sofile"), nil, 0o644))
	require.Equal(t, base+"/project-", complete(base+"/pro"), "longest common prefix")
	require.Equal(t, base+"/solo/", complete(base+"/so"), "a single directory match gets a slash; files are ignored")
	require.Equal(t, "~/p/solo/", complete("~/p/so"), "the ~ form is kept")
	require.Equal(t, base+"/.hidden/", complete(base+"/."))
	require.Equal(t, base+"/zzz", complete(base+"/zzz"))
	require.Equal(t, "/nonexistent/x", complete("/nonexistent/x"))
	m := newTestModel(t, nil)
	selectKind(t, m, itemOpen)
	m.open.SetValue(base + "/so")
	press(m, "tab")
	require.Equal(t, base+"/solo/", m.open.Value())
}
