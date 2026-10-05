package relay

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	xansi "github.com/charmbracelet/x/ansi"
)

// Question is one question put to one agent.
type Question struct {
	From Sender
	// FromUser marks a question the user typed in the asking session, rather
	// than one that session's agent wrote.
	FromUser bool
	To       Agent
	// ToWorktree is the recipient's own worktree.
	ToWorktree string
	// AlsoAsked lists the other recipients, whose answers are combined with this one.
	AlsoAsked []Agent
	Text      string
}

// Format renders the question as the prompt of a one-shot run of the recipient.
func (q Question) Format() string {
	var b strings.Builder
	label := q.From.Agent.Label()
	if q.FromUser {
		fmt.Fprintf(&b, "[Question from the user, asked in the %s session]\n", label)
	} else {
		fmt.Fprintf(&b, "[Message from %s]\n", label)
	}
	fmt.Fprintf(&b, "You are %s, consulted in a fresh session. Your own worktree: %s\n", q.To.Label(), q.ToWorktree)
	q.From.writeContext(&b)
	if q.From.Latest != nil {
		fmt.Fprintf(&b, "--- Latest exchange in the %s session ---\n", label)
		if q.From.Latest.User != "" {
			fmt.Fprintf(&b, "User: %s\n", quote(q.From.Latest.User))
		}
		fmt.Fprintf(&b, "%s: %s\n---\n", label, quote(q.From.Latest.Assistant))
	}
	if len(q.AlsoAsked) > 0 {
		fmt.Fprintf(&b, "Also asked: %s.\n", joinLabels(q.AlsoAsked))
	}
	fmt.Fprintf(&b, "Answer directly; your reply is passed back to %s.\n", label)
	fmt.Fprintf(&b, "\n%s", strings.TrimSpace(q.Text))
	return b.String()
}

// ConsultTarget is where and how to run an agent for one consultation.
type ConsultTarget struct {
	// Program is the agent's configured command; its executable is reused.
	Program string
	// Worktree is the agent's own worktree.
	Worktree string
	// SharedDir holds every worktree and relay transcript.
	SharedDir string
	// ScratchDir is a private directory for files the run needs, and the
	// working directory for CLIs that must not run in the worktree.
	ScratchDir string
}

// consultRun is a prepared one-shot run and how to read its answer.
type consultRun struct {
	cmd    *exec.Cmd
	answer func(stdout []byte) (string, error)
}

// Consult asks the agent a question in a fresh, non-interactive run of its
// CLI and returns the answer. A fresh run carries nothing from the agent's
// own sessions, so an earlier conversation cannot color the answer, and none
// of the runs below leave a session behind that the relay would mistake for
// the agent's live one.
func (a Agent) Consult(ctx context.Context, target ConsultTarget, prompt string) (string, error) {
	spec, ok := specs[a]
	if !ok || spec.consult == nil {
		return "", fmt.Errorf("%s cannot be consulted", a.Label())
	}
	if len(strings.Fields(target.Program)) == 0 {
		return "", fmt.Errorf("no command configured for %s", a.Label())
	}
	run := spec.consult(ctx, target, prompt)
	var stdout, stderr bytes.Buffer
	run.cmd.Stdout = &stdout
	run.cmd.Stderr = &stderr
	if err := run.cmd.Run(); err != nil {
		return "", fmt.Errorf("%s run failed: %w%s", a.Label(), err, lastLines(stderr.String(), 3))
	}
	answer, err := run.answer(stdout.Bytes())
	if err != nil {
		return "", err
	}
	answer = strings.TrimSpace(xansi.Strip(answer))
	if answer == "" {
		return "", fmt.Errorf("%s gave an empty answer%s", a.Label(), lastLines(stderr.String(), 3))
	}
	return answer, nil
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if joined := strings.TrimSpace(strings.Join(lines, "\n")); joined != "" {
		return ": " + joined
	}
	return ""
}

func programParts(program string) (string, []string) {
	fields := strings.Fields(program)
	return fields[0], fields[1:]
}

func stdoutAnswer(stdout []byte) (string, error) {
	return string(stdout), nil
}

// codexConsult runs `codex exec`, which keeps the read-only sandbox and writes
// only the final message to a file; --ephemeral leaves no session log.
func codexConsult(ctx context.Context, target ConsultTarget, prompt string) consultRun {
	binary, _ := programParts(target.Program)
	out := filepath.Join(target.ScratchDir, "codex-answer-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".txt")
	cmd := exec.CommandContext(ctx, binary, "exec",
		"--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral", "--color", "never",
		"-C", target.Worktree, "-o", out, "-")
	cmd.Stdin = strings.NewReader(prompt)
	return consultRun{cmd: cmd, answer: func([]byte) (string, error) {
		defer os.Remove(out)
		answer, err := os.ReadFile(out)
		if err != nil {
			return "", fmt.Errorf("read Codex answer: %w", err)
		}
		return string(answer), nil
	}}
}

// claudeConsult runs `claude -p` in the worktree. --no-session-persistence
// leaves no session log, and dontAsk denies anything that would need a
// permission prompt, since nobody can answer one.
func claudeConsult(ctx context.Context, target ConsultTarget, prompt string) consultRun {
	binary, extra := programParts(target.Program)
	args := append(extra, "-p", "--no-session-persistence", "--permission-mode", "dontAsk",
		"--add-dir", target.SharedDir, "--output-format", "text")
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = target.Worktree
	cmd.Stdin = strings.NewReader(prompt)
	return consultRun{cmd: cmd, answer: stdoutAnswer}
}

// agyConsult runs `agy -p` outside the worktree: agy records each run as the
// latest conversation of its working directory, which would hide the live
// session the relay reads from the worktree's entry.
func agyConsult(ctx context.Context, target ConsultTarget, prompt string) consultRun {
	binary, extra := programParts(target.Program)
	args := append(extra, "-p", prompt, "--output-format", "text")
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = target.ScratchDir
	return consultRun{cmd: cmd, answer: stdoutAnswer}
}
