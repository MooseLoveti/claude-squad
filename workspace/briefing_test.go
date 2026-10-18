package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBriefingNamesTheOtherInstalledMembers(t *testing.T) {
	w := &Workspace{Dir: "/repo", Kind: KindGit, Members: []Member{
		{Agent: "claude", Dir: "/wt/c", Branch: "me/claude", Program: "claude"},
		{Agent: "codex", Dir: "/wt/x", Branch: "me/codex", Program: "codex"},
		{Agent: "gemini", Dir: "/wt/g", Branch: "me/gemini"},
	}}
	got := Briefing(w, w.Members[0], "cs")
	require.Contains(t, got, "Claude Code です。同じプロジェクトで Codex も")
	require.Contains(t, got, "- Codex: /wt/x（ブランチ me/codex）")
	require.NotContains(t, got, "/wt/g", "an agent that is not installed never starts")

	alone := &Workspace{Dir: "/repo", Kind: KindGit, Members: w.Members[:1]}
	require.Empty(t, Briefing(alone, alone.Members[0], "cs"), "nobody to tell about")
}

func TestWriteBriefingsGivesAgyAGeminiFileOutsideGit(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	ws, err := Open(repo, false, testConfig())
	require.NoError(t, err)
	gemini, _ := ws.Member("gemini")
	file := filepath.Join(gemini.Dir, geminiFile)

	require.NoError(t, WriteBriefings(ws, "/bin/cs"))
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "[claude-squad] あなたは claude-squad で動いている Gemini です。"), string(data))
	require.Empty(t, runGit(t, gemini.Dir, "status", "--porcelain"), "GEMINI.md stays out of git")

	// A reopened workspace rewrites its own briefing without excluding twice.
	require.NoError(t, WriteBriefings(ws, "/bin/cs2"))
	data, err = os.ReadFile(file)
	require.NoError(t, err)
	require.Contains(t, string(data), "'/bin/cs2' transcript codex")
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(exclude), "/GEMINI.md\n"))

	for _, m := range ws.Members {
		if m.Agent != "gemini" {
			require.NoFileExists(t, filepath.Join(m.Dir, geminiFile), "the others take theirs on the command line")
		}
	}
}

func TestWriteBriefingsLeavesTheUsersGeminiFileAlone(t *testing.T) {
	isolate(t)
	repo := newRepo(t)
	ws, err := Open(repo, false, testConfig())
	require.NoError(t, err)
	gemini, _ := ws.Member("gemini")
	file := filepath.Join(gemini.Dir, geminiFile)
	require.NoError(t, os.WriteFile(file, []byte("my own rules\n"), 0o644))
	require.NoError(t, WriteBriefings(ws, "cs"))
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "my own rules\n", string(data))

	tracked := newRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(tracked, geminiFile), []byte(briefingMarker+" tracked\n"), 0o644))
	runGit(t, tracked, "add", geminiFile)
	runGit(t, tracked, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "rules")
	ws, err = Open(tracked, false, testConfig())
	require.NoError(t, err)
	gemini, _ = ws.Member("gemini")
	require.NoError(t, WriteBriefings(ws, "cs"))
	data, err = os.ReadFile(filepath.Join(gemini.Dir, geminiFile))
	require.NoError(t, err)
	require.Equal(t, briefingMarker+" tracked\n", string(data), "a file the repository tracks is the repository's")
}

func TestWriteBriefingsSkipsASharedDirectory(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	ws, err := Open(dir, false, testConfig())
	require.NoError(t, err)
	require.NoError(t, WriteBriefings(ws, "cs"))
	require.NoFileExists(t, filepath.Join(dir, geminiFile), "a shared directory is the user's own")
}
