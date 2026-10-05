package relay

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
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
)

// agentSpec holds everything that differs between relay agents. Supporting a
// new CLI means adding one Agent constant and one entry in specs.
type agentSpec struct {
	// executable is matched against the configured program.
	executable string
	label      string
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
	// launchArgs grant access to sharedDir, which holds every worktree and
	// relay transcript, so reading a peer never stops on a permission prompt.
	launchArgs func(sharedDir string) []string
	findLog    func(cwd string) (string, error)
	parseLog   func(io.Reader) ([]turn, error)
	// consult prepares a fresh one-shot run that answers a question.
	consult func(ctx context.Context, target ConsultTarget, prompt string) consultRun
}

// relayAgents fixes the iteration order so routing and detection are deterministic.
var relayAgents = []Agent{AgentClaude, AgentCodex, AgentAntigravity}

var specs = map[Agent]agentSpec{
	AgentClaude: {
		executable:       "claude",
		label:            "Claude Code",
		tags:             []string{"@claude"},
		responseMarkers:  []string{"⏺", "●"},
		boundaryPrefixes: []string{"❯", "✻", "✽", "✢", "✶"},
		inputPrefix:      "❯",
		busyMarkers:      []string{"esc to interrupt"},
		launchArgs: func(sharedDir string) []string {
			return []string{"--add-dir", sharedDir}
		},
		findLog:  findClaudeLog,
		parseLog: parseClaudeLog,
		consult:  claudeConsult,
	},
	AgentCodex: {
		executable:       "codex",
		label:            "Codex",
		tags:             []string{"@codex"},
		responseMarkers:  []string{"•"},
		boundaryPrefixes: []string{"›"},
		inputPrefix:      "›",
		// Codex marks its "Working" status line with the same bullet as replies.
		busyMarkers: []string{"esc to interrupt"},
		replyFooter: regexp.MustCompile(`^\s*Worked for \d`),
		// Codex's read-only sandbox can already read the whole filesystem.
		launchArgs: func(string) []string { return nil },
		findLog:    findCodexLog,
		parseLog:   parseCodexLog,
		consult:    codexConsult,
	},
	AgentAntigravity: {
		executable:  "agy",
		label:       "Gemini",
		tags:        []string{"@gemini", "@agy"},
		inputPrefix: ">",
		// The pane interleaves thinking summaries with the reply at the same
		// indentation, so the reply comes from the session log instead.
		latestReply: agyLatestReply,
		// --add-dir makes agy run commands in the added directory instead of
		// its own worktree; reads outside the workspace ask once instead.
		launchArgs: func(string) []string { return nil },
		findLog:    findAgyLog,
		parseLog:   parseAgyLog,
		consult:    agyConsult,
	},
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

// LaunchArgs returns extra CLI arguments for starting the agent; see agentSpec.
func (a Agent) LaunchArgs(sharedDir string) []string {
	if spec, ok := specs[a]; ok {
		return spec.launchArgs(sharedDir)
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

func quote(text string) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= quoteLimit {
		return string(runes)
	}
	return string(runes[:quoteLimit]) + " …(truncated; see the transcript)"
}

func (s Sender) writeContext(b *strings.Builder) {
	fmt.Fprintf(b, "%s worktree (read only for you): %s\n", s.Agent.Label(), s.WorktreePath)
	if s.TranscriptPath != "" {
		fmt.Fprintf(b, "%s full session transcript: %s\n", s.Agent.Label(), s.TranscriptPath)
	}
}

// Answer is one agent's reply to a question asked of several agents.
type Answer struct {
	From Sender
	Text string
}

// FormatAnswers combines the answers to a question into one message for the
// asking session, labelled by who said what. The question is repeated because
// the asking agent may never have seen it: the user can type it in place.
func FormatAnswers(question string, fromUser bool, answers []Answer) string {
	agents := make([]Agent, len(answers))
	for i, a := range answers {
		agents[i] = a.From.Agent
	}
	asker := "you"
	if fromUser {
		asker = "the user"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Answers from %s]\n", joinLabels(agents))
	fmt.Fprintf(&b, "Question (asked by %s): %s\n", asker, strings.TrimSpace(question))
	for _, a := range answers {
		fmt.Fprintf(&b, "\n=== %s's answer ===\n", a.From.Agent.Label())
		a.From.writeContext(&b)
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(a.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

func joinLabels(agents []Agent) string {
	labels := make([]string, len(agents))
	for i, a := range agents {
		labels[i] = a.Label()
	}
	return strings.Join(labels, ", ")
}

// RepliesFromLog reports whether the agent's replies are read from its session
// log, which marks a reply finished on its own, rather than from the pane.
func (a Agent) RepliesFromLog() bool {
	return specs[a].latestReply != nil
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
