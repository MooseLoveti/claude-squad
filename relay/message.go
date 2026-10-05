package relay

import (
	"fmt"
	"strings"
)

// Question is one question put to one agent, typed into its live session.
type Question struct {
	From Sender
	// FromUser marks a question the user typed in the asking session, rather
	// than one that session's agent wrote.
	FromUser bool
	To       Agent
	// ToDir is the recipient's own working directory.
	ToDir string
	// AlsoAsked lists the other recipients, whose answers are combined with this one.
	AlsoAsked []Agent
	Text      string
}

// Format renders the question as the message typed into the recipient's session.
func (q Question) Format() string {
	var b strings.Builder
	label := q.From.Agent.Label()
	if q.FromUser {
		fmt.Fprintf(&b, "[%sからの相談（ユーザーの質問）]\n", label)
	} else {
		fmt.Fprintf(&b, "[%sからの相談]\n", label)
	}
	q.From.writeContext(&b, q.ToDir)
	if q.From.Latest != nil {
		fmt.Fprintf(&b, "--- %s の直近のやり取り ---\n", label)
		if q.From.Latest.User != "" {
			fmt.Fprintf(&b, "ユーザー: %s\n", quote(q.From.Latest.User))
		}
		fmt.Fprintf(&b, "%s: %s\n---\n", label, quote(q.From.Latest.Assistant))
	}
	if len(q.AlsoAsked) > 0 {
		fmt.Fprintf(&b, "%s にも同じ質問をしています。\n", joinLabels(q.AlsoAsked))
	}
	fmt.Fprintf(&b, "回答は %s にそのまま渡されます。\n", label)
	fmt.Fprintf(&b, "\n%s", strings.TrimSpace(q.Text))
	return b.String()
}

// Answer is one agent's reply to a question asked of one or more agents.
type Answer struct {
	From Sender
	Text string
}

// FormatAnswers combines the answers to a question into one message for the
// asking session, labelled by who said what. The question is repeated because
// the asking agent never saw it when the user typed it with a tag.
func FormatAnswers(question string, fromUser bool, answers []Answer) string {
	var b strings.Builder
	if fromUser {
		tags := make([]string, len(answers))
		for i, a := range answers {
			tags[i] = a.From.Agent.Tag()
		}
		fmt.Fprintf(&b, "[ユーザーの質問（%s 宛て）]\n", strings.Join(tags, " "))
	} else {
		b.WriteString("[あなたの質問]\n")
	}
	fmt.Fprintf(&b, "%s\n", strings.TrimSpace(question))
	for _, a := range answers {
		fmt.Fprintf(&b, "\n[%sの意見]\n", a.From.Agent.Label())
		if a.From.WorktreePath != "" {
			fmt.Fprintf(&b, "（%s の作業ディレクトリ: %s）\n", a.From.Agent.Label(), a.From.WorktreePath)
		}
		fmt.Fprintf(&b, "%s\n", strings.TrimSpace(a.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

func joinLabels(agents []Agent) string {
	labels := make([]string, len(agents))
	for i, a := range agents {
		labels[i] = a.Label()
	}
	return strings.Join(labels, "・")
}
