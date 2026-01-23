package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/justinmoon/cook/internal/branch"
	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/db"
	"github.com/justinmoon/cook/internal/events"
	"github.com/justinmoon/cook/internal/gate"
	"github.com/spf13/cobra"
)

func newGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Manage gates",
	}

	cmd.AddCommand(newGateRunCmd())
	cmd.AddCommand(newGateStatusCmd())

	return cmd
}

func newGateRunCmd() *cobra.Command {
	var gateName string

	cmd := &cobra.Command{
		Use:   "run <repo/branch>",
		Short: "Run gates for a branch",
		Long: `Run gates for a branch. If --gate is specified, only that gate is run.
Otherwise, all gates defined in cook.toml are run in sequence.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoName, branchName, err := requireRef(args[0], "branch")
			if err != nil {
				return err
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := db.Open(cfg.Server.DataDir)
			if err != nil {
				return err
			}
			defer database.Close()

			branchStore := branch.NewStore(database, cfg.Server.DataDir)
			b, err := branchStore.Get(repoName, branchName)
			if err != nil {
				return err
			}
			if b == nil {
				return fmt.Errorf("branch %s/%s not found", repoName, branchName)
			}

			if b.Status != branch.StatusActive {
				return fmt.Errorf("branch %s/%s is not active", repoName, branchName)
			}

			// Load repo config
			repoConfig, err := gate.LoadRepoConfig(b.Environment.Path)
			if err != nil {
				return fmt.Errorf("failed to load cook.toml: %w", err)
			}

			if len(repoConfig.Gates) == 0 {
				fmt.Println("No gates configured in cook.toml")
				return nil
			}

			// Get current HEAD
			rev, err := getRevision(b.Environment.Path, "HEAD")
			if err != nil {
				return fmt.Errorf("failed to get HEAD: %w", err)
			}

			gateStore := gate.NewStore(database, cfg.Server.DataDir)

			// Filter gates if --gate specified
			var gatesToRun []gate.Gate
			if gateName != "" {
				for _, g := range repoConfig.Gates {
					if g.Name == gateName {
						gatesToRun = append(gatesToRun, g)
						break
					}
				}
				if len(gatesToRun) == 0 {
					return fmt.Errorf("gate %q not found in cook.toml", gateName)
				}
			} else {
				gatesToRun = repoConfig.Gates
			}

			// Get event bus for publishing
			bus := getEventBus(cfg)
			if bus != nil {
				defer bus.Close()
			}

			// Run gates
			allPassed := true
			for _, g := range gatesToRun {
				fmt.Printf("Running gate: %s\n", g.Name)
				fmt.Printf("  Command: %s\n", g.Command)

				// Publish gate started
				publishEvent(bus, events.Event{
					Type:     events.EventGateStarted,
					Branch:   branchName,
					Repo:     repoName,
					GateName: g.Name,
				})

				run, err := gateStore.RunGate(g, repoName, branchName, rev, b.Environment.Path)
				if err != nil {
					return fmt.Errorf("failed to run gate %s: %w", g.Name, err)
				}

				if run.Status == gate.StatusPassed {
					fmt.Printf("  Result: PASSED\n")
					publishEvent(bus, events.Event{
						Type:     events.EventGatePassed,
						Branch:   branchName,
						Repo:     repoName,
						GateName: g.Name,
					})
				} else {
					fmt.Printf("  Result: FAILED (exit code: %d)\n", *run.ExitCode)
					fmt.Printf("  Log: %s\n", run.LogPath)
					allPassed = false
					publishEvent(bus, events.Event{
						Type:     events.EventGateFailed,
						Branch:   branchName,
						Repo:     repoName,
						GateName: g.Name,
					})
				}
			}

			if !allPassed {
				return fmt.Errorf("some gates failed")
			}

			fmt.Println("\nAll gates passed!")
			return nil
		},
	}

	cmd.Flags().StringVar(&gateName, "gate", "", "Run only this gate")

	return cmd
}

func newGateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <repo/branch>",
		Short: "Show gate status for a branch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoName, branchName, err := requireRef(args[0], "branch")
			if err != nil {
				return err
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := db.Open(cfg.Server.DataDir)
			if err != nil {
				return err
			}
			defer database.Close()

			branchStore := branch.NewStore(database, cfg.Server.DataDir)
			b, err := branchStore.Get(repoName, branchName)
			if err != nil {
				return err
			}
			if b == nil {
				return fmt.Errorf("branch %s/%s not found", repoName, branchName)
			}

			// Load repo config
			repoConfig, err := gate.LoadRepoConfig(b.Environment.Path)
			if err != nil {
				return fmt.Errorf("failed to load cook.toml: %w", err)
			}

			if len(repoConfig.Gates) == 0 {
				fmt.Println("No gates configured in cook.toml")
				return nil
			}

			gateStore := gate.NewStore(database, cfg.Server.DataDir)

			fmt.Printf("Gates for branch %s/%s:\n\n", repoName, branchName)

			for _, g := range repoConfig.Gates {
				run, err := gateStore.GetLatestRun(repoName, branchName, g.Name)
				if err != nil {
					return err
				}

				statusIcon := "○"
				statusText := "not run"

				if run != nil {
					switch run.Status {
					case gate.StatusPassed:
						statusIcon = "●"
						statusText = "passed"
					case gate.StatusFailed:
						statusIcon = "✗"
						statusText = fmt.Sprintf("failed (exit %d)", *run.ExitCode)
					case gate.StatusRunning:
						statusIcon = "◐"
						statusText = "running"
					}
				}

				fmt.Printf("%s %s: %s\n", statusIcon, g.Name, statusText)
				if run != nil && run.LogPath != "" {
					// Show last few lines of log on failure
					if run.Status == gate.StatusFailed {
						if content, err := os.ReadFile(run.LogPath); err == nil {
							lines := strings.Split(string(content), "\n")
							start := len(lines) - 5
							if start < 0 {
								start = 0
							}
							for _, line := range lines[start:] {
								if line != "" {
									fmt.Printf("    %s\n", line)
								}
							}
						}
					}
				}
			}

			return nil
		},
	}
}
