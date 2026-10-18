package relay

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	xansi "github.com/charmbracelet/x/ansi"
)

// Agent identifies a CLI that can take part in relays.
type Agent int

const (
	AgentUnknown Agent = iota
	AgentClaude
	AgentCodex
	AgentAntigravity
	AgentDeepSeek
)

// Slot is a pane position in the dashboard: agents in a 2×2 grid, the control
// panel in a full-width row under it.
type Slot int

const (
	SlotTopLeft Slot = iota
	SlotTopRight
	SlotBottomLeft
	SlotBottomRight
	SlotBottom
)

// LaunchEnv is what an agent's launch arguments may refer to.
type LaunchEnv struct {
	// SharedDir holds every worktree and relay transcript.
	SharedDir string
	// Self is the claude-squad executable, for CLIs that call back into it.
	Self string
	// Briefing is told to the agent before its first prompt; see Briefing.
	Briefing string
}

// agentSpec holds everything that differs between relay agents. Supporting a
// new CLI means adding one Agent constant and one entry in specs.
type agentSpec struct {
	// executable is matched against the configured program.
	executable string
	label      string
	// emoji and color (hex) mark the agent in the dashboard.
	emoji string
	color string
	// slot is where the agent's pane sits in the dashboard.
	slot Slot
	// tags address the agent in a reply; the first one is canonical.
	tags []string
	// responseMarkers start an assistant reply at column zero in the TUI.
	responseMarkers []string
	// boundaryPrefixes end a reply when they start a line at column zero:
	// input prompts, spinners and box borders.
	boundaryPrefixes []string
	// inputPrefix starts the input box line at column zero; what follows it
	// (and its indented continuation lines) is the text being typed.
	inputPrefix string
	// replyFooter matches status lines a CLI prints right under a finished
	// reply, which are trimmed off the reply's end.
	replyFooter *regexp.Regexp
	// busyMarkers appear near the bottom of the pane only while the agent is
	// working; no pane reply counts as finished while one is showing. Log-based
	// agents need none: their log marks a reply finished.
	busyMarkers []string
	// latestReply, when set, reads the latest reply from the session log
	// instead of the pane, for CLIs whose screen does not separate the reply
	// from other output. key identifies the reply across polls.
	latestReply func(cwd string) (text, key string, err error)
	// logBusy reports, for log-based agents, whether the latest prompt is
	// still being worked on.
	logBusy func(cwd string) bool
	// launchArgs grant access to the shared directory, so reading a peer never
	// stops on a permission prompt, pass the briefing, and wire up usage
	// reporting.
	launchArgs func(env LaunchEnv) []string
	// briefingFile is the instructions file the CLI reads from its working
	// directory, for CLIs that take no instructions on their command line; the
	// briefing is written there.
	briefingFile string
	// startupNotices are notices the CLI may show at launch, and the keys the
	// dashboard answers them with.
	startupNotices []startupNotice
	findLog        func(cwd string) (string, error)
	parseLog       func(io.Reader) ([]turn, error)
	// usage reads the account's remaining rate limits, worth doing every usagePoll.
	usage     func(ctx context.Context, env UsageEnv) (Usage, error)
	usagePoll time.Duration
}

// startupNotice is a notice a CLI shows at launch and the keys that answer it.
type startupNotice struct {
	marker string
	keys   []string
}

// relayAgents fixes the iteration order so routing and detection are deterministic.
var relayAgents = []Agent{AgentClaude, AgentCodex, AgentAntigravity, AgentDeepSeek}

var specs = map[Agent]agentSpec{
	AgentClaude: {
		executable:       "claude",
		label:            "Claude Code",
		emoji:            "🧠",
		color:            "#E8885F",
		slot:             SlotBottomLeft,
		tags:             []string{"@claude"},
		responseMarkers:  []string{"⏺", "●"},
		boundaryPrefixes: []string{"❯", "✻", "✽", "✢", "✶"},
		inputPrefix:      "❯",
		busyMarkers:      []string{"esc to interrupt"},
		launchArgs:       claudeLaunchArgs,
		findLog:          findClaudeLog,
		parseLog:         parseClaudeLog,
		usage:            claudeUsage,
		usagePoll:        localUsagePoll,
	},
	AgentCodex: {
		executable:       "codex",
		label:            "Codex",
		emoji:            "🤖",
		color:            "#19C37D",
		slot:             SlotTopLeft,
		tags:             []string{"@codex"},
		responseMarkers:  []string{"•"},
		boundaryPrefixes: []string{"›"},
		inputPrefix:      "›",
		// Codex marks its "Working" status line with the same bullet as replies.
		busyMarkers: []string{"esc to interrupt"},
		replyFooter: regexp.MustCompile(`^\s*(Worked for \d|└ Tip: )`),
		// Codex's read-only sandbox can already read the whole filesystem.
		launchArgs: codexLaunchArgs,
		startupNotices: []startupNotice{
			// Accounts with Daybreak models get a hardware security key
			// reminder on every launch, which config cannot turn off.
			{marker: "Set up security for Daybreak mode", keys: []string{"Escape"}},
			// A new release is offered at launch; Escape skips it this time
			// only, leaving updating to the user.
			{marker: "Skip until next version", keys: []string{"Escape"}},
		},
		findLog:   findCodexLog,
		parseLog:  parseCodexLog,
		usage:     codexUsage,
		usagePoll: localUsagePoll,
	},
	AgentAntigravity: {
		executable:  "agy",
		label:       "Gemini",
		emoji:       "✨",
		color:       "#5B9BFF",
		slot:        SlotBottomRight,
		tags:        []string{"@gemini", "@agy"},
		inputPrefix: ">",
		// The pane interleaves thinking summaries with the reply at the same
		// indentation, so the reply comes from the session log instead.
		latestReply: agyLatestReply,
		logBusy:     agyBusy,
		// --add-dir makes agy run commands in the added directory instead of
		// its own worktree; reads outside the workspace ask once instead.
		launchArgs:   func(LaunchEnv) []string { return nil },
		briefingFile: "GEMINI.md",
		findLog:      findAgyLog,
		parseLog:     parseAgyLog,
		usage:        agyUsage,
		usagePoll:    networkUsagePoll,
	},
	AgentDeepSeek: {
		executable: "reasonix",
		label:      "DeepSeek",
		emoji:      "🐋",
		color:      "#4D6BFE",
		slot:       SlotTopRight,
		tags:       []string{"@deepseek", "@reasonix"},
		// The input box is indented one column; continuation lines by three.
		inputPrefix: " ❯",
		// The pane shows replies without a marker, so they come from the
		// session's event log, which marks where each turn ends.
		latestReply: reasonixLatestReply,
		logBusy:     reasonixBusy,
		// Reasonix reads files anywhere and its sandboxed shell can read the
		// home directory, so peers' worktrees need no flag; --add-dir would
		// make them writable.
		launchArgs:   func(LaunchEnv) []string { return nil },
		briefingFile: "REASONIX.local.md",
		// Each new worktree is asked about once before the TUI starts. A
		// trusted folder runs edits and commands without asking, with writes
		// confined to the worktree by the OS sandbox.
		startupNotices: []startupNotice{{marker: "Trust this folder? (`reasonix trust --revoke` undoes it) [y/N]:", keys: []string{"y", "Enter"}}},
		findLog:        findReasonixLog,
		parseLog:       parseReasonixLog,
		usage:          deepseekUsage,
		usagePoll:      networkUsagePoll,
	},
}

// Agents lists every supported agent in a fixed order.
func Agents() []Agent {
	return append([]Agent(nil), relayAgents...)
}

// DetectAgent finds the agent executable in a configured program command.
func DetectAgent(program string) Agent {
	for _, field := range strings.Fields(program) {
		name := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.Trim(field, "\"'"))), ".exe")
		for _, agent := range relayAgents {
			if specs[agent].executable == name {
				return agent
			}
		}
	}
	return AgentUnknown
}

// AgentByName finds an agent by Name, as stored in saved state and tmux.
func AgentByName(name string) Agent {
	for _, agent := range relayAgents {
		if agent.Name() == name {
			return agent
		}
	}
	return AgentUnknown
}

func (a Agent) Label() string {
	if spec, ok := specs[a]; ok {
		return spec.label
	}
	return "Unknown agent"
}

// Tag is the canonical way to address this agent in a reply, e.g. "@codex".
func (a Agent) Tag() string {
	if spec, ok := specs[a]; ok {
		return spec.tags[0]
	}
	return ""
}

// Name is the canonical tag without "@", e.g. "codex"; it names the agent's
// branch, saved state and pane.
func (a Agent) Name() string {
	return strings.TrimPrefix(a.Tag(), "@")
}

// Executable is the command run when no profile configures the agent.
func (a Agent) Executable() string {
	return specs[a].executable
}

// Emoji marks the agent in the dashboard.
func (a Agent) Emoji() string {
	return specs[a].emoji
}

// Color is the agent's theme color as "#RRGGBB".
func (a Agent) Color() string {
	return specs[a].color
}

// BriefingFile is the instructions file the briefing is written to, or "" when
// the briefing goes on the command line.
func (a Agent) BriefingFile() string {
	return specs[a].briefingFile
}

// Slot is where the agent's pane sits in the dashboard.
func (a Agent) Slot() Slot {
	return specs[a].slot
}

// LaunchArgs returns extra CLI arguments for starting the agent; see agentSpec.
func (a Agent) LaunchArgs(env LaunchEnv) []string {
	if spec, ok := specs[a]; ok {
		return spec.launchArgs(env)
	}
	return nil
}

// Busy reports whether the agent is still working on its latest prompt, read
// from its session log or from the bottom of its pane.
func (a Agent) Busy(cwd, pane string) bool {
	spec, ok := specs[a]
	if !ok {
		return false
	}
	if spec.logBusy != nil {
		return spec.logBusy(cwd)
	}
	return isBusy(spec, pane)
}

// noticeTailLines reaches a notice's first line above the option list, hints,
// input box and status lines drawn under it.
const noticeTailLines = 20

// NoticeKeys returns the keys that dismiss a startup notice showing at the
// bottom of the pane, or nil when none is showing.
func (a Agent) NoticeKeys(pane string) []string {
	for _, n := range specs[a].startupNotices {
		if paneTailContains(pane, noticeTailLines, []string{n.marker}) {
			return n.keys
		}
	}
	return nil
}

// allTag addresses every other running agent at once.
const allTag = "@all"

type tagHit struct {
	agent      Agent // AgentUnknown for allTag
	start, end int
}

// Addressing is who a message is for and what it says.
type Addressing struct {
	// Agents are the specifically tagged agents, in relayAgents order.
	Agents []Agent
	// All means "@all": every other running agent.
	All     bool
	Message string
}

func (a Addressing) Tagged() bool {
	return a.All || len(a.Agents) > 0
}

// Route finds who a text addresses and the message meant for them. The
// message is the text after the last tag; the recipients are that tag plus any
// tags directly before it ("@claude @gemini review this"), so tags quoted
// earlier in the text do not fan out. A source never addresses its own agent.
func Route(source Agent, text string) Addressing {
	lower := strings.ToLower(text)
	var hits []tagHit
	addHits := func(agent Agent, tag string) {
		for from := 0; ; {
			idx := strings.Index(lower[from:], tag)
			if idx < 0 {
				return
			}
			start := from + idx
			hits = append(hits, tagHit{agent: agent, start: start, end: start + len(tag)})
			from = start + len(tag)
		}
	}
	addHits(AgentUnknown, allTag)
	for _, agent := range relayAgents {
		if agent == source {
			continue
		}
		for _, tag := range specs[agent].tags {
			addHits(agent, tag)
		}
	}
	if len(hits) == 0 {
		return Addressing{}
	}

	last := hits[0]
	for _, h := range hits {
		if h.start > last.start {
			last = h
		}
	}
	group := []tagHit{last}
	for groupStart := last.start; ; {
		prev, found := tagHit{}, false
		for _, h := range hits {
			// Only separators between a hit and the group means it is the tag
			// directly before the group.
			if h.end <= groupStart && strings.Trim(text[h.end:groupStart], tagSeparators) == "" {
				prev, found = h, true
				break
			}
		}
		if !found {
			break
		}
		group = append(group, prev)
		groupStart = prev.start
	}

	addressing := Addressing{Message: strings.TrimSpace(text[last.end:])}
	for _, agent := range relayAgents {
		for _, h := range group {
			if h.agent == agent {
				addressing.Agents = append(addressing.Agents, agent)
				break
			}
		}
	}
	for _, h := range group {
		if h.agent == AgentUnknown {
			addressing.All = true
		}
	}
	return addressing
}

// Leading reads tags written at the very start of a text, as a user types
// them ("@codex @claude what do you think?"), and the message after them. Text
// that does not start with a tag addresses no one, so mentioning "@claude" in
// an ordinary prompt never diverts it. A source's own tag is skipped.
func Leading(source Agent, text string) Addressing {
	var to Addressing
	rest := strings.TrimLeft(text, tagSeparators)
	for {
		agent, tag := leadingTag(source, rest)
		if tag == "" {
			break
		}
		switch {
		case agent == AgentUnknown:
			to.All = true
		case agent != source:
			to.Agents = appendAgent(to.Agents, agent)
		}
		rest = strings.TrimLeft(rest[len(tag):], tagSeparators)
	}
	if !to.Tagged() {
		return Addressing{}
	}
	ordered := make([]Agent, 0, len(to.Agents))
	for _, agent := range relayAgents {
		for _, a := range to.Agents {
			if a == agent {
				ordered = append(ordered, agent)
			}
		}
	}
	to.Agents = ordered
	to.Message = strings.TrimSpace(rest)
	return to
}

// leadingTag returns the tag text starts with, if it is a whole word ("@all"
// but not "@allison"), and the agent it names (AgentUnknown for "@all").
func leadingTag(source Agent, text string) (Agent, string) {
	lower := strings.ToLower(text)
	isWord := func(tag string) bool {
		if !strings.HasPrefix(lower, tag) {
			return false
		}
		next := lower[len(tag):]
		if next == "" {
			return true
		}
		r := []rune(next)[0]
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	}
	if isWord(allTag) {
		return AgentUnknown, text[:len(allTag)]
	}
	for _, agent := range relayAgents {
		for _, tag := range specs[agent].tags {
			if isWord(tag) {
				return agent, text[:len(tag)]
			}
		}
	}
	return AgentUnknown, ""
}

func appendAgent(agents []Agent, agent Agent) []Agent {
	for _, a := range agents {
		if a == agent {
			return agents
		}
	}
	return append(agents, agent)
}

// InputText returns what is typed in the agent's input box on the pane, read
// from the last input line and its indented continuation lines.
func (a Agent) InputText(pane string) (string, bool) {
	prefix := specs[a].inputPrefix
	if prefix == "" {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(xansi.Strip(pane), "\r", ""), "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			start = i
		}
	}
	if start < 0 {
		return "", false
	}
	// Claude Code puts a no-break space after its prompt glyph; TrimSpace
	// removes that as well as plain spaces.
	parts := []string{strings.TrimSpace(strings.TrimPrefix(lines[start], prefix))}
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(line, "  ") || isSeparator(trimmed) {
			break
		}
		parts = append(parts, trimmed)
	}
	return strings.Join(parts, "\n"), true
}

// tagSeparators may sit between grouped tags, e.g. "@claude, @gemini" or
// "@claude と @gemini".
const tagSeparators = " \t\n,、&と/"

// Sender describes where a relay participant's work can be inspected.
type Sender struct {
	Agent          Agent
	WorktreePath   string
	TranscriptPath string
	// Latest is the session's latest exchange, quoted in questions so the
	// receiver knows what a short question like "what do you think?" is about.
	Latest *Exchange
}

// quoteLimit keeps a quoted exchange short; the transcript has the rest.
const quoteLimit = 1500

const truncatedNote = " …（以下省略。全文は会話全文ファイルを参照）"

func quote(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= quoteLimit {
		return string(runes)
	}
	return string(runes[:quoteLimit]) + truncatedNote
}

// writeContext tells a recipient working in toDir where the sender's work is.
func (s Sender) writeContext(b *strings.Builder, toDir string) {
	if s.WorktreePath == toDir {
		fmt.Fprintf(b, "作業ディレクトリ（%s と共有）: %s\n", s.Agent.Label(), s.WorktreePath)
	} else {
		fmt.Fprintf(b, "%s の作業ディレクトリ（あなたは読み取りのみ）: %s\n", s.Agent.Label(), s.WorktreePath)
	}
	if s.TranscriptPath != "" {
		fmt.Fprintf(b, "%s の会話全文: %s\n", s.Agent.Label(), s.TranscriptPath)
	}
}

// LatestReply returns the agent's most recent reply and a stable identity for
// it. The identity ignores footer and status changes while still telling apart
// two separate replies with identical text.
func (a Agent) LatestReply(worktreePath, pane string) (string, [sha256.Size]byte, error) {
	spec, ok := specs[a]
	if !ok {
		return "", [sha256.Size]byte{}, fmt.Errorf("%s has no relay support", a.Label())
	}
	if spec.latestReply == nil {
		if isBusy(spec, pane) {
			return "", [sha256.Size]byte{}, fmt.Errorf("%s is still working", a.Label())
		}
		return latestPaneReply(a, pane)
	}
	text, key, err := spec.latestReply(worktreePath)
	if err != nil {
		return "", [sha256.Size]byte{}, err
	}
	return text, sha256.Sum256([]byte(key + "\x00" + text)), nil
}

// busyTailLines bounds the busy check to the status area at the bottom of the
// pane, so a marker quoted earlier in the conversation does not count.
const busyTailLines = 12

func isBusy(spec agentSpec, pane string) bool {
	return paneTailContains(pane, busyTailLines, spec.busyMarkers)
}

func paneTailContains(pane string, tailLines int, markers []string) bool {
	lines := strings.Split(strings.TrimRight(xansi.Strip(pane), " \t\r\n"), "\n")
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	tail := strings.Join(lines, "\n")
	for _, marker := range markers {
		if strings.Contains(tail, marker) {
			return true
		}
	}
	return false
}

// latestPaneReply extracts the last rendered reply from a terminal transcript.
// Each CLI prefixes reply blocks with a stable glyph; the next input prompt
// marks the end of that reply.
func latestPaneReply(agent Agent, pane string) (string, [sha256.Size]byte, error) {
	clean := strings.ReplaceAll(xansi.Strip(pane), "\r", "")
	lines := strings.Split(clean, "\n")

	start := -1
	first := ""
	occurrences := 0
	for idx, line := range lines {
		markerLine := strings.TrimRight(line, " \t")
		if text, ok := responseMarker(agent, markerLine); ok {
			occurrences++
			start = idx
			first = text
		}
	}
	if start < 0 {
		return "", [sha256.Size]byte{}, fmt.Errorf("no completed %s response found", agent.Label())
	}

	result := []string{first}
	for _, line := range lines[start+1:] {
		line = strings.TrimRight(line, " \t")
		if isResponseBoundary(agent, line) {
			break
		}
		result = append(result, line)
	}

	if footer := specs[agent].replyFooter; footer != nil {
		for len(result) > 1 && (strings.TrimSpace(result[len(result)-1]) == "" || footer.MatchString(result[len(result)-1])) {
			result = result[:len(result)-1]
		}
	}
	response := strings.TrimSpace(strings.Join(result, "\n"))
	if response == "" {
		return "", [sha256.Size]byte{}, fmt.Errorf("latest %s response is empty", agent.Label())
	}
	id := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", occurrences, response)))
	return response, id, nil
}

// isResponseBoundary matches prompts and spinners only at column zero, because
// reply bodies are indented and may legitimately contain the same glyphs.
func isResponseBoundary(agent Agent, line string) bool {
	if isSeparator(strings.TrimSpace(line)) {
		return true
	}
	for _, prefix := range specs[agent].boundaryPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func responseMarker(agent Agent, line string) (string, bool) {
	// Response glyphs are emitted at column zero. Indented lookalikes belong to
	// status bars (for example Claude's "● high · /effort") or message bodies.
	if line != strings.TrimLeft(line, " \t") {
		return "", false
	}
	for _, marker := range specs[agent].responseMarkers {
		if line == marker {
			return "", true
		}
		if strings.HasPrefix(line, marker+" ") {
			return strings.TrimSpace(strings.TrimPrefix(line, marker)), true
		}
	}
	return "", false
}

func isSeparator(line string) bool {
	if len([]rune(line)) < 8 {
		return false
	}
	for _, r := range line {
		switch r {
		case '─', '━', '═', '-', ' ':
		default:
			return false
		}
	}
	return true
}
