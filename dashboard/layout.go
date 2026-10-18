package dashboard

import (
	"claude-squad/relay"
	"claude-squad/workspace"
	"fmt"
)

// PanelTitle is the control panel pane's title.
const PanelTitle = "🎮 操作パネル"

// panelColor is the control panel's theme color.
const panelColor = accentColor

// AgentCommand is the shell command that runs a member of w, briefed on the
// other members. A member without a configured or installed program shows how
// to fix that instead.
func AgentCommand(w *workspace.Workspace, m workspace.Member, env relay.LaunchEnv) string {
	agent := relay.AgentByName(m.Agent)
	if m.Program == "" {
		message := fmt.Sprintf("%s (%s) が見つかりません。PATH に入れるか、~/.claude-squad/config.json の profiles に登録してください。",
			agent.Label(), agent.Executable())
		return "printf '%s\\n' " + shellQuote(message)
	}
	env.Briefing = workspace.Briefing(w, m, env.Self)
	return ShellCommand(m.Program, agent.LaunchArgs(env))
}

// Layout places every member's agent and the control panel in the 2×2 grid:
// each agent in its own slot, the panel in the first slot left over.
func Layout(w *workspace.Workspace, self, sharedDir string) []Pane {
	env := relay.LaunchEnv{SharedDir: sharedDir, Self: self}
	used := map[relay.Slot]bool{}
	var panes []Pane
	for _, m := range w.Members {
		agent := relay.AgentByName(m.Agent)
		title := agent.Emoji() + " " + agent.Label()
		if m.Branch != "" {
			title += " ⎇ " + m.Branch
		}
		panes = append(panes, Pane{Slot: agent.Slot(), Agent: m.Agent, Title: title, Color: agent.Color(), Dir: m.Dir, Command: AgentCommand(w, m, env)})
		used[agent.Slot()] = true
	}
	for slot := relay.SlotTopLeft; slot <= relay.SlotBottomRight; slot++ {
		if !used[slot] {
			panes = append(panes, Pane{Slot: slot, Title: PanelTitle, Color: panelColor, Dir: w.Dir, Command: shellQuote(self) + " panel"})
			break
		}
	}
	return panes
}
