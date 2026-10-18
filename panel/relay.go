package panel

import (
	"claude-squad/log"
	"claude-squad/relay"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Terminal is what the relay needs from the dashboard's panes.
type Terminal interface {
	Capture(pane string) (string, error)
	CaptureHistory(pane string) (string, error)
	Paste(pane, text string) error
	SendKeys(pane string, keys ...string) error
	Backspace(pane string, n int) error
	SetNote(pane, note string) error
}

// Member is an agent running in a dashboard pane.
type Member struct {
	Agent relay.Agent
	Pane  string
	Dir   string
}

// EventKind tells what a relay log entry records.
type EventKind int

const (
	EventInfo EventKind = iota
	EventWarn
	// EventAsk: Agent asked Count agents a question.
	EventAsk
	// EventAnswer: Agent answered a question.
	EventAnswer
	// EventDeliver: Count answers went back to Agent.
	EventDeliver
	// EventBroadcast: a prompt was typed into Count agents.
	EventBroadcast
)

// LogEntry is one line of the relay log; Detail holds the full text sent.
// Seq numbers entries from 1, so readers can tell which ones are new.
type LogEntry struct {
	Seq    int
	Kind   EventKind
	Agent  relay.Agent
	Count  int
	Time   time.Time
	Text   string
	Detail string
}

const (
	// autoRelayLimit stops agents from tagging each other forever.
	autoRelayLimit = 6
	// consultTimeout gives up on an answer that never comes.
	consultTimeout = 30 * time.Minute
	// pasteSettle lets a CLI take in a paste before Enter submits it.
	pasteSettle = 300 * time.Millisecond
	// logLimit bounds the relay log kept in memory.
	logLimit = 200
	// noteQuestionLimit keeps a question short enough for a pane title.
	noteQuestionLimit = 24
	// noticeRetry waits for a CLI to take a dismissal before sending another.
	noticeRetry = 5 * time.Second
)

type consultState int

const (
	// consultQueued waits for the recipient to be free.
	consultQueued consultState = iota
	// consultSent waits for the recipient's reply.
	consultSent
	consultAnswered
)

// consult is one recipient of a question.
type consult struct {
	to     Member
	prompt string
	state  consultState
	// baseline is the recipient's latest reply before the question, so the
	// same reply is never taken for the answer.
	baseline [sha256.Size]byte
	// candidate is a new reply seen once; it counts as the answer when the
	// next poll still shows it, as pane replies may still be rendering.
	candidate [sha256.Size]byte
	sentAt    time.Time
	answer    string
}

// ask is one question put to one or more agents. Its answers go back to the
// asker together once every recipient has answered.
type ask struct {
	asker    Member
	question string
	fromUser bool
	consults []*consult
	// deliveryFailed is set after a failed delivery, which is retried on
	// later polls and logged once.
	deliveryFailed bool
}

func (a *ask) answered() bool {
	for _, c := range a.consults {
		if c.state != consultAnswered {
			return false
		}
	}
	return true
}

// Relay carries @tag questions between the agents' panes: the question is
// typed into each recipient's session, and the answers are typed back into
// the asker's session once all of them are in.
type Relay struct {
	mu            sync.Mutex
	term          Terminal
	members       []Member
	transcriptDir string
	logFile       string
	now           func() time.Time
	settle        time.Duration

	asks      []*ask
	busy      map[relay.Agent]bool
	history   map[relay.Agent]string
	notes     map[relay.Agent]string
	log       []LogEntry
	seq       int
	autoRelay bool
	autoCount int
	// autoSeen is each agent's latest reply already checked for tags.
	autoSeen map[relay.Agent][sha256.Size]byte
	// autoCandidate is a reply seen once, checked when it is seen again.
	autoCandidate map[relay.Agent][sha256.Size]byte
	// dismissedAt is when each agent's startup notice was last dismissed.
	dismissedAt map[relay.Agent]time.Time
}

// NewRelay sets up a relay over members. Transcripts and the full relay log
// are written under configDir.
func NewRelay(term Terminal, members []Member, configDir string) *Relay {
	dir := filepath.Join(configDir, "relay")
	return &Relay{
		term:          term,
		members:       members,
		transcriptDir: dir,
		logFile:       filepath.Join(dir, "log.txt"),
		now:           time.Now,
		settle:        pasteSettle,
		busy:          map[relay.Agent]bool{},
		history:       map[relay.Agent]string{},
		notes:         map[relay.Agent]string{},
		autoSeen:      map[relay.Agent][sha256.Size]byte{},
		autoCandidate: map[relay.Agent][sha256.Size]byte{},
		dismissedAt:   map[relay.Agent]time.Time{},
	}
}

// LogFile is where every relay message is written in full.
func (r *Relay) LogFile() string {
	return r.logFile
}

func (r *Relay) member(pane string) (Member, bool) {
	for _, m := range r.members {
		if m.Pane == pane {
			return m, true
		}
	}
	return Member{}, false
}

// HandleEnter decides what Enter typed on a pane does. When the pane's input
// starts with a tag, the input is cleared and asked of the tagged agents, and
// HandleEnter reports true so the Enter never reaches the agent. A tagged
// input that cannot be asked stays in place with the reason in the log.
func (r *Relay) HandleEnter(pane string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	source, ok := r.member(pane)
	if !ok {
		return false
	}
	screen, err := r.term.Capture(pane)
	if err != nil {
		return false
	}
	text, ok := source.Agent.InputText(screen)
	if !ok {
		return false
	}
	to := relay.Leading(source.Agent, text)
	if !to.Tagged() {
		return false
	}
	if to.Message == "" {
		r.warn("@タグの後に質問がありません")
		return true
	}
	destinations, err := r.destinations(source, to)
	if err != nil {
		r.warn(err.Error())
		return true
	}
	// Two extra deletions cover characters InputText trims, such as a
	// trailing space.
	if err := r.term.Backspace(pane, len([]rune(text))+strings.Count(text, "\n")+2); err != nil {
		log.ErrorLog.Printf("clear input of %s: %v", source.Agent.Label(), err)
	}
	r.startAsk(source, destinations, to.Message, true)
	r.refreshNotes()
	return true
}

// destinations resolves who an addressing reaches: "@all" is every other
// agent, otherwise each tagged agent that is in the workspace.
func (r *Relay) destinations(source Member, to relay.Addressing) ([]Member, error) {
	var out []Member
	for _, m := range r.members {
		if m.Agent == source.Agent {
			continue
		}
		if to.All || containsAgent(to.Agents, m.Agent) {
			out = append(out, m)
		}
	}
	for _, agent := range to.Agents {
		if !containsMember(out, agent) {
			return nil, fmt.Errorf("%s はこのワークスペースで動いていません", agent.Tag())
		}
	}
	if len(out) == 0 {
		return nil, errors.New("@all で聞ける相手がいません")
	}
	return out, nil
}

func containsAgent(agents []relay.Agent, agent relay.Agent) bool {
	for _, a := range agents {
		if a == agent {
			return true
		}
	}
	return false
}

func containsMember(members []Member, agent relay.Agent) bool {
	for _, m := range members {
		if m.Agent == agent {
			return true
		}
	}
	return false
}

func (r *Relay) startAsk(source Member, destinations []Member, question string, fromUser bool) {
	from := r.sender(source)
	a := &ask{asker: source, question: question, fromUser: fromUser}
	labels := make([]string, len(destinations))
	var detail strings.Builder
	for i, d := range destinations {
		labels[i] = d.Agent.Label()
		var also []relay.Agent
		for _, other := range destinations {
			if other.Agent != d.Agent {
				also = append(also, other.Agent)
			}
		}
		prompt := relay.Question{From: from, FromUser: fromUser, To: d.Agent, ToDir: d.Dir, AlsoAsked: also, Text: question}.Format()
		a.consults = append(a.consults, &consult{to: d, prompt: prompt})
		fmt.Fprintf(&detail, "=== %s へ ===\n%s\n\n", d.Agent.Label(), prompt)
	}
	r.asks = append(r.asks, a)
	r.addLog(LogEntry{Kind: EventAsk, Agent: source.Agent, Count: len(destinations),
		Text: fmt.Sprintf("%s → %s: %s", source.Agent.Label(), strings.Join(labels, "・"), oneLine(question)), Detail: detail.String()})
}

// sender describes the asker for the recipients, writing its transcript
// outside every worktree so it never shows up in git.
func (r *Relay) sender(source Member) relay.Sender {
	from := relay.Sender{Agent: source.Agent, WorktreePath: source.Dir}
	if transcript, err := source.Agent.Transcript(source.Dir); err == nil {
		path := filepath.Join(r.transcriptDir, filepath.Base(source.Dir)+"-"+source.Agent.Name()+".md")
		if err := os.MkdirAll(r.transcriptDir, 0o755); err == nil && os.WriteFile(path, []byte(transcript), 0o644) == nil {
			from.TranscriptPath = path
		}
	}
	if exchange, err := source.Agent.LatestExchange(source.Dir); err == nil {
		from.Latest = &exchange
	}
	return from
}

// Broadcast types the same prompt into every agent's session.
func (r *Relay) Broadcast(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	for _, m := range r.members {
		if err := r.submit(m, text); err != nil {
			r.warn(fmt.Sprintf("%s に送れませんでした: %v", m.Agent.Label(), err))
		}
	}
	r.addLog(LogEntry{Kind: EventBroadcast, Count: len(r.members), Text: "全員に送信: " + oneLine(text), Detail: text})
}

// submit pastes text into a pane and presses Enter.
func (r *Relay) submit(m Member, text string) error {
	if err := r.term.Paste(m.Pane, text); err != nil {
		return err
	}
	time.Sleep(r.settle)
	return r.term.SendKeys(m.Pane, "Enter")
}

// Step polls every pane once and moves each question along: it sends queued
// questions to free recipients, collects finished answers, and delivers
// complete answer sets to free askers. With auto relay on, it also asks what
// a new agent reply tags.
func (r *Relay) Step() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.members {
		history, err := r.term.CaptureHistory(m.Pane)
		if err != nil {
			continue
		}
		r.history[m.Agent] = history
		r.busy[m.Agent] = m.Agent.Busy(m.Dir, history)
		r.dismissNotice(m)
	}

	for _, a := range r.asks {
		for _, c := range a.consults {
			switch c.state {
			case consultQueued:
				if r.free(c.to.Agent) {
					r.send(c)
				}
			case consultSent:
				r.collect(c)
			}
		}
	}

	var pending []*ask
	for _, a := range r.asks {
		if a.answered() && r.free(a.asker.Agent) && r.deliver(a) {
			continue
		}
		pending = append(pending, a)
	}
	r.asks = pending

	if r.autoRelay {
		r.checkTaggedReplies()
	}
	r.refreshNotes()
}

// dismissNotice closes a startup notice showing on m's pane. The keys may
// interrupt work, so a busy agent is left alone.
func (r *Relay) dismissNotice(m Member) {
	keys := m.Agent.NoticeKeys(r.history[m.Agent])
	if keys == nil || r.busy[m.Agent] || r.now().Sub(r.dismissedAt[m.Agent]) < noticeRetry {
		return
	}
	if err := r.term.SendKeys(m.Pane, keys...); err != nil {
		log.ErrorLog.Printf("dismiss notice on %s: %v", m.Agent.Label(), err)
		return
	}
	r.dismissedAt[m.Agent] = r.now()
}

// free reports whether an agent can take a message now: it is not working and
// no question sent to it is still waiting for its answer.
func (r *Relay) free(agent relay.Agent) bool {
	return !r.busy[agent] && !r.answering(agent)
}

func (r *Relay) answering(agent relay.Agent) bool {
	for _, a := range r.asks {
		for _, c := range a.consults {
			if c.to.Agent == agent && c.state == consultSent {
				return true
			}
		}
	}
	return false
}

func (r *Relay) send(c *consult) {
	if _, id, err := c.to.Agent.LatestReply(c.to.Dir, r.history[c.to.Agent]); err == nil {
		c.baseline = id
	}
	if err := r.submit(c.to, c.prompt); err != nil {
		c.state, c.answer = consultAnswered, fmt.Sprintf("（回答なし: 送信に失敗しました: %v）", err)
		r.warn(fmt.Sprintf("%s に送れませんでした: %v", c.to.Agent.Label(), err))
		return
	}
	c.state, c.sentAt = consultSent, r.now()
	// The question just typed shows the recipient as busy until the next poll.
	r.busy[c.to.Agent] = true
}

func (r *Relay) collect(c *consult) {
	if r.now().Sub(c.sentAt) > consultTimeout {
		c.state, c.answer = consultAnswered, "（回答なし: 時間内に返答がありませんでした）"
		r.warn(c.to.Agent.Label() + " から返答がありませんでした")
		return
	}
	if r.busy[c.to.Agent] {
		return
	}
	reply, id, err := c.to.Agent.LatestReply(c.to.Dir, r.history[c.to.Agent])
	if err != nil || id == c.baseline {
		return
	}
	if id != c.candidate {
		c.candidate = id
		return
	}
	c.state, c.answer = consultAnswered, reply
	// The answer is not a new message to scan for tags.
	r.autoSeen[c.to.Agent] = id
	r.addLog(LogEntry{Kind: EventAnswer, Agent: c.to.Agent, Text: c.to.Agent.Label() + " が回答しました", Detail: reply})
}

// deliver types the combined answers into the asker's session and reports
// whether that worked.
func (r *Relay) deliver(a *ask) bool {
	answers := make([]relay.Answer, len(a.consults))
	for i, c := range a.consults {
		answers[i] = relay.Answer{From: relay.Sender{Agent: c.to.Agent, WorktreePath: c.to.Dir}, Text: c.answer}
	}
	message := relay.FormatAnswers(a.question, a.fromUser, answers)
	if err := r.submit(a.asker, message); err != nil {
		if !a.deliveryFailed {
			r.addLog(LogEntry{Kind: EventWarn, Text: fmt.Sprintf("%s に回答を返せませんでした（再試行します）: %v", a.asker.Agent.Label(), err), Detail: message})
		}
		a.deliveryFailed = true
		return false
	}
	r.busy[a.asker.Agent] = true
	r.addLog(LogEntry{Kind: EventDeliver, Agent: a.asker.Agent, Count: len(a.consults), Text: a.asker.Agent.Label() + " に回答を返しました", Detail: message})
	return true
}

// checkTaggedReplies asks what each agent's new, finished reply tags. A reply
// counts once it shows on two polls in a row, and the first reply seen after
// turning auto relay on only sets the starting point.
func (r *Relay) checkTaggedReplies() {
	for _, m := range r.members {
		if r.busy[m.Agent] || r.answering(m.Agent) {
			continue
		}
		reply, id, err := m.Agent.LatestReply(m.Dir, r.history[m.Agent])
		if err != nil {
			continue
		}
		seen, known := r.autoSeen[m.Agent]
		if !known {
			r.autoSeen[m.Agent] = id
			continue
		}
		if id == seen {
			continue
		}
		if r.autoCandidate[m.Agent] != id {
			r.autoCandidate[m.Agent] = id
			continue
		}
		r.autoSeen[m.Agent] = id
		to := relay.Route(m.Agent, reply)
		if !to.Tagged() || to.Message == "" {
			continue
		}
		destinations, err := r.destinations(m, to)
		if err != nil {
			r.warn(err.Error())
			continue
		}
		r.startAsk(m, destinations, to.Message, false)
		r.autoCount++
		if r.autoCount >= autoRelayLimit {
			r.autoRelay = false
			r.addLog(LogEntry{Kind: EventInfo, Text: fmt.Sprintf("自動Relayを%d回で止めました", autoRelayLimit)})
			return
		}
	}
}

// SetAutoRelay turns auto relay on or off. Turning it on resets the count and
// forgets which replies were seen, so only replies after this point count.
func (r *Relay) SetAutoRelay(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.autoRelay = on
	if on {
		r.autoCount = 0
		r.autoSeen = map[relay.Agent][sha256.Size]byte{}
		r.autoCandidate = map[relay.Agent][sha256.Size]byte{}
	}
}

// refreshNotes shows on each pane's title what the relay is doing with it.
func (r *Relay) refreshNotes() {
	notes := map[relay.Agent][]string{}
	for _, a := range r.asks {
		notes[a.asker.Agent] = append(notes[a.asker.Agent], askNote(a))
		for _, c := range a.consults {
			if c.state == consultSent {
				notes[c.to.Agent] = append(notes[c.to.Agent], "← "+a.asker.Agent.Label()+" の相談に回答中")
			}
		}
	}
	for _, m := range r.members {
		note := strings.Join(notes[m.Agent], " | ")
		if r.notes[m.Agent] == note {
			continue
		}
		if err := r.term.SetNote(m.Pane, note); err != nil {
			log.WarningLog.Printf("set note on %s: %v", m.Agent.Label(), err)
			continue
		}
		r.notes[m.Agent] = note
	}
}

// askNote is one question's progress, e.g. "Q: どう思う？ → Codex … · Gemini ✓".
func askNote(a *ask) string {
	question := []rune(oneLine(a.question))
	if len(question) > noteQuestionLimit {
		question = append(question[:noteQuestionLimit], '…')
	}
	status := make([]string, len(a.consults))
	for i, c := range a.consults {
		mark := " …"
		if c.state == consultAnswered {
			mark = " ✓"
		}
		status[i] = c.to.Agent.Label() + mark
	}
	return fmt.Sprintf("Q: %s → %s", string(question), strings.Join(status, " · "))
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func (r *Relay) warn(text string) {
	r.addLog(LogEntry{Kind: EventWarn, Text: text})
}

func (r *Relay) addLog(entry LogEntry) {
	r.seq++
	entry.Seq, entry.Time = r.seq, r.now()
	r.log = append(r.log, entry)
	if len(r.log) > logLimit {
		r.log = r.log[len(r.log)-logLimit:]
	}
	if err := r.appendLogFile(entry); err != nil {
		log.WarningLog.Printf("write relay log: %v", err)
	}
}

func (r *Relay) appendLogFile(entry LogEntry) error {
	if err := os.MkdirAll(filepath.Dir(r.logFile), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(r.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	text := fmt.Sprintf("[%s] %s\n", entry.Time.Format("01-02 15:04:05"), entry.Text)
	if entry.Detail != "" {
		text += strings.TrimRight(entry.Detail, "\n") + "\n"
	}
	_, err = f.WriteString(text + "\n")
	return err
}

// Status is what the panel shows about the relay.
type Status struct {
	Log       []LogEntry
	Busy      map[relay.Agent]bool
	Pending   []string
	AutoRelay bool
	AutoCount int
}

// Status returns a copy of the relay's state for display.
func (r *Relay) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Status{
		Log:       append([]LogEntry(nil), r.log...),
		Busy:      map[relay.Agent]bool{},
		AutoRelay: r.autoRelay,
		AutoCount: r.autoCount,
	}
	for agent, busy := range r.busy {
		s.Busy[agent] = busy
	}
	for _, a := range r.asks {
		s.Pending = append(s.Pending, a.asker.Agent.Label()+" "+askNote(a))
	}
	return s
}
