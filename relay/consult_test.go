package relay

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeCLI writes an executable named name that records how it was run
// (arguments one per line, working directory, stdin) into dir/name.log and
// then runs body.
func fakeCLI(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nlog=\"" + filepath.Join(dir, name+".log") + "\"\n" +
		"for a in \"$@\"; do echo \"arg:$a\"; done > \"$log\"\n" +
		"echo \"cwd:$(pwd)\" >> \"$log\"\n" +
		"if [ ! -t 0 ]; then echo \"stdin:$(cat)\" >> \"$log\"; fi\n" +
		body + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func readRun(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name+".log"))
	require.NoError(t, err)
	return string(b)
}

func consultDirs(t *testing.T) (bin, worktree, shared, scratch string) {
	return t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
}

func TestConsultCodexReadsTheLastMessageFile(t *testing.T) {
	bin, worktree, shared, scratch := consultDirs(t)
	// The answer file is the argument after -o.
	codex := fakeCLI(t, bin, "codex", `out=""; prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
echo "progress noise"
printf 'the answer\n' > "$out"`)

	got, err := AgentCodex.Consult(context.Background(),
		ConsultTarget{Program: codex + " --search --ask-for-approval never", Worktree: worktree, SharedDir: shared, ScratchDir: scratch},
		"[Question] what?")
	require.NoError(t, err)
	require.Equal(t, "the answer", got)

	run := readRun(t, bin, "codex")
	for _, want := range []string{"arg:exec", "arg:--sandbox\narg:read-only", "arg:--ephemeral", "arg:-C\narg:" + worktree, "stdin:[Question] what?"} {
		require.Contains(t, run, want)
	}
	require.NotContains(t, run, "--ask-for-approval", "interactive-only profile flags are not passed to exec")
	entries, err := os.ReadDir(scratch)
	require.NoError(t, err)
	require.Empty(t, entries, "the answer file is removed")
}

func TestConsultClaudeRunsInTheWorktreeWithoutASession(t *testing.T) {
	bin, worktree, shared, scratch := consultDirs(t)
	claude := fakeCLI(t, bin, "claude", `echo "\033[1mmap\033[0m"`)

	got, err := AgentClaude.Consult(context.Background(),
		ConsultTarget{Program: claude + " --model opus", Worktree: worktree, SharedDir: shared, ScratchDir: scratch}, "which?")
	require.NoError(t, err)
	require.Equal(t, "map", got, "ANSI styling is stripped")

	run := readRun(t, bin, "claude")
	for _, want := range []string{"arg:--model\narg:opus", "arg:-p", "arg:--no-session-persistence", "arg:--permission-mode\narg:dontAsk",
		"arg:--add-dir\narg:" + shared, "cwd:" + worktree, "stdin:which?"} {
		require.Contains(t, run, want)
	}
}

func TestConsultAgyRunsOutsideTheWorktree(t *testing.T) {
	bin, worktree, shared, scratch := consultDirs(t)
	agy := fakeCLI(t, bin, "agy", `echo "slice"`)

	got, err := AgentAntigravity.Consult(context.Background(),
		ConsultTarget{Program: agy + " --dangerously-skip-permissions", Worktree: worktree, SharedDir: shared, ScratchDir: scratch}, "which?")
	require.NoError(t, err)
	require.Equal(t, "slice", got)

	run := readRun(t, bin, "agy")
	require.Contains(t, run, "arg:--dangerously-skip-permissions\narg:-p\narg:which?", "the user's profile flags are kept")
	require.Contains(t, run, "cwd:"+scratch, "agy indexes conversations by directory, so it must not run in the worktree")
}

func TestConsultFailures(t *testing.T) {
	bin, worktree, shared, scratch := consultDirs(t)
	target := func(program string) ConsultTarget {
		return ConsultTarget{Program: program, Worktree: worktree, SharedDir: shared, ScratchDir: scratch}
	}

	failing := fakeCLI(t, bin, "claude", `echo "not logged in" >&2; exit 3`)
	_, err := AgentClaude.Consult(context.Background(), target(failing), "q")
	require.ErrorContains(t, err, "not logged in", "stderr explains a failed run")

	silent := fakeCLI(t, bin, "agy", `true`)
	_, err = AgentAntigravity.Consult(context.Background(), target(silent), "q")
	require.ErrorContains(t, err, "empty answer")

	noFile := fakeCLI(t, bin, "codex", `true`)
	_, err = AgentCodex.Consult(context.Background(), target(noFile), "q")
	require.ErrorContains(t, err, "read Codex answer")

	_, err = AgentCodex.Consult(context.Background(), target(""), "q")
	require.ErrorContains(t, err, "no command configured")
	_, err = AgentUnknown.Consult(context.Background(), target("x"), "q")
	require.Error(t, err)

	slow := fakeCLI(t, bin, "slow", `sleep 5`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = AgentClaude.Consult(ctx, target(slow), "q")
	require.Error(t, err, "a cancelled run stops")
	require.False(t, strings.Contains(err.Error(), "empty answer"))
}
