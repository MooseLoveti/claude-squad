package relay

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"

	xansi "github.com/charmbracelet/x/ansi"
)

// Agent identifies the two session types supported by the relay feature.
type Agent int

const (
	AgentUnknown Agent = iota
	AgentClaude
	AgentCodex
)

// DetectAgent finds the agent executable in a configured program command.
func DetectAgent(program string) Agent {
	for _, field := range strings.Fields(program) {
		name := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.Trim(field, "\"'"))), ".exe")
		switch name {
		case "claude":
			return AgentClaude
		case "codex":
			return AgentCodex
		}
	}
	return AgentUnknown
}

func (a Agent) Label() string {
	switch a {
	case AgentClaude:
		return "Claude Code"
	case AgentCodex:
		return "Codex"
	default:
		return "Unknown agent"
	}
}

func (a Agent) Peer() Agent {
	switch a {
	case AgentClaude:
		return AgentCodex
	case AgentCodex:
		return AgentClaude
	default:
		return AgentUnknown
	}
}

func (a Agent) TargetTag() string {
	switch a {
	case AgentClaude:
		return "@codex"
	case AgentCodex:
		return "@claude"
	default:
		return ""
	}
}

// FormatMessage adds the minimum source information required by the relay protocol.
func FormatMessage(source Agent, message string) string {
	return fmt.Sprintf("[Message from %s]\n\n%s", source.Label(), strings.TrimSpace(message))
}

// TaggedMessage returns the text after the last matching relay tag.
func TaggedMessage(response, tag string) string {
	if tag == "" {
		return ""
	}
	lowerResponse := strings.ToLower(response)
	idx := strings.LastIndex(lowerResponse, strings.ToLower(tag))
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(response[idx+len(tag):])
}

// LatestResponse extracts the last rendered assistant response from a Claude Code
// or Codex terminal transcript. Both CLIs prefix response blocks with a stable
// glyph; the next input prompt marks the end of that response.
func LatestResponse(agent Agent, pane string) (string, error) {
	response, _, err := LatestResponseWithID(agent, pane)
	return response, err
}

// LatestResponseWithID returns a stable identity for the response block. The
// identity ignores dynamic footer/status changes while still distinguishing two
// separate responses that happen to have identical text.
func LatestResponseWithID(agent Agent, pane string) (string, [sha256.Size]byte, error) {
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
		trimmed := strings.TrimSpace(line)
		if isResponseBoundary(agent, trimmed) {
			break
		}
		result = append(result, strings.TrimRight(line, " \t"))
	}

	response := strings.TrimSpace(strings.Join(result, "\n"))
	if response == "" {
		return "", [sha256.Size]byte{}, fmt.Errorf("latest %s response is empty", agent.Label())
	}
	id := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", occurrences, response)))
	return response, id, nil
}

func isResponseBoundary(agent Agent, line string) bool {
	if isPromptMarker(agent, line) || isSeparator(line) {
		return true
	}
	if agent == AgentClaude {
		for _, marker := range []string{"✻", "✽", "✢", "✶"} {
			if strings.HasPrefix(line, marker) {
				return true
			}
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
	markers := []string{}
	switch agent {
	case AgentClaude:
		markers = []string{"⏺", "●"}
	case AgentCodex:
		markers = []string{"•"}
	default:
		return "", false
	}
	for _, marker := range markers {
		if line == marker {
			return "", true
		}
		if strings.HasPrefix(line, marker+" ") {
			return strings.TrimSpace(strings.TrimPrefix(line, marker)), true
		}
	}
	return "", false
}

func isPromptMarker(agent Agent, line string) bool {
	switch agent {
	case AgentClaude:
		return strings.HasPrefix(line, "❯")
	case AgentCodex:
		return strings.HasPrefix(line, "›")
	default:
		return false
	}
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
