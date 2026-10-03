package app

import (
	"claude-squad/relay"
	"claude-squad/session"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

const autoRelayLimit = 6

type relayMode int

const (
	relayManual relayMode = iota
	relayAuto
)

func (m relayMode) String() string {
	if m == relayAuto {
		return "AUTO"
	}
	return "MANUAL"
}

func (m *home) updateRelayMenu() {
	m.menu.SetRelayStatus(m.relayMode.String(), m.autoRelayCount, autoRelayLimit)
}

func (m *home) toggleRelayMode() {
	if m.relayMode == relayAuto {
		m.relayMode = relayManual
		m.autoRelayCount = 0
		m.autoRelaySeen = make(map[*session.Instance][32]byte)
		m.updateRelayMenu()
		return
	}

	m.relayMode = relayAuto
	m.autoRelayCount = 0
	m.autoRelaySeen = make(map[*session.Instance][32]byte)
	// Baseline current output so enabling AUTO never forwards a historical tag.
	for _, instance := range m.snapshotActiveInstances() {
		content, err := instance.PreviewFullHistory()
		if err == nil {
			_, id, responseErr := relay.LatestResponseWithID(relay.DetectAgent(instance.Program), content)
			if responseErr == nil {
				m.autoRelaySeen[instance] = id
			} else {
				m.autoRelaySeen[instance] = [32]byte{}
			}
		}
	}
	m.updateRelayMenu()
}

func (m *home) relayPeer(source *session.Instance) (*session.Instance, relay.Agent, error) {
	sourceAgent := relay.DetectAgent(source.Program)
	if sourceAgent == relay.AgentUnknown {
		return nil, sourceAgent, fmt.Errorf("session %q is not Claude Code or Codex", source.Title)
	}

	peerAgent := sourceAgent.Peer()
	var peers []*session.Instance
	for _, candidate := range m.list.GetInstances() {
		if candidate == source || !candidate.Started() || candidate.Paused() {
			continue
		}
		if relay.DetectAgent(candidate.Program) == peerAgent {
			peers = append(peers, candidate)
		}
	}
	if len(peers) != 1 {
		return nil, sourceAgent, fmt.Errorf("relay requires exactly one running %s session; found %d", peerAgent.Label(), len(peers))
	}
	return peers[0], sourceAgent, nil
}

func (m *home) sendRelay(source, destination *session.Instance, sourceAgent relay.Agent, message string) error {
	if message == "" {
		return fmt.Errorf("cannot relay an empty message")
	}
	if err := destination.SendPrompt(relay.FormatMessage(sourceAgent, message)); err != nil {
		return fmt.Errorf("relay from %q to %q failed: %w", source.Title, destination.Title, err)
	}
	return nil
}

func (m *home) manualRelay(source *session.Instance) error {
	if source == nil || !source.Started() || source.Paused() {
		return fmt.Errorf("select a running Claude Code or Codex session to relay")
	}
	destination, sourceAgent, err := m.relayPeer(source)
	if err != nil {
		return err
	}
	content, err := source.PreviewFullHistory()
	if err != nil {
		return fmt.Errorf("capture latest response from %q: %w", source.Title, err)
	}
	response, err := relay.LatestResponse(sourceAgent, content)
	if err != nil {
		return err
	}
	return m.sendRelay(source, destination, sourceAgent, response)
}

// processAutoRelay inspects only stable pane snapshots produced by the existing
// metadata tick. A response-block identity prevents replaying the same tag when
// a CLI changes only its dynamic footer.
func (m *home) processAutoRelay(results []instanceMetaResult) []tea.Cmd {
	var cmds []tea.Cmd
	if m.relayMode != relayAuto {
		return cmds
	}

	for _, result := range results {
		if result.relayOutput == "" || result.relayErr != nil {
			continue
		}
		sourceAgent := relay.DetectAgent(result.instance.Program)
		if sourceAgent == relay.AgentUnknown {
			continue
		}
		response, id, err := relay.LatestResponseWithID(sourceAgent, result.relayOutput)
		if err != nil {
			continue
		}
		previous, known := m.autoRelaySeen[result.instance]
		m.autoRelaySeen[result.instance] = id
		if !known || previous == id {
			continue
		}
		message := relay.TaggedMessage(response, sourceAgent.TargetTag())
		if message == "" {
			continue
		}

		destination, _, err := m.relayPeer(result.instance)
		if err == nil {
			err = m.sendRelay(result.instance, destination, sourceAgent, message)
		}
		if err != nil {
			cmds = append(cmds, m.handleError(err))
			continue
		}

		if m.recordAutoRelay() {
			cmds = append(cmds, m.handleError(fmt.Errorf("AUTO Relay stopped after %d messages", autoRelayLimit)))
			break
		}
	}
	return cmds
}

func (m *home) recordAutoRelay() bool {
	m.autoRelayCount++
	if m.autoRelayCount >= autoRelayLimit {
		m.relayMode = relayManual
	}
	m.updateRelayMenu()
	return m.relayMode == relayManual
}
