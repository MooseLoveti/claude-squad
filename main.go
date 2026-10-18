package main

import (
	cmd2 "claude-squad/cmd"
	"claude-squad/config"
	"claude-squad/dashboard"
	"claude-squad/launcher"
	"claude-squad/log"
	"claude-squad/panel"
	"claude-squad/relay"
	"claude-squad/workspace"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	version = "1.0.20"
	binName string
	rootCmd = &cobra.Command{
		Use:   "claude-squad [dir]",
		Short: "Claude Squad - Codex, Claude Code and Gemini side by side, asking each other with @tags.",
		Long: "Starts Codex, Claude Code and Gemini (agy) in a 2×2 tmux dashboard with a control panel.\n" +
			"Without dir, a launcher asks where to work. While the dashboard is running, this attaches to it.",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize("")
			defer log.Close()
			return run(args)
		},
	}

	panelCmd = &cobra.Command{
		Use:    "panel",
		Short:  "Run the dashboard's control panel (started by the dashboard)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize("[panel] ")
			defer log.Close()
			return panel.Run()
		},
	}

	hookCmd = &cobra.Command{
		Use:    "hook enter <pane>",
		Short:  "Handle Enter typed on an agent pane (called by tmux)",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize("[hook] ")
			defer log.Close()
			if args[0] != "enter" {
				return fmt.Errorf("unknown hook %q", args[0])
			}
			pane := args[1]
			configDir, err := config.GetConfigDir()
			if err == nil && panel.AskHook(panel.SocketPath(configDir), pane) {
				return nil
			}
			// No panel took it: Enter does what it always does.
			return dashboard.New(cmd2.MakeExecutor()).SendKeys(pane, "Enter")
		},
	}

	statusLineCmd = &cobra.Command{
		Use:    "statusline",
		Short:  "Claude Code status line command that records its rate limits",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize("[statusline] ")
			defer log.Close()
			configDir, err := config.GetConfigDir()
			if err == nil {
				err = relay.WriteClaudeStatusLine(os.Stdin, os.Stdout, configDir)
			}
			if err != nil {
				// Claude Code shows whatever this prints, so errors only go to the log.
				log.ErrorLog.Printf("status line: %v", err)
			}
			return nil
		},
	}

	resetCmd = &cobra.Command{
		Use:   "reset",
		Short: "Stop the dashboard and forget the open workspace (worktrees and branches are kept)",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize("")
			defer log.Close()
			tmux := dashboard.New(cmd2.MakeExecutor())
			if tmux.Running() {
				if err := tmux.Kill(); err != nil {
					return err
				}
				fmt.Println("Stopped the dashboard")
			}
			state := config.LoadState()
			state.Workspace = nil
			if err := state.Save(); err != nil {
				return err
			}
			fmt.Println("Forgot the open workspace")
			return nil
		},
	}

	debugCmd = &cobra.Command{
		Use:   "debug",
		Short: "Print debug information like config paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize("")
			defer log.Close()
			configDir, err := config.GetConfigDir()
			if err != nil {
				return fmt.Errorf("failed to get config directory: %w", err)
			}
			configJSON, _ := json.MarshalIndent(config.LoadConfig(), "", "  ")
			fmt.Printf("Config: %s\n%s\n", filepath.Join(configDir, config.ConfigFileName), configJSON)
			fmt.Printf("State: %s\n", filepath.Join(configDir, config.StateFileName))
			fmt.Printf("Log: %s\n", log.FileName())
			return nil
		},
	}

	transcriptCmd = &cobra.Command{
		Use:   "transcript <agent>",
		Short: "Print the conversation of an agent in the open workspace (claude, codex or gemini)",
		Long: "Prints the latest conversation of an agent in the open workspace, read from its CLI's own session log.\n" +
			"Agents are told about this command at launch, so they can read each other's conversations.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize("[transcript] ")
			defer log.Close()
			ws, err := workspace.Load(config.LoadState())
			if err != nil {
				return err
			}
			if ws == nil {
				return errors.New("開いているワークスペースがありません")
			}
			agent := relay.AgentByName(args[0])
			m, ok := ws.Member(agent.Name())
			if agent == relay.AgentUnknown || !ok {
				return fmt.Errorf("%q はこのワークスペースのAIではありません（claude / codex / gemini）", args[0])
			}
			transcript, err := agent.Transcript(m.Dir)
			if err != nil {
				return fmt.Errorf("%s の会話記録を読めませんでした: %w", agent.Label(), err)
			}
			fmt.Print(transcript)
			return nil
		},
	}

	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("%s version %s\n", binName, version)
		},
	}
)

// run asks where to work, every time: back to the running dashboard, or a
// new workspace, which replaces the running one.
func run(args []string) error {
	tmux := dashboard.New(cmd2.MakeExecutor())
	cfg := config.LoadConfig()
	state := config.LoadState()

	running := ""
	if tmux.Running() {
		running = "?"
		if ws, err := workspace.Load(state); err == nil && ws != nil {
			running = ws.Dir
		}
	}

	var choice launcher.Choice
	if len(args) == 1 {
		_, root, err := workspace.Inspect(config.ExpandHome(args[0]))
		if errors.Is(err, workspace.ErrNoCommits) {
			return fmt.Errorf("%s にはまだコミットがありません。一度コミットするか、引数なしで起動して初期コミットを作ってください", root)
		}
		if err != nil {
			return err
		}
		choice = launcher.Choice{Dir: root, Attach: root == running}
	} else {
		var err error
		if choice, err = launcher.Run(cfg, state, running); errors.Is(err, launcher.ErrCancelled) {
			return nil
		} else if err != nil {
			return err
		}
	}

	configDir, err := config.GetConfigDir()
	if err != nil {
		return err
	}
	// A request left by a dashboard that was not attached through here is stale.
	workspace.TakeSwitch(configDir)

	if !choice.Attach {
		if running != "" {
			// The worktrees stay, so the stopped workspace can be opened again.
			if err := tmux.Kill(); err != nil {
				return err
			}
		}
		if err := start(tmux, cfg, state, configDir, choice); err != nil {
			return err
		}
	}
	// The panel's folder button stops the dashboard after leaving a switch
	// request; reopen it on the chosen folder and attach again.
	for {
		attachErr := tmux.Attach()
		dir, ok := workspace.TakeSwitch(configDir)
		if !ok {
			return attachErr
		}
		if err := start(tmux, cfg, state, configDir, launcher.Choice{Dir: dir}); err != nil {
			return err
		}
	}
}

// start opens a workspace on the chosen directory and starts its dashboard.
func start(tmux *dashboard.Tmux, cfg *config.Config, state *config.State, configDir string, choice launcher.Choice) error {
	ws, err := workspace.Open(choice.Dir, choice.Scratch, cfg)
	if err != nil {
		return err
	}
	if err := workspace.Save(state, ws); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := workspace.WriteBriefings(ws, self); err != nil {
		// Only agy goes without its briefing; the dashboard still starts.
		log.ErrorLog.Printf("write briefings: %v", err)
	}
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width, height = 200, 50
	}
	return tmux.Create(dashboard.Layout(ws, self, configDir), width, height-1, self)
}

func init() {
	rootCmd.AddCommand(panelCmd, hookCmd, statusLineCmd, resetCmd, debugCmd, transcriptCmd, versionCmd)
}

func main() {
	binName = filepath.Base(os.Args[0])
	rootCmd.Use = binName + " [dir]"
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
