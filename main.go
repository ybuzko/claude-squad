package main

import (
	"claude-squad/app"
	cmd2 "claude-squad/cmd"
	"claude-squad/config"
	"claude-squad/daemon"
	"claude-squad/linear"
	"claude-squad/log"
	"claude-squad/session"
	"claude-squad/session/git"
	"claude-squad/session/tmux"
	"claude-squad/status"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

var (
	version     = "1.0.20"
	programFlag string
	autoYesFlag bool
	daemonFlag  bool
	linearFlag  bool
	binName     string
	rootCmd     = &cobra.Command{
		Use:   "claude-squad",
		Short: "Claude Squad - Manage multiple AI agents like Claude Code, Aider, Codex, and Amp.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log.Initialize(daemonFlag)
			defer log.Close()

			if daemonFlag {
				cfg := config.LoadConfig()
				err := daemon.RunDaemon(cfg)
				log.ErrorLog.Printf("failed to start daemon %v", err)
				return err
			}

			cfg := config.LoadConfig()
			linearEnabled := cfg.Linear.Enabled || linearFlag

			if linearEnabled && cfg.Spawn.RepoPath != "" {
				// Linear mode creates every worktree from spawn.repo_path, so the current
				// directory does not matter.
				if !git.IsGitRepo(cfg.Spawn.RepoPath) {
					return fmt.Errorf("error: spawn.repo_path %q is not a git repository", cfg.Spawn.RepoPath)
				}
			} else {
				// Check if we're in a git repository
				currentDir, err := filepath.Abs(".")
				if err != nil {
					return fmt.Errorf("failed to get current directory: %w", err)
				}

				if !git.IsGitRepo(currentDir) {
					return fmt.Errorf("error: %s must be run from within a git repository", binName)
				}
			}

			// Program flag overrides config
			program := cfg.GetProgram()
			if programFlag != "" {
				program = programFlag
			}
			// AutoYes flag overrides config
			autoYes := cfg.AutoYes
			if autoYesFlag {
				autoYes = true
			}
			if autoYes {
				defer func() {
					if err := daemon.LaunchDaemon(); err != nil {
						log.ErrorLog.Printf("failed to launch daemon: %v", err)
					}
				}()
			}
			// Kill any daemon that's running.
			if err := daemon.StopDaemon(); err != nil {
				log.ErrorLog.Printf("failed to stop daemon: %v", err)
			}

			return app.Run(ctx, program, autoYes, linearFlag)
		},
	}

	hookCmd = &cobra.Command{
		Use:   "hook",
		Short: "Claude Code hook: record session status for the ticket in $CS_ISSUE_ID (reads the hook payload from stdin)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return status.HandleHook(os.Stdin, os.Getenv(status.IssueIDEnv), time.Now())
		},
	}

	resetCmd = &cobra.Command{
		Use:   "reset",
		Short: "Reset all stored instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()

			state := config.LoadState()
			storage, err := session.NewStorage(state)
			if err != nil {
				return fmt.Errorf("failed to initialize storage: %w", err)
			}
			if err := storage.DeleteAllInstances(); err != nil {
				return fmt.Errorf("failed to reset storage: %w", err)
			}
			fmt.Println("Storage has been reset successfully")

			if err := tmux.CleanupSessions(cmd2.MakeExecutor()); err != nil {
				return fmt.Errorf("failed to cleanup tmux sessions: %w", err)
			}
			fmt.Println("Tmux sessions have been cleaned up")

			if err := git.CleanupWorktrees(); err != nil {
				return fmt.Errorf("failed to cleanup worktrees: %w", err)
			}
			fmt.Println("Worktrees have been cleaned up")

			// Kill any daemon that's running.
			if err := daemon.StopDaemon(); err != nil {
				return err
			}
			fmt.Println("daemon has been stopped")

			return nil
		},
	}

	debugCmd = &cobra.Command{
		Use:   "debug",
		Short: "Print debug information like config paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()

			cfg := config.LoadConfig()

			configDir, err := config.GetConfigDir()
			if err != nil {
				return fmt.Errorf("failed to get config directory: %w", err)
			}
			configJson, _ := json.MarshalIndent(cfg, "", "  ")

			fmt.Printf("Config: %s\n%s\n", filepath.Join(configDir, config.ConfigFileName), configJson)

			// With Linear configured, prove the credentials and view id work by
			// listing what the dispatcher would see. Read-only.
			if cfg.Linear.Enabled || linearFlag {
				key := cfg.LinearAPIKey()
				if key == "" {
					fmt.Println("\nLinear: enabled, but no API key (set LINEAR_API_KEY)")
					return nil
				}
				client := linear.NewClient(key)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				issues, err := client.ViewIssues(ctx, cfg.Linear.ViewID)
				if err != nil {
					return fmt.Errorf("linear view check failed: %w", err)
				}
				rule := linear.DoneRule{StateTypes: cfg.Linear.DoneStateTypes, StateIDs: cfg.Linear.DoneStateIDs}
				fmt.Printf("\nLinear view %s: %d issues\n", cfg.Linear.ViewID, len(issues))
				for _, is := range issues {
					doneMark := ""
					if rule.IsDone(is) {
						doneMark = " (done)"
					}
					if is.DueDate != "" {
						doneMark += " (due " + is.DueDate + ")"
					}
					fmt.Printf("  %-10s %-14s %s%s\n", is.Identifier, is.StateName, is.Title, doneMark)
				}
			}

			return nil
		},
	}

	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("%s version %s\n", binName, version)
			fmt.Printf("https://github.com/smtg-ai/claude-squad/releases/tag/v%s\n", version)
		},
	}
)

func init() {
	rootCmd.Flags().StringVarP(&programFlag, "program", "p", "",
		"Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')")
	rootCmd.Flags().BoolVarP(&autoYesFlag, "autoyes", "y", false,
		"[experimental] If enabled, all instances will automatically accept prompts")
	rootCmd.Flags().BoolVar(&daemonFlag, "daemon", false, "Run a program that loads all sessions"+
		" and runs autoyes mode on them.")
	rootCmd.Flags().BoolVar(&linearFlag, "linear", false,
		"Enable the Linear dispatcher (same as linear.enabled=true in the config)")
	debugCmd.Flags().BoolVar(&linearFlag, "linear", false,
		"Also check the Linear view configured in linear.view_id")

	// Hide the daemonFlag as it's only for internal use
	err := rootCmd.Flags().MarkHidden("daemon")
	if err != nil {
		panic(err)
	}

	rootCmd.AddCommand(debugCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(hookCmd)
}

func main() {
	// Extract the binary name from how this was invoked
	binName = filepath.Base(os.Args[0])
	rootCmd.Use = binName

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
	}
}
