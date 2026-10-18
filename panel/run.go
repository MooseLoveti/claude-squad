package panel

import (
	"claude-squad/cmd"
	"claude-squad/config"
	"claude-squad/dashboard"
	"claude-squad/log"
	"claude-squad/relay"
	"claude-squad/workspace"
	"errors"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// stepInterval is how often the relay polls the agents' panes.
const stepInterval = time.Second

// Run runs the control panel of the open workspace's dashboard, and the relay
// with it, until the dashboard closes.
func Run() error {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return err
	}
	state := config.LoadState()
	ws, err := workspace.Load(state)
	if err != nil {
		return err
	}
	if ws == nil {
		return errors.New("開いているワークスペースがありません。claude-squad を起動して作業場所を選んでください")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}

	tmux := dashboard.New(cmd.MakeExecutor())
	panes, err := tmux.Panes()
	if err != nil {
		return err
	}
	var members []Member
	for _, p := range panes {
		agent := relay.AgentByName(p.Agent)
		wm, ok := ws.Member(p.Agent)
		if agent == relay.AgentUnknown || !ok || wm.Program == "" {
			continue
		}
		members = append(members, Member{Agent: agent, Pane: p.ID, Dir: wm.Dir})
	}

	// The dashboard may have been started by another version.
	if err := tmux.Restyle(dashboard.Layout(ws, self, configDir), self); err != nil {
		log.WarningLog.Printf("restyle dashboard: %v", err)
	}

	r := NewRelay(tmux, members, configDir)
	stopHook, err := ServeHook(SocketPath(configDir), r.HandleEnter)
	if err != nil {
		return err
	}
	defer stopHook()

	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(stepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				r.Step()
			}
		}
	}()

	env := relay.LaunchEnv{SharedDir: configDir, Self: self}
	toast := func(text string) {
		if err := tmux.Toast(text); err != nil {
			log.WarningLog.Printf("toast: %v", err)
		}
	}
	model := newModel(ws, state, tmux, r, members, env, configDir, toast)
	_, err = tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	if err != nil {
		log.ErrorLog.Printf("panel: %v", err)
	}
	return err
}
