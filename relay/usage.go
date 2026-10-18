package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// UsageWindow is one rate-limit window and how much of it is left.
type UsageWindow struct {
	// Name is a short label such as "5h", "週" or "Gemini 5h".
	Name string
	// Remaining is the fraction of the window still available, 0 to 1.
	Remaining float64
	// ResetsAt is when the window refills; zero when unknown.
	ResetsAt time.Time
}

// Balance is a prepaid account's remaining money.
type Balance struct {
	// Amount is the balance as the provider writes it, such as "12.34".
	Amount   string
	Currency string
	// Available is false when the balance no longer covers a request.
	Available bool
}

// Symbol is the sign written before the amount, or the currency code with a
// space for a currency without one here.
func (b Balance) Symbol() string {
	switch b.Currency {
	case "USD":
		return "$"
	case "CNY":
		return "¥"
	}
	return b.Currency + " "
}

// Usage is an account's remaining rate limits, or for a prepaid account its
// balance, as of Updated.
type Usage struct {
	Windows  []UsageWindow
	Balances []Balance
	Updated  time.Time
}

// UsageEnv is what reading an agent's usage may need.
type UsageEnv struct {
	// ConfigDir is claude-squad's own directory.
	ConfigDir string
	// Program is the agent's configured command.
	Program string
	Now     func() time.Time
}

func (e UsageEnv) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// ErrNoUsage means the agent has not reported its limits yet.
var ErrNoUsage = errors.New("no usage reported yet")

// Usage reads the agent's remaining rate limits.
func (a Agent) Usage(ctx context.Context, env UsageEnv) (Usage, error) {
	spec, ok := specs[a]
	if !ok || spec.usage == nil {
		return Usage{}, fmt.Errorf("%s does not report usage", a.Label())
	}
	return spec.usage(ctx, env)
}

// UsagePollInterval is how often the agent's usage is worth reading.
func (a Agent) UsagePollInterval() time.Duration {
	return specs[a].usagePoll
}

// localUsagePoll suits usage read from local files; networkUsagePoll suits
// usage that needs a call to the provider.
const (
	localUsagePoll   = 30 * time.Second
	networkUsagePoll = 5 * time.Minute
)

func windowName(minutes int) string {
	switch {
	case minutes == 300:
		return "5h"
	case minutes == 10080:
		return "週"
	case minutes%1440 == 0:
		return strconv.Itoa(minutes/1440) + "d"
	case minutes%60 == 0:
		return strconv.Itoa(minutes/60) + "h"
	default:
		return strconv.Itoa(minutes) + "m"
	}
}

// remaining turns a used percentage into the fraction left, treating a window
// whose reset time has passed as full again: the reading predates the reset.
func remaining(usedPercent float64, resetsAt, now time.Time) float64 {
	if !resetsAt.IsZero() && !resetsAt.After(now) {
		return 1
	}
	left := 1 - usedPercent/100
	return min(max(left, 0), 1)
}

// codexTailBytes bounds how much of a session log is read; Codex logs a
// token_count event after every model response, so the tail has the latest.
const codexTailBytes = 1 << 20

// codexRecentLogs bounds how many of the newest session logs are searched.
const codexRecentLogs = 5

// codexUsage reads the rate limits Codex logs with every model response. They
// are account-wide, so the newest reading across all sessions wins.
func codexUsage(_ context.Context, env UsageEnv) (Usage, error) {
	root, err := envOrHome("CODEX_HOME", ".codex")
	if err != nil {
		return Usage{}, err
	}
	return codexUsageIn(filepath.Join(root, "sessions"), env.now())
}

func codexUsageIn(dir string, now time.Time) (Usage, error) {
	logs, err := newestFiles(dir, codexRecentLogs, func(name string) bool {
		return strings.HasPrefix(name, "rollout-") && filepath.Ext(name) == ".jsonl"
	})
	if err != nil {
		return Usage{}, err
	}
	var best *codexRateLimits
	var bestAt time.Time
	for _, path := range logs {
		limits, at, ok := lastCodexRateLimits(path)
		if ok && at.After(bestAt) {
			best, bestAt = limits, at
		}
	}
	if best == nil {
		return Usage{}, ErrNoUsage
	}
	usage := Usage{Updated: bestAt}
	for _, w := range []*codexWindow{best.Primary, best.Secondary} {
		if w == nil {
			continue
		}
		resets := time.Time{}
		if w.ResetsAt > 0 {
			resets = time.Unix(w.ResetsAt, 0)
		}
		usage.Windows = append(usage.Windows, UsageWindow{
			Name:      windowName(w.WindowMinutes),
			Remaining: remaining(w.UsedPercent, resets, now),
			ResetsAt:  resets,
		})
	}
	return usage, nil
}

type codexWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexRateLimits struct {
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

func lastCodexRateLimits(path string) (*codexRateLimits, time.Time, bool) {
	data, err := readTail(path, codexTailBytes)
	if err != nil {
		return nil, time.Time{}, false
	}
	var found *codexRateLimits
	var at time.Time
	_ = eachJSONLine(bytes.NewReader(data), func(line []byte) {
		if !bytes.Contains(line, []byte(`"rate_limits"`)) {
			return
		}
		var event struct {
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Payload   struct {
				Type       string           `json:"type"`
				RateLimits *codexRateLimits `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "event_msg" ||
			event.Payload.Type != "token_count" || event.Payload.RateLimits == nil {
			return
		}
		found, at = event.Payload.RateLimits, event.Timestamp
	})
	return found, at, found != nil
}

// readTail reads at most n bytes from the end of a file, dropping the partial
// first line when it starts mid-file.
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := max(info.Size()-n, 0)
	data, err := io.ReadAll(io.NewSectionReader(f, offset, info.Size()-offset))
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return data, nil
}

// newestFiles returns up to limit files under dir accepted by nameOK, newest
// modification first.
func newestFiles(dir string, limit int, nameOK func(string) bool) ([]string, error) {
	type entry struct {
		path string
		mod  time.Time
	}
	var entries []entry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !nameOK(d.Name()) {
			return nil
		}
		if info, err := d.Info(); err == nil {
			entries = append(entries, entry{path, info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod.After(entries[j].mod) })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}
	return paths, nil
}

// claudeStatusFile is where `cs statusline` keeps the latest status Claude
// Code handed it; Claude Code reports rate limits nowhere else on disk.
func claudeStatusFile(configDir string) string {
	return filepath.Join(configDir, "usage", "claude.json")
}

// claudeLaunchArgs let Claude Code read every worktree and transcript, add
// the briefing to its system prompt, and install `cs statusline` as its status
// line so its rate limits reach the dashboard. --settings layers on top of the
// user's own settings files.
func claudeLaunchArgs(env LaunchEnv) []string {
	args := []string{"--add-dir", env.SharedDir}
	if env.Briefing != "" {
		args = append(args, "--append-system-prompt", env.Briefing)
	}
	if env.Self != "" {
		settings, _ := json.Marshal(map[string]any{
			"statusLine": map[string]string{"type": "command", "command": shellQuote(env.Self) + " statusline"},
		})
		args = append(args, "--settings", string(settings))
	}
	return args
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type claudeLimit struct {
	UsedPercentage float64         `json:"used_percentage"`
	ResetsAt       json.RawMessage `json:"resets_at"`
}

type claudeStatus struct {
	RateLimits *struct {
		FiveHour *claudeLimit `json:"five_hour"`
		SevenDay *claudeLimit `json:"seven_day"`
	} `json:"rate_limits"`
	ContextWindow *struct {
		UsedPercentage *float64 `json:"used_percentage"`
	} `json:"context_window"`
}

// parseResetTime accepts Unix seconds, Unix milliseconds or an RFC 3339 string.
func parseResetTime(raw json.RawMessage) time.Time {
	var seconds float64
	if json.Unmarshal(raw, &seconds) == nil && seconds > 0 {
		if seconds > 1e12 {
			return time.UnixMilli(int64(seconds))
		}
		return time.Unix(int64(seconds), 0)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if t, err := time.Parse(time.RFC3339, text); err == nil {
			return t
		}
	}
	return time.Time{}
}

func claudeUsage(_ context.Context, env UsageEnv) (Usage, error) {
	path := claudeStatusFile(env.ConfigDir)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Usage{}, ErrNoUsage
	}
	if err != nil {
		return Usage{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Usage{}, err
	}
	var status claudeStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return Usage{}, fmt.Errorf("parse Claude Code status: %w", err)
	}
	if status.RateLimits == nil {
		return Usage{}, ErrNoUsage
	}
	usage := Usage{Updated: info.ModTime()}
	for _, w := range []struct {
		name  string
		limit *claudeLimit
	}{{"5h", status.RateLimits.FiveHour}, {"週", status.RateLimits.SevenDay}} {
		if w.limit == nil {
			continue
		}
		resets := parseResetTime(w.limit.ResetsAt)
		usage.Windows = append(usage.Windows, UsageWindow{
			Name:      w.name,
			Remaining: remaining(w.limit.UsedPercentage, resets, env.now()),
			ResetsAt:  resets,
		})
	}
	return usage, nil
}

// WriteClaudeStatusLine is Claude Code's status line command: it keeps the
// status JSON Claude Code passes on stdin for the dashboard and prints a short
// summary for Claude Code's own status line. A status without rate limits
// (before the first response) leaves the last saved one in place.
func WriteClaudeStatusLine(stdin io.Reader, stdout io.Writer, configDir string) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	var status claudeStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return fmt.Errorf("parse Claude Code status: %w", err)
	}
	var parts []string
	if status.RateLimits != nil {
		if err := writeFileAtomic(claudeStatusFile(configDir), data); err != nil {
			return err
		}
		if l := status.RateLimits.FiveHour; l != nil {
			parts = append(parts, fmt.Sprintf("5h 残り%.0f%%", 100-l.UsedPercentage))
		}
		if l := status.RateLimits.SevenDay; l != nil {
			parts = append(parts, fmt.Sprintf("週 残り%.0f%%", 100-l.UsedPercentage))
		}
	}
	if c := status.ContextWindow; c != nil && c.UsedPercentage != nil {
		parts = append(parts, fmt.Sprintf("context %.0f%%", *c.UsedPercentage))
	}
	_, err = fmt.Fprintln(stdout, strings.Join(parts, " · "))
	return err
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// deepseekBalanceURL reports a DeepSeek account's balance without running a
// model; see https://api-docs.deepseek.com/api/get-user-balance.
var deepseekBalanceURL = "https://api.deepseek.com/user/balance"

const deepseekUsageTimeout = 30 * time.Second

// deepseekKeyVar names the API key in the environment and in Reasonix's .env.
const deepseekKeyVar = "DEEPSEEK_API_KEY"

// deepseekUsage reads the account balance with the key Reasonix uses: the
// environment's, or else the one `reasonix setup` saved.
func deepseekUsage(ctx context.Context, env UsageEnv) (Usage, error) {
	key, err := deepseekKey()
	if err != nil {
		return Usage{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, deepseekUsageTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, deepseekBalanceURL, nil)
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Usage{}, fmt.Errorf("DeepSeek の残高: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Usage{}, fmt.Errorf("DeepSeek の残高: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Usage{}, fmt.Errorf("DeepSeek の残高: HTTP %d", resp.StatusCode)
	}
	return parseDeepSeekBalance(body, env.now())
}

func parseDeepSeekBalance(body []byte, now time.Time) (Usage, error) {
	var result struct {
		IsAvailable  bool `json:"is_available"`
		BalanceInfos []struct {
			Currency     string `json:"currency"`
			TotalBalance string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return Usage{}, fmt.Errorf("parse DeepSeek balance: %w", err)
	}
	usage := Usage{Updated: now}
	for _, info := range result.BalanceInfos {
		usage.Balances = append(usage.Balances, Balance{Amount: info.TotalBalance, Currency: info.Currency, Available: result.IsAvailable})
	}
	if len(usage.Balances) == 0 {
		return Usage{}, errors.New("DeepSeek から残高が返ってきませんでした")
	}
	return usage, nil
}

// deepseekKey is the API key from the environment, or else from the .env in
// Reasonix's home ($REASONIX_HOME, by default ~/.reasonix).
func deepseekKey() (string, error) {
	if key := os.Getenv(deepseekKeyVar); key != "" {
		return key, nil
	}
	home, err := reasonixHome()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(home, ".env"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if key := dotenvValue(string(data), deepseekKeyVar); key != "" {
		return key, nil
	}
	return "", errors.New("DeepSeek の API キーがありません（reasonix を起動してキーを登録してください）")
}

// dotenvValue reads name from .env text: NAME=value lines, optionally with
// "export " in front and the value in quotes.
func dotenvValue(text, name string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != name {
			continue
		}
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return strings.Trim(value, "'")
	}
	return ""
}

// agyUsageTimeout bounds the network call behind agy's /usage command.
const agyUsageTimeout = time.Minute

// agyUsage asks agy's print mode for /usage, which calls the quota service but
// runs no agent turn. It runs in claude-squad's own directory so it never
// becomes the latest conversation of a worktree.
func agyUsage(ctx context.Context, env UsageEnv) (Usage, error) {
	fields := strings.Fields(env.Program)
	if len(fields) == 0 {
		return Usage{}, errors.New("Gemini のコマンドが設定されていません")
	}
	ctx, cancel := context.WithTimeout(ctx, agyUsageTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, fields[0], "-p", "/usage", "--output-format", "json")
	cmd.Dir = env.ConfigDir
	out, err := cmd.Output()
	if err != nil {
		return Usage{}, fmt.Errorf("agy /usage: %w", err)
	}
	return parseAgyUsage(out, env.now())
}

func parseAgyUsage(out []byte, now time.Time) (Usage, error) {
	var result struct {
		Command struct {
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						Window            string    `json:"window"`
						RemainingFraction float64   `json:"remaining_fraction"`
						ResetTime         time.Time `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return Usage{}, fmt.Errorf("parse agy /usage: %w", err)
	}
	usage := Usage{Updated: now}
	for _, group := range result.Command.Data.Groups {
		// agy also reports the quota of the Claude and GPT models it can
		// run; only the Gemini models are used here.
		if !strings.Contains(group.Name, "Gemini") {
			continue
		}
		for _, bucket := range group.Buckets {
			window := bucket.Window
			if window == "weekly" {
				window = "週"
			}
			left := bucket.RemainingFraction
			if !bucket.ResetTime.IsZero() && !bucket.ResetTime.After(now) {
				left = 1
			}
			usage.Windows = append(usage.Windows, UsageWindow{
				Name:      window,
				Remaining: min(max(left, 0), 1),
				ResetsAt:  bucket.ResetTime,
			})
		}
	}
	if len(usage.Windows) == 0 {
		return Usage{}, ErrNoUsage
	}
	// agy lists the weekly window first; show the 5-hour one first like the
	// other agents.
	sort.SliceStable(usage.Windows, func(i, j int) bool {
		return usage.Windows[i].Name == "5h" && usage.Windows[j].Name != "5h"
	})
	return usage, nil
}
