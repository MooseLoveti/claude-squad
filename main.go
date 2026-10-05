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

	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("%s version %s\n", binName, version)
		},
	}
)

// run attaches to the running dashboard, or chooses a workspace and starts one.
func run(args []string) error {
	tmux := dashboard.New(cmd2.MakeExecutor())
	if tmux.Running() {
		if len(args) > 0 {
			fmt.Println("開いているワークスペースに戻ります。別の場所で始めるには、パネルでワークスペースを閉じてください。")
		}
		return tmux.Attach()
	}

	cfg := config.LoadConfig()
	state := config.LoadState()
	var choice launcher.Choice
	if len(args) == 1 {
		_, root, err := workspace.Inspect(config.ExpandHome(args[0]))
		if errors.Is(err, workspace.ErrNoCommits) {
			return fmt.Errorf("%s にはまだコミットがありません。一度コミットするか、引数なしで起動して初期コミットを作ってください", root)
		}
		if err != nil {
			return err
		}
		choice = launcher.Choice{Dir: root}
	} else {
		var err error
		if choice, err = launcher.Run(cfg, state); errors.Is(err, launcher.ErrCancelled) {
			return nil
		} else if err != nil {
			return err
		}
	}

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
	configDir, err := config.GetConfigDir()
	if err != nil {
		return err
	}
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width, height = 200, 50
	}
	if err := tmux.Create(dashboard.Layout(ws, self, configDir), width, height-1, self); err != nil {
		return err
	}
	return tmux.Attach()
}

func init() {
	rootCmd.AddCommand(panelCmd, hookCmd, statusLineCmd, resetCmd, debugCmd, versionCmd)
}

func main() {
	binName = filepath.Base(os.Args[0])
	rootCmd.Use = binName + " [dir]"
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
