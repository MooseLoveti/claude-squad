package relay

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Peer is another agent in the same workspace.
type Peer struct {
	Agent  Agent
	Dir    string
	Branch string
}

// Briefing tells an agent, from the moment it starts, which other agents work
// alongside it, where their work is and how to read their conversations.
// Without it an agent asked about a peer's work answers that it has no way to
// see it. It carries no @tags, so quoting it never routes a message.
func Briefing(self Agent, selfDir string, peers []Peer, exe string) string {
	var b strings.Builder
	labels := make([]string, len(peers))
	for i, p := range peers {
		labels[i] = p.Agent.Label()
	}
	fmt.Fprintf(&b, "[claude-squad] あなたは claude-squad で動いている %s です。同じプロジェクトで %s も並行して作業しています。\n",
		self.Label(), strings.Join(labels, "・"))

	var shared, separate []Peer
	for _, p := range peers {
		if p.Dir == selfDir {
			shared = append(shared, p)
		} else {
			separate = append(separate, p)
		}
	}
	if len(shared) > 0 {
		sharedLabels := make([]string, len(shared))
		for i, p := range shared {
			sharedLabels[i] = p.Agent.Label()
		}
		fmt.Fprintf(&b, "あなたの作業ディレクトリ: %s（%s と共有。同じファイルを同時に編集しないよう注意）\n", selfDir, strings.Join(sharedLabels, "・"))
	} else {
		fmt.Fprintf(&b, "あなたの作業ディレクトリ: %s\n", selfDir)
	}
	if len(separate) > 0 {
		b.WriteString("ほかのAIの作業ディレクトリ（読み取りのみ。編集しないこと）:\n")
		for _, p := range separate {
			fmt.Fprintf(&b, "- %s: %s", p.Agent.Label(), p.Dir)
			if p.Branch != "" {
				fmt.Fprintf(&b, "（ブランチ %s）", p.Branch)
			}
			b.WriteString("\n")
		}
	}
	if exe != "" {
		b.WriteString("ほかのAIの会話記録は、次のコマンドを実行すると最新のものが読めます:\n")
		for _, p := range peers {
			fmt.Fprintf(&b, "- %s: %s transcript %s\n", p.Agent.Label(), shellQuote(exe), p.Agent.Name())
		}
	}
	b.WriteString("ユーザーにほかのAIの作業・変更・会話について聞かれたら、上のディレクトリを読むかコマンドを実行して確かめてから答えてください。「ほかのAIの記録は参照できない」とは答えないでください。")
	return b.String()
}

// codexLaunchArgs pass the briefing as Codex's developer instructions. The
// value of -c is read as TOML, and a JSON string is a valid TOML string.
func codexLaunchArgs(env LaunchEnv) []string {
	if env.Briefing == "" {
		return nil
	}
	value, _ := json.Marshal(env.Briefing)
	return []string{"-c", "developer_instructions=" + string(value)}
}
