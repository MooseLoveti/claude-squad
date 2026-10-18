// Package workspace sets up the directory the agents work in: one git
// worktree per agent inside a repository, or one directory they share.
package workspace

import (
	"claude-squad/config"
	"claude-squad/relay"
	"claude-squad/session/git"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Kind is how agents share a workspace.
type Kind string

const (
	// KindGit gives each agent its own worktree and branch.
	KindGit Kind = "git"
	// KindShared runs every agent in the same directory.
	KindShared Kind = "shared"
)

// Member is one agent's place in the workspace.
type Member struct {
	// Agent is the relay agent's Name.
	Agent string `json:"agent"`
	// Dir is where the agent runs.
	Dir string `json:"dir"`
	// Branch is the agent's branch in a git workspace.
	Branch string `json:"branch,omitempty"`
	// Program is the command the agent runs with; empty when not installed.
	Program string `json:"program"`
}

// Workspace is the directory the agents work on and where each one runs.
type Workspace struct {
	// Dir is the repository root or the shared directory.
	Dir  string `json:"dir"`
	Kind Kind   `json:"kind"`
	// Scratch marks a throwaway directory made for working without one.
	Scratch bool     `json:"scratch,omitempty"`
	Members []Member `json:"members"`
}

// ErrNoCommits means a repository has no commit to branch worktrees from.
var ErrNoCommits = errors.New("リポジトリにまだコミットがありません")

// Inspect resolves dir and tells how agents can work in it: a repository with
// commits gets worktrees, any other directory is shared. A repository without
// commits returns its root and ErrNoCommits.
func Inspect(dir string) (Kind, string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("%s はディレクトリではありません", abs)
	}
	root, err := gitOutput(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return KindShared, abs, nil
	}
	if _, err := gitOutput(root, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return KindGit, root, ErrNoCommits
	}
	return KindGit, root, nil
}

// InitialCommit gives a new repository its first commit to branch from, of
// whatever is staged, possibly nothing. It works before git has an identity
// configured, using a placeholder one.
func InitialCommit(root string) error {
	args := []string{"-c", "commit.gpgsign=false"}
	if email, _ := gitOutput(root, "config", "user.email"); email == "" {
		args = append(args, "-c", "user.name=claude-squad", "-c", "user.email=claude-squad@localhost")
	}
	_, err := gitOutput(root, append(args, "commit", "--allow-empty", "-m", "Initial commit")...)
	return err
}

// ErrHomeRepo refuses to make the home directory, or a directory above it, a
// repository: its first commit would sweep up everything the user has.
var ErrHomeRepo = errors.New("ホームディレクトリやその上のフォルダは git にできません。共有で開くか、作業用のサブフォルダを選んでください")

// InitRepo makes an existing directory a git repository whose first commit
// holds the files already there, so every agent's worktree starts with them.
func InitRepo(dir string) error {
	if containsHome(dir) {
		return ErrHomeRepo
	}
	if _, err := gitOutput(dir, "init"); err != nil {
		return err
	}
	if _, err := gitOutput(dir, "add", "-A"); err != nil {
		return err
	}
	return InitialCommit(dir)
}

// containsHome reports whether dir is the home directory or one above it.
func containsHome(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(abs, home)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CreateDir makes a new directory as a git repository with an empty first
// commit. An existing directory is only accepted when empty.
func CreateDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err == nil && len(entries) > 0 {
		return "", fmt.Errorf("%s は既にあり、空ではありません", abs)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	return abs, InitRepo(abs)
}

// ScratchDir makes a fresh directory for working without one.
func ScratchDir(configDir string, now time.Time) (string, error) {
	dir := filepath.Join(configDir, "scratch", now.Format("20060102-150405"))
	return dir, os.MkdirAll(dir, 0o755)
}

// ProgramFor finds the command an agent runs with: the first profile whose
// executable is the agent's, otherwise the agent's executable on PATH.
func ProgramFor(cfg *config.Config, agent relay.Agent) string {
	for _, p := range cfg.Profiles {
		if relay.DetectAgent(p.Program) == agent {
			return p.Program
		}
	}
	if path, err := exec.LookPath(agent.Executable()); err == nil {
		return path
	}
	return ""
}

// Open prepares a workspace on dir for every agent: in a repository each agent
// gets a worktree on its own branch (prefix + agent name), reusing the branch
// and its worktree when they already exist.
func Open(dir string, scratch bool, cfg *config.Config) (*Workspace, error) {
	kind, root, err := Inspect(dir)
	if err != nil {
		return nil, err
	}
	w := &Workspace{Dir: root, Kind: kind, Scratch: scratch}
	var existing map[string]string
	if kind == KindGit {
		// Drop registrations of worktrees whose directories are gone, so their
		// branches can be checked out again.
		if _, err := gitOutput(root, "worktree", "prune"); err != nil {
			return nil, err
		}
		if existing, err = worktreesByBranch(root); err != nil {
			return nil, err
		}
	}
	for _, agent := range relay.Agents() {
		m := Member{Agent: agent.Name(), Dir: root, Program: ProgramFor(cfg, agent)}
		if kind == KindGit {
			if m.Dir, m.Branch, err = ensureWorktree(root, agent.Name(), existing); err != nil {
				return nil, fmt.Errorf("%s の worktree を用意できませんでした: %w", agent.Label(), err)
			}
		}
		w.Members = append(w.Members, m)
	}
	return w, nil
}

func ensureWorktree(root, name string, existing map[string]string) (string, string, error) {
	tree, branch, err := git.NewGitWorktree(root, name)
	if err != nil {
		return "", "", err
	}
	if path, ok := existing[branch]; ok {
		return path, branch, nil
	}
	if err := tree.Setup(); err != nil {
		return "", "", err
	}
	return tree.GetWorktreePath(), branch, nil
}

// worktreesByBranch maps each checked-out branch to its worktree.
func worktreesByBranch(root string) (map[string]string, error) {
	out, err := gitOutput(root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktreeList(out), nil
}

func parseWorktreeList(out string) map[string]string {
	byBranch := map[string]string{}
	path := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			byBranch[strings.TrimPrefix(line, "branch refs/heads/")] = path
		}
	}
	return byBranch
}

// Member returns the agent's member, if the workspace has it.
func (w *Workspace) Member(agent string) (Member, bool) {
	for _, m := range w.Members {
		if m.Agent == agent {
			return m, true
		}
	}
	return Member{}, false
}

// Save records w as the open workspace.
func Save(state *config.State, w *Workspace) error {
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	state.Workspace = data
	if !w.Scratch {
		state.AddRecent(w.Dir)
	}
	return state.Save()
}

// Load returns the open workspace, or nil when there is none.
func Load(state *config.State) (*Workspace, error) {
	if len(state.Workspace) == 0 {
		return nil, nil
	}
	var w Workspace
	if err := json.Unmarshal(state.Workspace, &w); err != nil {
		return nil, fmt.Errorf("parse saved workspace: %w", err)
	}
	return &w, nil
}

// DirtyError lists worktrees with uncommitted changes, which closing keeps.
type DirtyError struct {
	Dirs []string
}

func (e *DirtyError) Error() string {
	return "未コミットの変更がある worktree: " + strings.Join(e.Dirs, ", ")
}

// Close forgets the open workspace and removes the worktrees claude-squad
// made, keeping their branches. Worktrees with uncommitted changes are never
// removed: unless force is set, Close changes nothing and returns a
// *DirtyError naming them; with force it removes only the clean ones.
func Close(state *config.State, w *Workspace, force bool) error {
	var clean, dirty []string
	if w.Kind == KindGit {
		for _, m := range w.Members {
			if m.Dir == w.Dir || !ownWorktree(m.Dir) {
				continue
			}
			if status, err := gitOutput(m.Dir, "status", "--porcelain"); err != nil || status != "" {
				dirty = append(dirty, m.Dir)
			} else {
				clean = append(clean, m.Dir)
			}
		}
	}
	if len(dirty) > 0 && !force {
		return &DirtyError{Dirs: dirty}
	}
	var failed []string
	for _, dir := range clean {
		if _, err := gitOutput(w.Dir, "worktree", "remove", dir); err != nil {
			failed = append(failed, err.Error())
		}
	}
	state.Workspace = nil
	if err := state.Save(); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("worktree を削除できませんでした: %s", strings.Join(failed, "; "))
	}
	return nil
}

// ownWorktree reports whether dir is one of the worktrees claude-squad makes
// under its config directory, as opposed to one the user made.
func ownWorktree(dir string) bool {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(filepath.Join(configDir, "worktrees"), dir)
	return err == nil && !strings.HasPrefix(rel, "..")
}

func gitOutput(dir string, args ...string) (string, error) {
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s (%w)", args[0], strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}
