package app

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/relay"
	"claude-squad/session"
	"claude-squad/ui/overlay"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// relayGather collects the answers to one question asked of one or more agents,
// so the asker receives them together.
type relayGather struct {
	asker     *session.Instance
	question  string
	fromUser  bool
	waitingOn []*session.Instance
	answers   map[*session.Instance]relay.Answer
}

func newRelayGather(asker *session.Instance, question string, fromUser bool, waitingOn []*session.Instance) *relayGather {
	return &relayGather{
		asker:     asker,
		question:  question,
		fromUser:  fromUser,
		waitingOn: waitingOn,
		answers:   map[*session.Instance]relay.Answer{},
	}
}

func (g *relayGather) complete() bool {
	return len(g.waitingOn) > 0 && len(g.answers) == len(g.waitingOn)
}

func (g *relayGather) orderedAnswers() []relay.Answer {
	answers := make([]relay.Answer, 0, len(g.answers))
	for _, recipient := range g.waitingOn {
		if answer, ok := g.answers[recipient]; ok {
			answers = append(answers, answer)
		}
	}
	return answers
}

// consultAnswer is one finished consultation, handed from its goroutine to
// whoever owns the relay state (the UI loop, or the attach watcher).
type consultAnswer struct {
	gather    *relayGather
	recipient *session.Instance
	answer    relay.Answer
}

// consultTimeout bounds a consultation that never finishes; slow answers are
// otherwise waited for.
const consultTimeout = 30 * time.Minute

func (m *home) updateRelayMenu() {
	m.menu.SetRelayStatus(m.relayMode.String(), m.autoRelayCount, autoRelayLimit, m.relayNote())
	m.refreshAskNotes()
}

// askNoteQuestionLimit keeps the question short enough for a status bar.
const askNoteQuestionLimit = 30

// refreshAskNotes shows each asking session, in its own tmux status bar, the
// question it sent and who has answered, so a question typed in place and
// cleared from the input box stays visible until the answers arrive.
func (m *home) refreshAskNotes() {
	notes := map[*session.Instance][]string{}
	for _, gather := range m.pendingAsks {
		notes[gather.asker] = append(notes[gather.asker], askNote(gather))
	}

	if m.askNotes == nil {
		m.askNotes = map[*session.Instance]string{}
	}
	for asker, parts := range notes {
		m.setAskNote(asker, strings.Join(parts, " | "))
	}
	for asker := range m.askNotes {
		if _, still := notes[asker]; !still {
			m.setAskNote(asker, "")
		}
	}
}

// askNote is one question's line in the status bar, e.g.
// "Q: what do you think? → Codex ✓ · Gemini …".
func askNote(gather *relayGather) string {
	question := []rune(strings.Join(strings.Fields(gather.question), " "))
	if len(question) > askNoteQuestionLimit {
		question = append(question[:askNoteQuestionLimit], '…')
	}
	var status []string
	for _, recipient := range gather.waitingOn {
		mark := " …"
		if _, answered := gather.answers[recipient]; answered {
			mark = " ✓"
		}
		status = append(status, relay.DetectAgent(recipient.Program).Label()+mark)
	}
	return fmt.Sprintf("Q: %s → %s", string(question), strings.Join(status, " · "))
}

func (m *home) setAskNote(asker *session.Instance, note string) {
	if m.askNotes[asker] == note {
		return
	}
	if err := asker.SetStatusNote(note); err != nil {
		log.WarningLog.Printf("could not show the ask status for %q: %v", asker.Title, err)
		return
	}
	if note == "" {
		delete(m.askNotes, asker)
		return
	}
	m.askNotes[asker] = note
}

// relayNote shows who still owes an answer, or how to reread the last answers.
func (m *home) relayNote() string {
	var waits []string
	for _, gather := range m.pendingAsks {
		var parts []string
		for _, recipient := range gather.waitingOn {
			mark := " …"
			if _, done := gather.answers[recipient]; done {
				mark = " ✓"
			}
			parts = append(parts, relay.DetectAgent(recipient.Program).Label()+mark)
		}
		waits = append(waits, strings.Join(parts, " · "))
	}
	if len(waits) > 0 {
		return "Waiting for answers: " + strings.Join(waits, " | ")
	}
	if m.lastAnswers != nil {
		return "v: last answers"
	}
	return ""
}

// shownAnswers is a combined answer set as the user reads it.
type shownAnswers struct {
	title string
	text  string
}

func answersForUser(gather *relayGather) *shownAnswers {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n", strings.TrimSpace(gather.question))
	for _, answer := range gather.orderedAnswers() {
		fmt.Fprintf(&b, "\n■ %s\n%s\n", answer.From.Agent.Label(), strings.TrimSpace(answer.Text))
	}
	title := fmt.Sprintf("Answers to your question from %s", gather.asker.Title)
	if !gather.fromUser {
		title = fmt.Sprintf("Answers to %s's question (also sent to it)", gather.asker.Title)
	}
	return &shownAnswers{
		title: title,
		text:  strings.TrimRight(b.String(), "\n"),
	}
}

func answersOverlaySize(width, height int) (int, int) {
	return int(float32(width) * 0.8), int(float32(height) * 0.8)
}

func (m *home) openAnswers() {
	m.answersOverlay = overlay.NewScrollOverlay(m.lastAnswers.title, m.lastAnswers.text)
	m.answersOverlay.SetSize(answersOverlaySize(m.windowWidth, m.windowHeight))
	m.state = stateAnswers
	m.answersUnseen = false
}

// openAnswersIfIdle shows newly arrived answers, but never on top of a prompt,
// confirmation or help screen the user is in the middle of.
func (m *home) openAnswersIfIdle() {
	if m.answersUnseen && m.lastAnswers != nil && m.state == stateDefault {
		m.openAnswers()
	}
}

func (m *home) toggleRelayMode() {
	m.autoRelayCount = 0
	m.autoRelaySeen = make(map[*session.Instance][32]byte)
	if m.relayMode == relayAuto {
		m.relayMode = relayManual
		m.updateRelayMenu()
		return
	}

	m.relayMode = relayAuto
	// Baseline current output so enabling AUTO never forwards a historical tag.
	for _, instance := range m.snapshotActiveInstances() {
		content, err := instance.PreviewFullHistory()
		if err == nil {
			agent := relay.DetectAgent(instance.Program)
			_, id, replyErr := agent.LatestReply(instance.GetWorktreePath(), content)
			if replyErr == nil {
				m.autoRelaySeen[instance] = id
			} else {
				m.autoRelaySeen[instance] = [32]byte{}
			}
		}
	}
	m.updateRelayMenu()
}

// runningRelayAgents returns every started, unpaused relay-capable session
// other than source.
func (m *home) runningRelayAgents(source *session.Instance) []*session.Instance {
	var running []*session.Instance
	for _, candidate := range m.list.GetInstances() {
		if candidate == source || !candidate.Started() || candidate.Paused() {
			continue
		}
		if relay.DetectAgent(candidate.Program) != relay.AgentUnknown {
			running = append(running, candidate)
		}
	}
	return running
}

// pickDestinations resolves which sessions an addressing reaches. "@all" means
// every other running agent; each specifically tagged agent must have exactly
// one running session; untagged text is only unambiguous when a single other
// agent is running.
func pickDestinations(running []*session.Instance, to relay.Addressing) ([]*session.Instance, error) {
	if to.All {
		if len(running) == 0 {
			return nil, fmt.Errorf("@all found no other running agents")
		}
		return running, nil
	}
	if len(to.Agents) == 0 {
		if len(running) != 1 {
			return nil, fmt.Errorf("%d other agents are running; address them with tags such as @claude, @codex, @gemini or @all", len(running))
		}
		return running, nil
	}
	var destinations []*session.Instance
	for _, target := range to.Agents {
		var matches []*session.Instance
		for _, candidate := range running {
			if relay.DetectAgent(candidate.Program) == target {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("relay to %s requires exactly one running %s session; found %d", target.Tag(), target.Label(), len(matches))
		}
		destinations = append(destinations, matches[0])
	}
	return destinations, nil
}

// sender writes the source's transcript and describes where its work is.
func sender(source *session.Instance, agent relay.Agent) relay.Sender {
	worktreePath := source.GetWorktreePath()
	transcriptPath, err := writeTranscript(agent, worktreePath)
	if err != nil {
		// The message alone is still useful; the peer can read the worktree directly.
		log.WarningLog.Printf("relay from %q sent without transcript: %v", source.Title, err)
	}
	from := relay.Sender{Agent: agent, WorktreePath: worktreePath, TranscriptPath: transcriptPath}
	if exchange, err := agent.LatestExchange(worktreePath); err == nil {
		from.Latest = &exchange
	}
	return from
}

// writeTranscript stores the sender's full conversation outside every worktree,
// so it never shows up in git, and overwrites it on each relay to stay current.
func writeTranscript(agent relay.Agent, worktreePath string) (string, error) {
	transcript, err := agent.Transcript(worktreePath)
	if err != nil {
		return "", err
	}
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(configDir, "relay")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create relay directory: %w", err)
	}
	path := filepath.Join(dir, filepath.Base(worktreePath)+".md")
	if err := os.WriteFile(path, []byte(transcript), 0o644); err != nil {
		return "", fmt.Errorf("write transcript: %w", err)
	}
	return path, nil
}

// deliverReply asks the agents a finished reply tags (or, when untagged is
// allowed, the only other agent) what it says. It reports whether it asked.
func (m *home) deliverReply(source *session.Instance, sourceAgent relay.Agent, reply string, untaggedOK bool) (bool, error) {
	if reply == "" {
		return false, fmt.Errorf("cannot relay an empty message")
	}

	to := relay.Route(sourceAgent, reply)
	if !to.Tagged() && !untaggedOK {
		return false, nil
	}
	message := to.Message
	if untaggedOK {
		// A manual relay forwards the whole reply, not just the tagged tail.
		message = reply
	}
	if message == "" {
		return false, fmt.Errorf("cannot relay an empty message")
	}
	destinations, err := pickDestinations(m.runningRelayAgents(source), to)
	if err != nil {
		return false, err
	}
	return true, m.ask(source, sourceAgent, destinations, message, false)
}

// submitPrompt sends what the user typed in the prompt box to the selected
// session. Tagged text ("@codex ...", "@all ...") is instead asked of those
// agents, together with this session's worktree and transcript, and their
// answers come back to this session.
func (m *home) submitPrompt(selected *session.Instance, text string) error {
	defer m.updateRelayMenu()
	agent := relay.DetectAgent(selected.Program)
	to := relay.Leading(agent, text)
	if !to.Tagged() {
		to = relay.Route(agent, text)
	}
	if agent == relay.AgentUnknown || !to.Tagged() {
		return selected.SendPrompt(text)
	}
	if to.Message == "" {
		return fmt.Errorf("write a question after the tag")
	}
	destinations, err := pickDestinations(m.runningRelayAgents(selected), to)
	if err != nil {
		return err
	}
	return m.ask(selected, agent, destinations, to.Message, true)
}

// ask consults every destination about one question, each in a fresh
// one-shot run of its CLI, and opens a gather so the answers come back to the
// asker together.
func (m *home) ask(source *session.Instance, sourceAgent relay.Agent, destinations []*session.Instance, question string, fromUser bool) error {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return err
	}
	scratchDir := filepath.Join(configDir, "relay", "consult")
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		return fmt.Errorf("create consult directory: %w", err)
	}
	if m.consultAnswers == nil {
		m.consultAnswers = make(chan consultAnswer, 32)
	}

	from := sender(source, sourceAgent)
	gather := newRelayGather(source, question, fromUser, destinations)
	m.pendingAsks = append(m.pendingAsks, gather)
	for _, destination := range destinations {
		agent := relay.DetectAgent(destination.Program)
		var alsoAsked []relay.Agent
		for _, other := range destinations {
			if other != destination {
				alsoAsked = append(alsoAsked, relay.DetectAgent(other.Program))
			}
		}
		prompt := relay.Question{
			From: from, FromUser: fromUser, To: agent,
			ToWorktree: destination.GetWorktreePath(), AlsoAsked: alsoAsked, Text: question,
		}.Format()
		target := relay.ConsultTarget{
			Program: destination.Program, Worktree: destination.GetWorktreePath(),
			SharedDir: configDir, ScratchDir: scratchDir,
		}
		go m.consult(gather, destination, agent, target, prompt)
	}
	return nil
}

// consult runs one consultation off the UI loop and hands back its answer; a
// failure becomes the answer text, so the gather still completes.
func (m *home) consult(gather *relayGather, recipient *session.Instance, agent relay.Agent, target relay.ConsultTarget, prompt string) {
	ctx, cancel := context.WithTimeout(m.ctx, consultTimeout)
	defer cancel()
	text, err := agent.Consult(ctx, target, prompt)
	if err != nil {
		log.WarningLog.Printf("consulting %q failed: %v", recipient.Title, err)
		text = fmt.Sprintf("(no answer: %v)", err)
	}
	m.consultAnswers <- consultAnswer{
		gather:    gather,
		recipient: recipient,
		answer:    relay.Answer{From: relay.Sender{Agent: agent, WorktreePath: target.Worktree}, Text: text},
	}
}

// collectAnswers records finished consultations and returns each question's
// answers once all of them are in.
func (m *home) collectAnswers() []tea.Cmd {
	var cmds []tea.Cmd
	for {
		select {
		case done := <-m.consultAnswers:
			if err := m.recordAnswer(done); err != nil {
				cmds = append(cmds, m.handleError(err))
			}
		default:
			if len(cmds) > 0 || len(m.pendingAsks) > 0 {
				m.updateRelayMenu()
			}
			return cmds
		}
	}
}

func (m *home) recordAnswer(done consultAnswer) error {
	gather := done.gather
	gather.answers[done.recipient] = done.answer
	if !gather.complete() {
		m.updateRelayMenu()
		return nil
	}
	for i, pending := range m.pendingAsks {
		if pending == gather {
			m.pendingAsks = append(m.pendingAsks[:i:i], m.pendingAsks[i+1:]...)
			break
		}
	}
	defer m.updateRelayMenu()
	return m.sendAnswers(gather)
}

// sendAnswers delivers a question's answers. Answers to the user's own
// question are shown to the user, in a popup over the session they are
// attached to or on the main screen, and never typed into the asking agent,
// so development there carries on undisturbed. Answers to a question an agent
// asked go back to that agent, which needs them to continue.
func (m *home) sendAnswers(gather *relayGather) error {
	m.lastAnswers = answersForUser(gather)
	if !gather.fromUser {
		m.answersUnseen = false
		if err := gather.asker.SendPrompt(relay.FormatAnswers(gather.question, gather.fromUser, gather.orderedAnswers())); err != nil {
			return fmt.Errorf("return answers to %q failed: %w", gather.asker.Title, err)
		}
		return nil
	}
	if m.attached == nil {
		m.answersUnseen = true
		return nil
	}
	path, err := writeAnswersFile(m.lastAnswers)
	if err == nil {
		err = m.attached.ShowPopup(path)
	}
	if err != nil {
		// The main screen shows them after detaching instead.
		m.answersUnseen = true
		return fmt.Errorf("show answers over %q: %w", m.attached.Title, err)
	}
	m.answersUnseen = false
	return nil
}

// writeAnswersFile stores answers for the popup, which reads them from disk.
func writeAnswersFile(answers *shownAnswers) (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(configDir, "relay")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create relay directory: %w", err)
	}
	path := filepath.Join(dir, "answers.txt")
	text := answers.title + "   (q: close · ↑/↓ PgUp/PgDn: scroll)\n\n" + answers.text + "\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("write answers: %w", err)
	}
	return path, nil
}

func (m *home) manualRelay(source *session.Instance) error {
	if source == nil || !source.Started() || source.Paused() {
		return fmt.Errorf("select a running agent session to relay")
	}
	sourceAgent := relay.DetectAgent(source.Program)
	if sourceAgent == relay.AgentUnknown {
		return fmt.Errorf("session %q is not a relay-capable agent", source.Title)
	}
	content, err := source.PreviewFullHistory()
	if err != nil {
		return fmt.Errorf("capture latest response from %q: %w", source.Title, err)
	}
	reply, _, err := sourceAgent.LatestReply(source.GetWorktreePath(), content)
	if err != nil {
		return err
	}
	_, err = m.deliverReply(source, sourceAgent, reply, true)
	m.updateRelayMenu()
	return err
}

// relayWhileAttached keeps relays running while the user is attached to a
// session, because attaching blocks the UI loop that normally drives them. It
// also lets the user ask other agents in place: Enter on an input that starts
// with a tag ("@codex ...", "@all ...") clears the input instead of sending it
// to this agent, and asks the tagged agents; their answers come back into this
// session. The returned stop waits for the background work to end; until then
// that work owns the relay state, since the UI loop is blocked.
func (m *home) relayWhileAttached(attached *session.Instance) (stop func()) {
	m.relayEpoch++
	m.attached = attached
	if m.consultAnswers == nil {
		m.consultAnswers = make(chan consultAnswer, 32)
	}
	asks := make(chan string, 8)
	if agent := relay.DetectAgent(attached.Program); agent != relay.AgentUnknown {
		attached.SetEnterHook(func() bool {
			pane, err := attached.Preview()
			if err != nil {
				return false
			}
			text, ok := agent.InputText(pane)
			if !ok || !relay.Leading(agent, text).Tagged() {
				return false
			}
			// One backspace per character, plus a margin for line breaks the
			// pane does not show; extra backspaces on an empty input do nothing.
			if err := attached.ClearInput(len([]rune(text)) + strings.Count(text, "\n") + 2); err != nil {
				log.WarningLog.Printf("could not clear the input of %q: %v", attached.Title, err)
			}
			asks <- text
			return true
		})
	}

	done := make(chan struct{})
	finished := make(chan struct{})
	ask := func(text string) {
		if err := m.submitPrompt(attached, text); err != nil {
			m.handleError(fmt.Errorf("ask from %q: %w", attached.Title, err))
		}
	}
	go func() {
		defer close(finished)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		previous := map[*session.Instance]string{}
		for {
			select {
			case <-done:
				return
			case text := <-asks:
				ask(text)
			case done := <-m.consultAnswers:
				if err := m.recordAnswer(done); err != nil {
					m.handleError(err)
				}
			case <-ticker.C:
				if m.relayWatching() {
					m.processAutoRelay(m.stableRelaySnapshots(previous))
				}
			}
		}
	}()

	return func() {
		attached.SetEnterHook(nil)
		close(done)
		<-finished
		for {
			select {
			case text := <-asks:
				ask(text)
			default:
				m.attached = nil
				m.relayEpoch++
				return
			}
		}
	}
}

// stableRelaySnapshots captures every running session and keeps those whose
// pane did not change since the previous call (or whose replies come from a
// log), mirroring what the metadata tick hands to processAutoRelay.
func (m *home) stableRelaySnapshots(previous map[*session.Instance]string) []instanceMetaResult {
	var results []instanceMetaResult
	for _, instance := range m.snapshotActiveInstances() {
		content, err := instance.PreviewFullHistory()
		if err != nil {
			continue
		}
		stable := previous[instance] == content
		previous[instance] = content
		if stable || relay.DetectAgent(instance.Program).RepliesFromLog() {
			results = append(results, instanceMetaResult{instance: instance, relayOutput: content})
		}
	}
	return results
}

// relayWatching reports whether pane snapshots must be checked for tagged
// replies, which happens in AUTO mode only.
func (m *home) relayWatching() bool {
	return m.relayMode == relayAuto
}

// processAutoRelay inspects only stable pane snapshots produced by the existing
// metadata tick. A response-block identity prevents replaying the same tag when
// a CLI changes only its dynamic footer.
func (m *home) processAutoRelay(results []instanceMetaResult) []tea.Cmd {
	var cmds []tea.Cmd
	if !m.relayWatching() {
		return cmds
	}
	defer m.updateRelayMenu()

	for _, result := range results {
		if result.relayOutput == "" || result.relayErr != nil {
			continue
		}
		sourceAgent := relay.DetectAgent(result.instance.Program)
		if sourceAgent == relay.AgentUnknown {
			continue
		}
		reply, id, err := sourceAgent.LatestReply(result.instance.GetWorktreePath(), result.relayOutput)
		if err != nil {
			continue
		}
		previous, known := m.autoRelaySeen[result.instance]
		m.autoRelaySeen[result.instance] = id
		if !known || previous == id {
			continue
		}

		sent, err := m.deliverReply(result.instance, sourceAgent, reply, false)
		if err != nil {
			cmds = append(cmds, m.handleError(err))
			continue
		}
		if sent && m.recordAutoRelay() {
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
