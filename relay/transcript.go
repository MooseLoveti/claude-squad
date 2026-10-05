package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type role int

const (
	roleUser role = iota
	roleAssistant
)

type turn struct {
	role role
	text string
}

// Transcript renders the user and assistant messages of the agent session whose
// working directory is worktreePath. It reads the CLI's own session log rather
// than the tmux scrollback, which is truncated and full of redraw artifacts.
func (a Agent) Transcript(worktreePath string) (string, error) {
	logPath, turns, err := a.sessionTurns(worktreePath)
	if err != nil {
		return "", err
	}
	return renderTranscript(a, worktreePath, logPath, turns), nil
}

// Exchange is the latest user message in a session and the agent's reply.
type Exchange struct {
	User      string
	Assistant string
}

// LatestExchange returns the session's most recent reply and the user message
// before it, so a question about "this" can carry what "this" is.
func (a Agent) LatestExchange(worktreePath string) (Exchange, error) {
	_, turns, err := a.sessionTurns(worktreePath)
	if err != nil {
		return Exchange{}, err
	}
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].role != roleAssistant {
			continue
		}
		exchange := Exchange{Assistant: turns[i].text}
		for j := i - 1; j >= 0; j-- {
			if turns[j].role == roleUser {
				exchange.User = turns[j].text
				break
			}
		}
		return exchange, nil
	}
	return Exchange{}, errors.New("no reply in the session yet")
}

func (a Agent) sessionTurns(worktreePath string) (string, []turn, error) {
	spec, ok := specs[a]
	if !ok {
		return "", nil, fmt.Errorf("no session log reader for %s", a.Label())
	}
	logPath, err := spec.findLog(worktreePath)
	if err != nil {
		return "", nil, err
	}
	f, err := os.Open(logPath)
	if err != nil {
		return "", nil, fmt.Errorf("open %s session log: %w", a.Label(), err)
	}
	defer f.Close()
	turns, err := spec.parseLog(f)
	if err != nil {
		return "", nil, fmt.Errorf("%s session log %s: %w", a.Label(), logPath, err)
	}
	return logPath, turns, nil
}

func renderTranscript(a Agent, worktreePath, logPath string, turns []turn) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s session transcript\n\n", a.Label())
	fmt.Fprintf(&b, "- worktree: %s\n", worktreePath)
	fmt.Fprintf(&b, "- source log: %s\n", logPath)
	for _, t := range turns {
		speaker := "User"
		if t.role == roleAssistant {
			speaker = a.Label()
		}
		fmt.Fprintf(&b, "\n## %s\n\n%s\n", speaker, t.text)
	}
	return b.String()
}

// envOrHome returns $envVar when set, otherwise ~/defaultDir.
func envOrHome(envVar, defaultDir string) (string, error) {
	if dir := os.Getenv(envVar); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, defaultDir), nil
}

var nonAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9]`)

// claudeProjectDir mirrors how Claude Code names a project's log directory.
func claudeProjectDir(cwd string) string {
	return nonAlphanumeric.ReplaceAllString(cwd, "-")
}

func findClaudeLog(cwd string) (string, error) {
	root, err := envOrHome("CLAUDE_CONFIG_DIR", ".claude")
	if err != nil {
		return "", err
	}
	return findClaudeLogIn(root, cwd)
}

func findClaudeLogIn(root, cwd string) (string, error) {
	dir := filepath.Join(root, "projects", claudeProjectDir(cwd))
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return "", err
	}
	path := newestFile(matches)
	if path == "" {
		return "", fmt.Errorf("no Claude Code session log in %s", dir)
	}
	return path, nil
}

func findCodexLog(cwd string) (string, error) {
	root, err := envOrHome("CODEX_HOME", ".codex")
	if err != nil {
		return "", err
	}
	return findCodexLogIn(root, cwd)
}

func findCodexLogIn(root, cwd string) (string, error) {
	dir := filepath.Join(root, "sessions")
	return findLogByHeader(dir, "Codex", func(name string) bool {
		return strings.HasPrefix(name, "rollout-") && filepath.Ext(name) == ".jsonl"
	}, func(header []byte) bool {
		var meta struct {
			Type    string `json:"type"`
			Payload struct {
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		return json.Unmarshal(header, &meta) == nil && meta.Type == "session_meta" && meta.Payload.Cwd == cwd
	})
}

// agyHome is fixed: Antigravity CLI has no setting to relocate its data.
func agyHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".gemini", "antigravity-cli"), nil
}

func findAgyLog(cwd string) (string, error) {
	root, err := agyHome()
	if err != nil {
		return "", err
	}
	return findAgyLogIn(root, cwd)
}

// findAgyLogIn follows Antigravity CLI's own workspace-to-conversation index,
// the one `agy --continue` uses, to that conversation's step log.
func findAgyLogIn(root, cwd string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "cache", "last_conversations.json"))
	if err != nil {
		return "", fmt.Errorf("read Antigravity conversation index: %w", err)
	}
	var index map[string]string
	if err := json.Unmarshal(data, &index); err != nil {
		return "", fmt.Errorf("parse Antigravity conversation index: %w", err)
	}
	id := index[cwd]
	if id == "" {
		return "", fmt.Errorf("no Antigravity conversation for %s", cwd)
	}
	return filepath.Join(root, "brain", filepath.Base(id), ".system_generated", "logs", "transcript.jsonl"), nil
}

// findLogByHeader returns the newest log under dir whose name passes nameOK and
// whose first line passes headerOK. Only the header is read, so scanning every
// log stays cheap as the directory grows.
func findLogByHeader(dir, label string, nameOK func(string) bool, headerOK func([]byte) bool) (string, error) {
	var matches []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && nameOK(d.Name()) && headerOK(firstLine(path)) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("search %s session logs: %w", label, err)
	}
	path := newestFile(matches)
	if path == "" {
		return "", fmt.Errorf("no %s session log for this worktree in %s", label, dir)
	}
	return path, nil
}

func firstLine(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	return line
}

func newestFile(paths []string) string {
	newest := ""
	var newestTime time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest, newestTime = path, info.ModTime()
		}
	}
	return newest
}

// injectedContext matches messages the CLIs insert on the user's behalf, such
// as Codex's <environment_context> or Claude Code's <command-name> blocks.
var injectedContext = regexp.MustCompile(`(?s)^<[a-z][a-z0-9_-]*>.*</[a-z][a-z0-9_-]*>$`)

func isInjected(text string) bool {
	return injectedContext.MatchString(text) || strings.HasPrefix(text, "# AGENTS.md instructions")
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseClaudeLog(r io.Reader) ([]turn, error) {
	var turns []turn
	err := eachJSONLine(r, func(line []byte) {
		var record struct {
			Type        string `json:"type"`
			IsMeta      bool   `json:"isMeta"`
			IsSidechain bool   `json:"isSidechain"`
			Message     struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &record) != nil || record.IsMeta || record.IsSidechain {
			return
		}
		var r role
		switch record.Type {
		case "user":
			r = roleUser
		case "assistant":
			r = roleAssistant
		default:
			return
		}
		turns = appendTurn(turns, r, claudeText(record.Message.Content))
	})
	if err != nil {
		return nil, err
	}
	if len(turns) == 0 {
		return nil, errors.New("no conversation messages found")
	}
	return turns, nil
}

// claudeText accepts both content shapes Claude Code writes: a plain string for
// typed prompts and a block list for everything else. Only text blocks count;
// tool_use and tool_result blocks are left to the worktree itself.
func claudeText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []contentBlock
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func parseCodexLog(r io.Reader) ([]turn, error) {
	var turns []turn
	err := eachJSONLine(r, func(line []byte) {
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string         `json:"type"`
				Role    string         `json:"role"`
				Content []contentBlock `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil ||
			record.Type != "response_item" || record.Payload.Type != "message" {
			return
		}
		var r role
		switch record.Payload.Role {
		case "user":
			r = roleUser
		case "assistant":
			r = roleAssistant
		default:
			return
		}
		for _, block := range record.Payload.Content {
			if block.Type == "input_text" || block.Type == "output_text" {
				turns = appendTurn(turns, r, block.Text)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if len(turns) == 0 {
		return nil, errors.New("no conversation messages found")
	}
	return turns, nil
}

type agyStep struct {
	Index     int               `json:"step_index"`
	Type      string            `json:"type"`
	Status    string            `json:"status"`
	Content   string            `json:"content"`
	ToolCalls []json.RawMessage `json:"tool_calls"`
}

// readAgySteps keeps the last record per step index, so a step logged again
// with a later status replaces its earlier record.
func readAgySteps(r io.Reader) ([]agyStep, error) {
	byIndex := map[int]agyStep{}
	var order []int
	err := eachJSONLine(r, func(line []byte) {
		var step agyStep
		if json.Unmarshal(line, &step) != nil {
			return
		}
		if _, seen := byIndex[step.Index]; !seen {
			order = append(order, step.Index)
		}
		byIndex[step.Index] = step
	})
	if err != nil {
		return nil, err
	}
	sort.Ints(order)
	steps := make([]agyStep, len(order))
	for i, idx := range order {
		steps[i] = byIndex[idx]
	}
	return steps, nil
}

// agyUserRequest matches the wrapper agy puts around what the user typed; the
// rest of a USER_INPUT step is metadata such as the local time.
var agyUserRequest = regexp.MustCompile(`(?s)<USER_REQUEST>(.*?)</USER_REQUEST>`)

func agyUserText(content string) string {
	if m := agyUserRequest.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	return content
}

func parseAgyLog(r io.Reader) ([]turn, error) {
	steps, err := readAgySteps(r)
	if err != nil {
		return nil, err
	}
	var turns []turn
	for _, step := range steps {
		switch step.Type {
		case "USER_INPUT":
			turns = appendTurn(turns, roleUser, agyUserText(step.Content))
		case "PLANNER_RESPONSE":
			turns = appendTurn(turns, roleAssistant, step.Content)
		}
	}
	if len(turns) == 0 {
		return nil, errors.New("no conversation messages found")
	}
	return turns, nil
}

func agyLatestReply(cwd string) (string, string, error) {
	path, err := findAgyLog(cwd)
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("open Antigravity session log: %w", err)
	}
	defer f.Close()
	return latestAgyReply(f)
}

// latestAgyReply returns the reply only when it is the last step of the log
// and calls no tools. Anything else is narration while agy is still working,
// which can look idle on screen (for example at a permission prompt).
func latestAgyReply(r io.Reader) (string, string, error) {
	steps, err := readAgySteps(r)
	if err != nil {
		return "", "", err
	}
	if n := len(steps); n > 0 {
		last := steps[n-1]
		if last.Type == "PLANNER_RESPONSE" && last.Status == "DONE" && len(last.ToolCalls) == 0 && strings.TrimSpace(last.Content) != "" {
			return strings.TrimSpace(last.Content), strconv.Itoa(last.Index), nil
		}
	}
	return "", "", errors.New("no completed Gemini reply to the latest prompt yet")
}

// appendTurn merges consecutive messages from the same speaker, because both
// CLIs split one reply into several records around tool calls.
func appendTurn(turns []turn, r role, text string) []turn {
	text = strings.TrimSpace(text)
	if text == "" || isInjected(text) {
		return turns
	}
	if n := len(turns); n > 0 && turns[n-1].role == r {
		turns[n-1].text += "\n\n" + text
		return turns
	}
	return append(turns, turn{role: r, text: text})
}

// eachJSONLine uses ReadBytes instead of bufio.Scanner because a single record
// can exceed Scanner's token limit.
func eachJSONLine(r io.Reader, fn func([]byte)) error {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			fn(line)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
