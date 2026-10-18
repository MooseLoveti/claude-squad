package workspace

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/relay"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	log.Initialize("[test] ")
	defer log.Close()
	os.Exit(m.Run())
}

// isolate points HOME (and so ~/.claude-squad and git's global config) at a
// temporary directory and sets the branch prefix.
func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := filepath.Join(home, ".claude-squad")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, config.ConfigFileName), []byte(`{"branch_prefix":"t/"}`), 0o644))
	return home
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOutput(dir, args...)
	require.NoError(t, err)
	return out
}

func newRepo(t *testing.T) string {
	dir, err := CreateDir(filepath.Join(t.TempDir(), "repo"))
	require.NoError(t, err)
	return dir
}

func testConfig() *config.Config {
	return &config.Config{BranchPrefix: "t/", Profiles: []config.Profile{
		{Name: "claude", Program: "/opt/claude"},
		{Name: "codex", Program: "/opt/codex --search"},
		{Name: "gemini", Program: "/opt/agy --dangerously-skip-permissions"},
		{Name: "deepseek", Program: "/opt/reasonix"},
	}}
}

func TestInspect(t *testing.T) {
	isolate(t)
	_, _, err := Inspect(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	_, _, err = Inspect(file)
	require.ErrorContains(t, err, "ディレクトリではありません")

	plain := t.TempDir()
	kind, root, err := Inspect(plain)
	require.NoError(t, err)
	require.Equal(t, KindShared, kind)
	require.Equal(t, plain, root)

	empty := t.TempDir()
	runGit(t, empty, "init")
	kind, root, err = Inspect(empty)
	require.ErrorIs(t, err, ErrNoCommits)
	require.Equal(t, KindGit, kind)
	require.Equal(t, empty, root)
	require.NoError(t, InitialCommit(root), "works without a configured git identity")

	repo := newRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "sub"), 0o755))
	kind, root, err = Inspect(filepath.Join(repo, "sub"))
	require.NoError(t, err)
	require.Equal(t, KindGit, kind)
	require.Equal(t, repo, root, "a subdirectory opens its repository")
}

func TestCreateDir(t *testing.T) {
	isolate(t)
	dir, err := CreateDir(filepath.Join(t.TempDir(), "a", "b"))
	require.NoError(t, err)
	require.Equal(t, "Initial commit", runGit(t, dir, "log", "--format=%s"))

	existing := t.TempDir()
	_, err = CreateDir(existing)
	require.NoError(t, err, "an existing empty directory is fine")

	full := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(full, "x"), nil, 0o644))
	_, err = CreateDir(full)
	require.ErrorContains(t, err, "空ではありません")
}

func TestInitRepoCommitsTheFilesAlreadyThere(t *testing.T) {
	home := isolate(t)
	dir := filepath.Join(t.TempDir(), "notes")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("b"), 0o644))
	require.NoError(t, InitRepo(dir))
	require.Equal(t, "a.txt\nsub/b.txt", runGit(t, dir, "ls-files"))

	ws, err := Open(dir, false, testConfig())
	require.NoError(t, err)
	for _, m := range ws.Members {
		require.FileExists(t, filepath.Join(m.Dir, "sub", "b.txt"), "every worktree starts with the files")
	}

	require.ErrorIs(t, InitRepo(home), ErrHomeRepo)
	require.ErrorIs(t, InitRepo(filepath.Dir(home)), ErrHomeRepo, "a directory above home holds it too")
	require.NoDirExists(t, filepath.Join(home, ".git"))
	project := filepath.Join(home, "project")
	require.NoError(t, os.MkdirAll(project, 0o755))
	require.NoError(t, InitRepo(project), "a folder inside home is fine")
	sibling := home + "-work"
	require.NoError(t, os.MkdirAll(sibling, 0o755))
	t.Cleanup(func() { os.RemoveAll(sibling) })
	require.NoError(t, InitRepo(sibling), "a name that only starts like home is not above it")
}

func TestScratchDir(t *testing.T) {
	configDir := t.TempDir()
	dir, err := ScratchDir(configDir, time.Date(2026, 10, 5, 18, 17, 3, 0, time.Local))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(configDir, "scratch", "20261005-181703"), dir)
	require.DirExists(t, dir)
}

func TestProgramFor(t *testing.T) {
	cfg := testConfig()
	require.Equal(t, "/opt/codex --search", ProgramFor(cfg, relay.AgentCodex))

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "agy"), []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", bin)
	require.Equal(t, filepath.Join(bin, "agy"), ProgramFor(&config.Config{}, relay.AgentAntigravity), "falls back to PATH")
	require.Equal(t, "", ProgramFor(&config.Config{}, relay.AgentClaude), "not installed")
}

func TestOpenGivesEachAgentAWorktreeAndReusesIt(t *testing.T) {
	home := isolate(t)
	repo := newRepo(t)
	ws, err := Open(repo, false, testConfig())
	require.NoError(t, err)
	require.Equal(t, KindGit, ws.Kind)
	require.Equal(t, repo, ws.Dir)
	require.Len(t, ws.Members, len(relay.Agents()))
	worktrees := filepath.Join(home, ".claude-squad", "worktrees")
	for _, m := range ws.Members {
		require.Equal(t, "t/"+m.Agent, m.Branch)
		require.True(t, strings.HasPrefix(m.Dir, worktrees), m.Dir)
		require.Equal(t, m.Branch, runGit(t, m.Dir, "branch", "--show-current"))
	}
	codex, ok := ws.Member("codex")
	require.True(t, ok)
	require.Equal(t, "/opt/codex --search", codex.Program)
	_, ok = ws.Member("aider")
	require.False(t, ok)

	again, err := Open(repo, false, testConfig())
	require.NoError(t, err)
	require.Equal(t, ws.Members, again.Members, "opening again reuses the branches and worktrees")

	// A worktree whose directory vanished is pruned and made again.
	require.NoError(t, os.RemoveAll(codex.Dir))
	third, err := Open(repo, false, testConfig())
	require.NoError(t, err)
	codex3, _ := third.Member("codex")
	require.NotEqual(t, codex.Dir, codex3.Dir)
	require.DirExists(t, codex3.Dir)
}

func TestOpenSharedDirectory(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	ws, err := Open(dir, true, testConfig())
	require.NoError(t, err)
	require.Equal(t, KindShared, ws.Kind)
	require.True(t, ws.Scratch)
	for _, m := range ws.Members {
		require.Equal(t, dir, m.Dir)
		require.Empty(t, m.Branch)
	}
}

func TestSaveLoadAndClose(t *testing.T) {
	home := isolate(t)
	state := config.LoadStateFrom(filepath.Join(home, ".claude-squad", config.StateFileName))
	none, err := Load(state)
	require.NoError(t, err)
	require.Nil(t, none)

	repo := newRepo(t)
	ws, err := Open(repo, false, testConfig())
	require.NoError(t, err)
	require.NoError(t, Save(state, ws))
	reloaded := config.LoadStateFrom(filepath.Join(home, ".claude-squad", config.StateFileName))
	loaded, err := Load(reloaded)
	require.NoError(t, err)
	require.Equal(t, ws, loaded)
	require.Equal(t, []string{repo}, reloaded.Recent)

	claude, _ := ws.Member("claude")
	require.NoError(t, os.WriteFile(filepath.Join(claude.Dir, "wip.txt"), []byte("wip"), 0o644))
	err = Close(reloaded, ws, false)
	var dirty *DirtyError
	require.ErrorAs(t, err, &dirty)
	require.Equal(t, []string{claude.Dir}, dirty.Dirs)
	for _, m := range ws.Members {
		require.DirExists(t, m.Dir, "nothing is removed until the user confirms")
	}
	stillOpen, err := Load(config.LoadStateFrom(filepath.Join(home, ".claude-squad", config.StateFileName)))
	require.NoError(t, err)
	require.NotNil(t, stillOpen)

	require.NoError(t, Close(reloaded, ws, true))
	require.DirExists(t, claude.Dir, "a worktree with uncommitted work is kept even when forced")
	for _, m := range ws.Members {
		if m.Agent != "claude" {
			require.NoDirExists(t, m.Dir)
		}
		require.Equal(t, m.Branch, runGit(t, repo, "branch", "--list", m.Branch, "--format=%(refname:short)"), "branches are kept")
	}
	closed, err := Load(config.LoadStateFrom(filepath.Join(home, ".claude-squad", config.StateFileName)))
	require.NoError(t, err)
	require.Nil(t, closed)

	scratchState := config.LoadStateFrom(filepath.Join(t.TempDir(), "state.json"))
	scratch, err := Open(t.TempDir(), true, testConfig())
	require.NoError(t, err)
	require.NoError(t, Save(scratchState, scratch))
	require.NoError(t, Close(scratchState, scratch, false), "a shared workspace has no worktrees to keep")
	require.Empty(t, scratchState.Recent, "scratch directories are not remembered")

	scratchState.Workspace = []byte("{")
	_, err = Load(scratchState)
	require.Error(t, err)
}

func TestParseWorktreeList(t *testing.T) {
	out := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /wt/x\nHEAD def\nbranch refs/heads/t/codex\n\nworktree /wt/d\nHEAD 123\ndetached\n"
	require.Equal(t, map[string]string{"main": "/repo", "t/codex": "/wt/x"}, parseWorktreeList(out))
}

func TestGitOutputReportsFailures(t *testing.T) {
	_, err := gitOutput(t.TempDir(), "rev-parse", "HEAD")
	require.ErrorContains(t, err, "git rev-parse")
}

func TestSwitchRequest(t *testing.T) {
	dir := t.TempDir()
	_, ok := TakeSwitch(dir)
	require.False(t, ok)
	require.NoError(t, RequestSwitch(filepath.Join(dir, "cfg"), "/work/next"))
	got, ok := TakeSwitch(filepath.Join(dir, "cfg"))
	require.True(t, ok)
	require.Equal(t, "/work/next", got)
	_, ok = TakeSwitch(filepath.Join(dir, "cfg"))
	require.False(t, ok, "a request is taken once")

	require.NoError(t, os.WriteFile(filepath.Join(dir, switchFileName), []byte("{"), 0o644))
	_, ok = TakeSwitch(dir)
	require.False(t, ok, "a broken request is dropped")
	require.NoFileExists(t, filepath.Join(dir, switchFileName))
}
