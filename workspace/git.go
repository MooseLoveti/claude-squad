package workspace

import (
	"claude-squad/session/git"
	"errors"
	"fmt"
)

// errNotGit is returned for branch operations in a shared workspace.
var errNotGit = errors.New("git の worktree を使うワークスペースではありません")

// base is where the member's branch left the repository's current branch, so
// a diff shows only what the agent has not merged yet.
func (w *Workspace) base(m Member) (string, error) {
	if w.Kind != KindGit || m.Branch == "" {
		return "", errNotGit
	}
	return gitOutput(w.Dir, "merge-base", "HEAD", m.Branch)
}

func (w *Workspace) worktree(m Member, base string) *git.GitWorktree {
	return git.NewGitWorktreeFromStorage(w.Dir, m.Dir, m.Agent, m.Branch, base, true)
}

// DiffStats counts the lines the member changed, committed or not, since its
// branch left the repository's current branch.
func (w *Workspace) DiffStats(m Member) (added, removed int, err error) {
	base, err := w.base(m)
	if err != nil {
		return 0, 0, err
	}
	stats := w.worktree(m, base).DiffNumstat()
	return stats.Added, stats.Removed, stats.Error
}

// DiffCommand is a shell command that pages the member's diff in color.
func (w *Workspace) DiffCommand(m Member) (string, error) {
	base, err := w.base(m)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("git add -N . && git -c color.ui=always diff %s | LESSCHARSET=utf-8 less -R", base), nil
}

// Merge commits the member's uncommitted work on its branch, then merges the
// branch into the repository's current branch. A conflict is left in place
// for the user or an agent to resolve.
func (w *Workspace) Merge(m Member) error {
	if w.Kind != KindGit || m.Branch == "" {
		return errNotGit
	}
	if err := w.worktree(m, "").CommitChanges(fmt.Sprintf("%s の作業内容 (claude-squad)", m.Agent)); err != nil {
		return err
	}
	_, err := gitOutput(w.Dir, "merge", "--no-ff", "--no-edit", m.Branch)
	return err
}

// Push commits the member's uncommitted work and pushes its branch.
func (w *Workspace) Push(m Member) error {
	if w.Kind != KindGit || m.Branch == "" {
		return errNotGit
	}
	return w.worktree(m, "").PushChanges(fmt.Sprintf("%s の作業内容 (claude-squad)", m.Agent), false)
}
