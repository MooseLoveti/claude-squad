// Package launcher is the screen that chooses where the agents work before
// the dashboard starts.
package launcher

import (
	"claude-squad/config"
	"claude-squad/workspace"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Choice is where to open the workspace.
type Choice struct {
	Dir string
	// Scratch marks a throwaway directory made for working without one.
	Scratch bool
}

// ErrCancelled means the user left the launcher without choosing.
var ErrCancelled = errors.New("cancelled")

type itemKind int

const (
	itemResume itemKind = iota
	itemOpen
	itemCreate
	itemScratch
	itemDir
)

type item struct {
	kind  itemKind
	label string
	dir   string
	// scratch carries over a resumed workspace's scratch flag.
	scratch bool
}

// confirmKind is a question the launcher is waiting on.
type confirmKind int

const (
	confirmNone confirmKind = iota
	// confirmNoCommits: a repository without commits needs one for worktrees.
	confirmNoCommits
	// confirmNotGit: a plain directory can become a repository or be shared.
	confirmNotGit
)

type model struct {
	items   []item
	cursor  int
	open    textinput.Model
	create  textinput.Model
	err     string
	confirm confirmKind
	pending string
	choice  *Choice
	width   int
	// itemRows maps a rendered row to the item it shows, for mouse clicks.
	itemRows map[int]int
	cfg      *config.Config
}

// Run shows the launcher and returns the chosen directory, already a usable
// workspace directory (initialized when the user agreed to).
func Run(cfg *config.Config, state *config.State) (Choice, error) {
	m := newModel(cfg, state)
	final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if err != nil {
		return Choice{}, err
	}
	if c := final.(*model).choice; c != nil {
		return *c, nil
	}
	return Choice{}, ErrCancelled
}

func newModel(cfg *config.Config, state *config.State) *model {
	cwd, _ := os.Getwd()
	open := textinput.New()
	open.Prompt = "パス: "
	open.SetValue(cwd)
	create := textinput.New()
	create.Prompt = "パス: "
	create.SetValue(withSlash(cfg.Projects()))

	m := &model{open: open, create: create, cfg: cfg}
	if saved, err := workspace.Load(state); err == nil && saved != nil {
		m.items = append(m.items, item{kind: itemResume, label: "前回のワークスペースを再開  " + pretty(saved.Dir), dir: saved.Dir, scratch: saved.Scratch})
	}
	m.items = append(m.items,
		item{kind: itemOpen, label: "既存のディレクトリを開く"},
		item{kind: itemCreate, label: "新しいディレクトリを作る（git リポジトリにします）"},
		item{kind: itemScratch, label: "ディレクトリなしで作業する（相談・調べもの用の使い捨て）"},
	)
	for _, dir := range candidates(state.Recent, cfg.Projects()) {
		m.items = append(m.items, item{kind: itemDir, label: pretty(dir), dir: dir})
	}
	m.focusInput()
	return m
}

// candidates lists directories to offer: recent ones first, then git
// repositories directly under the projects directory.
func candidates(recent []string, projects string) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, dir := range recent {
		if info, err := os.Stat(dir); err == nil && info.IsDir() && !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	entries, _ := os.ReadDir(projects)
	var repos []string
	for _, e := range entries {
		dir := filepath.Join(projects, e.Name())
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil && e.IsDir() && !seen[dir] {
			repos = append(repos, dir)
		}
	}
	sort.Strings(repos)
	return append(dirs, repos...)
}

func pretty(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(dir, home) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

func withSlash(dir string) string {
	return strings.TrimSuffix(dir, "/") + "/"
}

func (m *model) Init() tea.Cmd {
	return textinput.Blink
}

// input is the path box of the selected item, if it has one.
func (m *model) input() *textinput.Model {
	switch m.items[m.cursor].kind {
	case itemOpen:
		return &m.open
	case itemCreate:
		return &m.create
	}
	return nil
}

func (m *model) focusInput() {
	m.open.Blur()
	m.create.Blur()
	if in := m.input(); in != nil {
		in.Focus()
		in.CursorEnd()
	}
}

func (m *model) move(delta int) {
	m.cursor = (m.cursor + delta + len(m.items)) % len(m.items)
	m.err = ""
	m.focusInput()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.open.Width = max(msg.Width-12, 20)
		m.create.Width = max(msg.Width-12, 20)
		return m, nil
	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			if i, ok := m.itemRows[msg.Y]; ok {
				m.cursor = i
				m.err = ""
				m.focusInput()
				if m.input() == nil {
					return m.activate()
				}
			}
		}
		return m, nil
	case tea.KeyMsg:
		if m.confirm != confirmNone {
			return m.answer(msg.String())
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "up", "shift+tab":
			m.move(-1)
			return m, nil
		case "down":
			m.move(1)
			return m, nil
		case "enter":
			return m.activate()
		case "tab":
			if in := m.input(); in != nil {
				in.SetValue(complete(in.Value()))
				in.CursorEnd()
			} else {
				m.move(1)
			}
			return m, nil
		}
		if in := m.input(); in != nil {
			var cmd tea.Cmd
			*in, cmd = in.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// activate acts on the selected item.
func (m *model) activate() (tea.Model, tea.Cmd) {
	it := m.items[m.cursor]
	switch it.kind {
	case itemResume:
		return m.done(Choice{Dir: it.dir, Scratch: it.scratch})
	case itemOpen:
		return m.openDir(config.ExpandHome(strings.TrimSpace(m.open.Value())))
	case itemDir:
		return m.openDir(it.dir)
	case itemCreate:
		path := strings.TrimSpace(m.create.Value())
		if path == "" || strings.HasSuffix(path, "/") {
			m.err = "作るディレクトリの名前を入れてください"
			return m, nil
		}
		dir, err := workspace.CreateDir(config.ExpandHome(path))
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		return m.done(Choice{Dir: dir})
	case itemScratch:
		configDir, err := config.GetConfigDir()
		if err == nil {
			var dir string
			if dir, err = workspace.ScratchDir(configDir, time.Now()); err == nil {
				return m.done(Choice{Dir: dir, Scratch: true})
			}
		}
		m.err = err.Error()
	}
	return m, nil
}

func (m *model) openDir(dir string) (tea.Model, tea.Cmd) {
	kind, root, err := workspace.Inspect(dir)
	switch {
	case errors.Is(err, workspace.ErrNoCommits):
		m.confirm, m.pending = confirmNoCommits, root
	case err != nil:
		m.err = err.Error()
	case kind == workspace.KindShared:
		m.confirm, m.pending = confirmNotGit, root
	default:
		return m.done(Choice{Dir: root})
	}
	return m, nil
}

func (m *model) answer(key string) (tea.Model, tea.Cmd) {
	dir, kind := m.pending, m.confirm
	m.confirm, m.pending = confirmNone, ""
	switch {
	case kind == confirmNoCommits && key == "y":
		if err := workspace.InitialCommit(dir); err != nil {
			m.err = err.Error()
			return m, nil
		}
		return m.done(Choice{Dir: dir})
	case kind == confirmNotGit && key == "g":
		if err := workspace.InitRepo(dir); err != nil {
			m.err = err.Error()
			return m, nil
		}
		return m.done(Choice{Dir: dir})
	case kind == confirmNotGit && key == "s":
		return m.done(Choice{Dir: dir})
	}
	return m, nil
}

func (m *model) done(c Choice) (tea.Model, tea.Cmd) {
	m.choice = &c
	return m, tea.Quit
}

// complete extends a path to the longest common prefix of the directories it
// could name, adding a slash when exactly one matches.
func complete(path string) string {
	expanded := config.ExpandHome(path)
	dir, prefix := filepath.Split(expanded)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return path
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) && (strings.HasPrefix(prefix, ".") || !strings.HasPrefix(e.Name(), ".")) {
			matches = append(matches, e.Name())
		}
	}
	if len(matches) == 0 {
		return path
	}
	common := matches[0]
	for _, name := range matches[1:] {
		for !strings.HasPrefix(name, common) {
			common = common[:len(common)-1]
		}
	}
	completed := path[:len(path)-len(prefix)] + common
	if len(matches) == 1 {
		completed += "/"
	}
	return completed
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#B69CFF"})
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#1F6FB2", Dark: "#7CC4FF"})
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#777777", Dark: "#8A8A8A"})
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B03A3A", Dark: "#F28B82"})
	askStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#FFD166"})
)

func (m *model) View() string {
	var lines []string
	add := func(s string) { lines = append(lines, s) }
	m.itemRows = map[int]int{}

	add("")
	add("  " + titleStyle.Render("claude-squad") + dimStyle.Render("  Codex・Claude Code・Gemini を並べて動かします"))
	add("")
	add("  どこで作業しますか？" + dimStyle.Render("（↑↓ で選んで Enter、Tab でパス補完、Esc で終了）"))
	add("")
	recentShown := false
	for i, it := range m.items {
		if it.kind == itemDir && !recentShown {
			add("")
			add("  " + dimStyle.Render("最近使ったディレクトリ・リポジトリ"))
			recentShown = true
		}
		m.itemRows[len(lines)] = i
		if i == m.cursor {
			add("  " + selectedStyle.Render("▶ "+it.label))
		} else {
			add("    " + it.label)
		}
		if i == m.cursor {
			switch it.kind {
			case itemOpen:
				add("      " + m.open.View())
			case itemCreate:
				add("      " + m.create.View())
			}
		}
	}
	add("")
	switch m.confirm {
	case confirmNoCommits:
		add("  " + askStyle.Render(pretty(m.pending)+" にはコミットがありません。空の初期コミットを作りますか？ (y/n)"))
	case confirmNotGit:
		add("  " + askStyle.Render(pretty(m.pending)+" は git リポジトリではありません。"))
		add("  " + askStyle.Render("g: git リポジトリにして各AIに worktree を作る / s: このまま3つのAIで共有する / ほかのキー: 戻る"))
	}
	if m.err != "" {
		add("  " + errorStyle.Render(m.err))
	}
	return strings.Join(lines, "\n")
}
