package main

import (
	"fmt"
	"os"
	"time"

	"github.com/justinmoon/cook/internal/agent"
	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/db"
	"github.com/spf13/cobra"
)

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage agent sessions",
	}

	cmd.AddCommand(newAgentListCmd())
	cmd.AddCommand(newAgentShowCmd())
	cmd.AddCommand(newAgentKillCmd())

	return cmd
}

func newAgentListCmd() *cobra.Command {
	var repoFilter string
	var branchFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List agent sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := db.Open(cfg.Server.DataDir)
			if err != nil {
				return err
			}
			defer database.Close()

			store := agent.NewStore(database)
			sessions, err := store.List(repoFilter, branchFilter)
			if err != nil {
				return err
			}

			if len(sessions) == 0 {
				fmt.Println("No agent sessions found.")
				return nil
			}

			for _, s := range sessions {
				statusIcon := "○"
				switch s.Status {
				case agent.StatusRunning:
					statusIcon = "◐"
				case agent.StatusCompleted:
					statusIcon = "●"
				case agent.StatusFailed:
					statusIcon = "✗"
				case agent.StatusNeedsHelp:
					statusIcon = "?"
				}

				pidStr := ""
				if s.PID != nil {
					if agent.IsRunning(*s.PID) {
						pidStr = fmt.Sprintf(" (pid: %d)", *s.PID)
					} else {
						pidStr = fmt.Sprintf(" (pid: %d, dead)", *s.PID)
					}
				}

				fmt.Printf("%s [%d] %s on %s/%s%s\n", statusIcon, s.ID, s.AgentType, s.BranchRepo, s.BranchName, pidStr)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&repoFilter, "repo", "", "Filter by repo")
	cmd.Flags().StringVar(&branchFilter, "branch", "", "Filter by branch name")

	return cmd
}

func newAgentShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <session-id>",
		Short: "Show agent session details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sessionID int64
			fmt.Sscanf(args[0], "%d", &sessionID)

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := db.Open(cfg.Server.DataDir)
			if err != nil {
				return err
			}
			defer database.Close()

			store := agent.NewStore(database)
			session, err := store.Get(sessionID)
			if err != nil {
				return err
			}
			if session == nil {
				return fmt.Errorf("session %d not found", sessionID)
			}

			fmt.Printf("Session ID: %d\n", session.ID)
			fmt.Printf("Branch: %s/%s\n", session.BranchRepo, session.BranchName)
			fmt.Printf("Agent: %s\n", session.AgentType)
			fmt.Printf("Status: %s\n", session.Status)

			if session.PID != nil {
				running := agent.IsRunning(*session.PID)
				fmt.Printf("PID: %d (running: %v)\n", *session.PID, running)
			}

			if session.ExitCode != nil {
				fmt.Printf("Exit Code: %d\n", *session.ExitCode)
			}

			fmt.Printf("Started: %s\n", session.StartedAt.Format(time.RFC3339))
			if session.EndedAt != nil {
				fmt.Printf("Ended: %s\n", session.EndedAt.Format(time.RFC3339))
				fmt.Printf("Duration: %s\n", session.EndedAt.Sub(session.StartedAt))
			}

			if session.Prompt != "" {
				fmt.Printf("\nPrompt:\n%s\n", session.Prompt)
			}

			return nil
		},
	}
}

func newAgentKillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <session-id>",
		Short: "Kill an agent session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sessionID int64
			fmt.Sscanf(args[0], "%d", &sessionID)

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := db.Open(cfg.Server.DataDir)
			if err != nil {
				return err
			}
			defer database.Close()

			store := agent.NewStore(database)
			session, err := store.Get(sessionID)
			if err != nil {
				return err
			}
			if session == nil {
				return fmt.Errorf("session %d not found", sessionID)
			}

			if session.PID == nil {
				return fmt.Errorf("session has no PID")
			}

			if !agent.IsRunning(*session.PID) {
				fmt.Println("Agent process is not running.")
				return nil
			}

			if err := agent.Kill(*session.PID); err != nil {
				return fmt.Errorf("failed to kill agent: %w", err)
			}

			// Update session status
			now := time.Now()
			session.Status = agent.StatusFailed
			session.EndedAt = &now
			code := -1
			session.ExitCode = &code
			if err := store.Update(session); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to update session: %v\n", err)
			}

			fmt.Printf("Killed agent session %d (pid %d)\n", sessionID, *session.PID)
			return nil
		},
	}
}
