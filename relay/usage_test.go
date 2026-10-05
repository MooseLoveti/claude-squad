package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var usageNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func codexTokenCount(ts time.Time, primaryUsed float64, primaryReset time.Time) string {
	return `{"timestamp":"` + ts.Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"token_count","info":{},` +
		`"rate_limits":{"limit_id":"codex","primary":{"used_percent":` + jsonNumber(primaryUsed) + `,"window_minutes":300,"resets_at":` +
		jsonNumber(float64(primaryReset.Unix())) + `},"secondary":{"used_percent":16.0,"window_minutes":10080,"resets_at":` +
		jsonNumber(float64(usageNow.Add(72*time.Hour).Unix())) + `},"plan_type":"plus"}}}`
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func writeRollout(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "2026", "10", "05", name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return path
}

func TestCodexUsageTakesTheNewestReadingAcrossSessions(t *testing.T) {
	dir := t.TempDir()
	reset := usageNow.Add(2 * time.Hour)
	old := writeRollout(t, dir, "rollout-a.jsonl",
		`{"type":"session_meta","payload":{"cwd":"/x"}}`,
		codexTokenCount(usageNow.Add(-time.Hour), 90, reset))
	writeRollout(t, dir, "rollout-b.jsonl",
		codexTokenCount(usageNow.Add(-3*time.Hour), 50, reset),
		codexTokenCount(usageNow.Add(-30*time.Minute), 25, reset),
		`{"type":"event_msg","payload":{"type":"agent_message","message":"no limits here"}}`)
	// The older-looking file is touched last; the reading's own time decides.
	require.NoError(t, os.Chtimes(old, usageNow, usageNow))

	usage, err := codexUsageIn(dir, usageNow)
	require.NoError(t, err)
	require.Equal(t, usageNow.Add(-30*time.Minute), usage.Updated.UTC())
	require.Len(t, usage.Windows, 2)
	require.Equal(t, "5h", usage.Windows[0].Name)
	require.InDelta(t, 0.75, usage.Windows[0].Remaining, 1e-9)
	require.Equal(t, reset.Unix(), usage.Windows[0].ResetsAt.Unix())
	require.Equal(t, "週", usage.Windows[1].Name)
	require.InDelta(t, 0.84, usage.Windows[1].Remaining, 1e-9)
}

func TestCodexUsageTreatsAPassedResetAsFull(t *testing.T) {
	dir := t.TempDir()
	writeRollout(t, dir, "rollout-a.jsonl", codexTokenCount(usageNow.Add(-6*time.Hour), 97, usageNow.Add(-time.Hour)))
	usage, err := codexUsageIn(dir, usageNow)
	require.NoError(t, err)
	require.Equal(t, 1.0, usage.Windows[0].Remaining)
}

func TestCodexUsageWithoutReadings(t *testing.T) {
	_, err := codexUsageIn(filepath.Join(t.TempDir(), "missing"), usageNow)
	require.ErrorIs(t, err, ErrNoUsage)

	dir := t.TempDir()
	writeRollout(t, dir, "rollout-a.jsonl", `not json "rate_limits"`, `{"type":"event_msg","payload":{"type":"token_count","rate_limits":null}}`)
	writeRollout(t, dir, "other.jsonl", codexTokenCount(usageNow, 10, usageNow.Add(time.Hour)))
	_, err = codexUsageIn(dir, usageNow)
	require.ErrorIs(t, err, ErrNoUsage, "malformed lines and non-rollout files are ignored")
}

func TestReadTailDropsThePartialFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	require.NoError(t, os.WriteFile(path, []byte("first line\nsecond\nthird\n"), 0o644))
	data, err := readTail(path, 10)
	require.NoError(t, err)
	require.Equal(t, "third\n", string(data))
	data, err = readTail(path, 1000)
	require.NoError(t, err)
	require.Equal(t, "first line\nsecond\nthird\n", string(data))
}

func TestClaudeStatusLineRoundTrip(t *testing.T) {
	configDir := t.TempDir()
	env := UsageEnv{ConfigDir: configDir, Now: func() time.Time { return usageNow }}
	_, err := claudeUsage(context.Background(), env)
	require.ErrorIs(t, err, ErrNoUsage, "nothing recorded yet")

	status := `{"model":{"display_name":"Opus"},"context_window":{"used_percentage":35},` +
		`"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":` + jsonNumber(float64(usageNow.Add(time.Hour).Unix())) + `},` +
		`"seven_day":{"used_percentage":18,"resets_at":"` + usageNow.Add(48*time.Hour).Format(time.RFC3339) + `"}}}`
	var out bytes.Buffer
	require.NoError(t, WriteClaudeStatusLine(strings.NewReader(status), &out, configDir))
	require.Equal(t, "5h 残り58% · 週 残り82% · context 35%\n", out.String())

	usage, err := claudeUsage(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, usage.Windows, 2)
	require.InDelta(t, 0.58, usage.Windows[0].Remaining, 1e-9)
	require.Equal(t, usageNow.Add(time.Hour).Unix(), usage.Windows[0].ResetsAt.Unix())
	require.InDelta(t, 0.82, usage.Windows[1].Remaining, 1e-9)
	require.Equal(t, usageNow.Add(48*time.Hour).Unix(), usage.Windows[1].ResetsAt.Unix())

	// Before its first response Claude Code sends no rate limits; the last
	// recorded ones stay.
	out.Reset()
	require.NoError(t, WriteClaudeStatusLine(strings.NewReader(`{"context_window":{"used_percentage":2}}`), &out, configDir))
	require.Equal(t, "context 2%\n", out.String())
	usage, err = claudeUsage(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, usage.Windows, 2)
}

func TestClaudeStatusLineRejectsMalformedInput(t *testing.T) {
	configDir := t.TempDir()
	require.Error(t, WriteClaudeStatusLine(strings.NewReader("not json"), &bytes.Buffer{}, configDir))
	require.NoFileExists(t, claudeStatusFile(configDir))

	require.NoError(t, os.MkdirAll(filepath.Dir(claudeStatusFile(configDir)), 0o755))
	require.NoError(t, os.WriteFile(claudeStatusFile(configDir), []byte("{"), 0o644))
	_, err := claudeUsage(context.Background(), UsageEnv{ConfigDir: configDir})
	require.Error(t, err)
}

func TestParseResetTime(t *testing.T) {
	require.Equal(t, int64(1791043388), parseResetTime(json.RawMessage(`1791043388`)).Unix())
	require.Equal(t, int64(1791043388), parseResetTime(json.RawMessage(`1791043388000`)).Unix(), "milliseconds")
	require.Equal(t, int64(1791043388), parseResetTime(json.RawMessage(`"`+time.Unix(1791043388, 0).UTC().Format(time.RFC3339)+`"`)).Unix())
	require.True(t, parseResetTime(json.RawMessage(`"soon"`)).IsZero())
	require.True(t, parseResetTime(nil).IsZero())
}

const agyUsageOutput = `{"conversation_id":"","status":"SUCCESS","response":"…","command":{"name":"usage","data":{"groups":[` +
	`{"name":"Gemini Models","buckets":[{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.99,"reset_time":"2026-10-10T10:46:09Z"},` +
	`{"id":"gemini-5h","window":"5h","remaining_fraction":0.4,"reset_time":"2026-10-05T11:00:00Z"}]},` +
	`{"name":"Claude and GPT models","buckets":[{"id":"3p-5h","window":"5h","remaining_fraction":1,"reset_time":"2026-10-05T15:00:00Z"}]}]}}}`

func TestParseAgyUsage(t *testing.T) {
	usage, err := parseAgyUsage([]byte(agyUsageOutput), usageNow)
	require.NoError(t, err)
	require.Len(t, usage.Windows, 2, "only the Gemini models' quota")
	require.Equal(t, "5h", usage.Windows[0].Name, "the 5-hour window comes first")
	require.Equal(t, 1.0, usage.Windows[0].Remaining, "the 5h window reset at 11:00, before now")
	require.Equal(t, "週", usage.Windows[1].Name)
	require.Equal(t, 0.99, usage.Windows[1].Remaining)
	require.Equal(t, usageNow, usage.Updated)

	_, err = parseAgyUsage([]byte(`{"command":{"data":{"groups":[{"name":"Claude and GPT models","buckets":[{"window":"5h","remaining_fraction":1}]}]}}}`), usageNow)
	require.ErrorIs(t, err, ErrNoUsage, "no Gemini quota reported")
	_, err = parseAgyUsage([]byte(`{"command":{"data":{"groups":[]}}}`), usageNow)
	require.ErrorIs(t, err, ErrNoUsage)
	_, err = parseAgyUsage([]byte("Error: not signed in"), usageNow)
	require.Error(t, err)
}

func TestAgyUsageRunsPrintModeInTheConfigDir(t *testing.T) {
	bin, configDir := t.TempDir(), t.TempDir()
	agy := filepath.Join(bin, "agy")
	script := "#!/bin/sh\necho \"$PWD $*\" > " + filepath.Join(bin, "run.log") + "\ncat <<'EOF'\n" + agyUsageOutput + "\nEOF\n"
	require.NoError(t, os.WriteFile(agy, []byte(script), 0o755))

	usage, err := AgentAntigravity.Usage(context.Background(), UsageEnv{ConfigDir: configDir, Program: agy + " --dangerously-skip-permissions", Now: func() time.Time { return usageNow }})
	require.NoError(t, err)
	require.Len(t, usage.Windows, 2)
	run, err := os.ReadFile(filepath.Join(bin, "run.log"))
	require.NoError(t, err)
	require.Equal(t, configDir+" -p /usage --output-format json\n", string(run), "profile flags are left out")

	require.NoError(t, os.WriteFile(agy, []byte("#!/bin/sh\nexit 3\n"), 0o755))
	_, err = AgentAntigravity.Usage(context.Background(), UsageEnv{ConfigDir: configDir, Program: agy})
	require.Error(t, err)
	_, err = AgentAntigravity.Usage(context.Background(), UsageEnv{ConfigDir: configDir})
	require.Error(t, err, "no program configured")
	_, err = AgentUnknown.Usage(context.Background(), UsageEnv{})
	require.Error(t, err)
}

func TestUsagePollInterval(t *testing.T) {
	for _, agent := range Agents() {
		require.Positive(t, agent.UsagePollInterval(), agent.Label())
	}
	require.Greater(t, AgentAntigravity.UsagePollInterval(), AgentCodex.UsagePollInterval(), "agy's usage costs a network call")
}

func TestWindowName(t *testing.T) {
	require.Equal(t, "5h", windowName(300))
	require.Equal(t, "週", windowName(10080))
	require.Equal(t, "2d", windowName(2880))
	require.Equal(t, "3h", windowName(180))
	require.Equal(t, "45m", windowName(45))
}

func TestAgyStepsBusy(t *testing.T) {
	step := func(i int, typ string, tools int) string {
		calls := "[]"
		if tools > 0 {
			calls = `[{"name":"run"}]`
		}
		return `{"step_index":` + jsonNumber(float64(i)) + `,"type":"` + typ + `","status":"DONE","content":"text","tool_calls":` + calls + `}`
	}
	busy := func(lines ...string) bool { return agyStepsBusy(strings.NewReader(strings.Join(lines, "\n"))) }
	require.False(t, busy(), "no steps: nothing asked yet")
	require.True(t, busy(step(0, "USER_INPUT", 0)), "a prompt without a reply yet")
	require.True(t, busy(step(0, "USER_INPUT", 0), step(1, "PLANNER_RESPONSE", 1)), "a reply that calls tools")
	require.True(t, busy(step(0, "USER_INPUT", 0), step(1, "PLANNER_RESPONSE", 1), step(2, "GENERIC", 0)))
	require.False(t, busy(step(0, "USER_INPUT", 0), step(1, "PLANNER_RESPONSE", 0)))
}
