package main

import (
	"fmt"
	"os"
	stdExec "os/exec"
	"path/filepath"
	"strings"

	"github.com/justinmoon/cook/internal/agent"
	"github.com/justinmoon/cook/internal/branch"
	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/db"
	"github.com/justinmoon/cook/internal/events"
	"github.com/justinmoon/cook/internal/gate"
	"github.com/justinmoon/cook/internal/repo"
	"github.com/justinmoon/cook/internal/task"
	"github.com/spf13/cobra"
)

func newBranchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "branch",
		Short: "Manage branches",
	}

	cmd.AddCommand(newBranchCreateCmd())
	cmd.AddCommand(newBranchListCmd())
	cmd.AddCommand(newBranchShowCmd())
	cmd.AddCommand(newBranchAbandonCmd())
	cmd.AddCommand(newBranchMergeCmd())

	return cmd
}

func newBranchCreateCmd() *cobra.Command {
	var taskID string
	var envSpec string
	var agentType string
	var prompt string

	cmd := &cobra.Command{
		Use:   "create <repo> <name>",
		Short: "Create a new branch",
		Long: `Create a new branch with an environment.

Environment spec format:
  local:/path/to/checkout   - Local filesystem checkout
  local                     - Local checkout in default location (data_dir/checkouts/<branch>)

Agent types: claude, codex, opencode
`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoName := args[0]
			branchName := args[1]

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			database, err := db.Open(cfg.Server.DataDir)
			if err != nil {
				return err
			}
			defer database.Close()

			// Verify repo exists (repoName is owner/name format)
			repoOwner, repoShortName, err := repo.ParseRepoRef(repoName)
			if err != nil {
				return err
			}
			repoStore := repo.NewStore(cfg.Server.DataDir)
			r, err := repoStore.Get(repoOwner, repoShortName)
			if err != nil {
				return err
			}
			if r == nil {
				return fmt.Errorf("repository %s not found", repoName)
			}

			// Parse environment spec
			env, err := parseEnvSpec(envSpec, branchName, cfg.Server.DataDir)
			if err != nil {
				return err
			}

			// Get base revision
			baseRev := ""
			// Try to get master HEAD, ok if it fails (empty repo)
			if rev, err := getRevision(r.Path, "master"); err == nil {
				baseRev = rev
			}

			branchStore := branch.NewStore(database, cfg.Server.DataDir)

			// Check if branch already exists
			existing, err := branchStore.Get(repoName, branchName)
			if err != nil {
				return err
			}
			if existing != nil {
				return fmt.Errorf("branch %s/%s already exists", repoName, branchName)
			}

			// Validate task BEFORE creating checkout
			var taskStore *task.Store
			var taskRepo, taskSlug string
			if taskID != "" {
				// Task can be "slug" (same repo) or "repo/slug"
				taskRepo, taskSlug = parseRef(taskID)
				if taskRepo == "" {
					taskRepo = repoName
					taskSlug = taskID
				}

				taskStore = task.NewStore(database)
				t, err := taskStore.Get(taskRepo, taskSlug)
				if err != nil {
					return err
				}
				if t == nil {
					return fmt.Errorf("task %s/%s not found", taskRepo, taskSlug)
				}

				// Check if task is blocked
				blocked, blockers, err := taskStore.IsBlocked(t)
				if err != nil {
					return err
				}
				if blocked {
					return fmt.Errorf("task %s/%s is blocked by: %s", taskRepo, taskSlug, strings.Join(blockers, ", "))
				}
			}

			// Create the local checkout
			fmt.Printf("Creating checkout at %s...\n", env.Path)
			if err := branchStore.CreateLocalCheckout(r.Path, branchName, env.Path); err != nil {
				return fmt.Errorf("failed to create checkout: %w", err)
			}

			// Get head rev after checkout
			headRev := baseRev
			if rev, err := getRevision(env.Path, "HEAD"); err == nil {
				headRev = rev
			}

			// Create branch record
			b := &branch.Branch{
				Name:        branchName,
				Repo:        repoName,
				BaseRev:     baseRev,
				HeadRev:     headRev,
				Environment: env,
				Status:      branch.StatusActive,
			}

			if taskID != "" {
				b.TaskRepo = &taskRepo
				b.TaskSlug = &taskSlug

				// Update task status
				if err := taskStore.UpdateStatus(taskRepo, taskSlug, task.StatusInProgress); err != nil {
					return err
				}
			}

			if err := branchStore.Create(b); err != nil {
				return err
			}

			// Publish event
			if bus := getEventBus(cfg); bus != nil {
				defer bus.Close()
				publishEvent(bus, events.Event{
					Type:   events.EventBranchCreated,
					Branch: branchName,
					Repo:   repoName,
				})
			}

			fmt.Printf("Created branch: %s\n", branchName)
			fmt.Printf("  Repo: %s\n", repoName)
			fmt.Printf("  Checkout: %s\n", env.Path)
			if taskID != "" {
				fmt.Printf("  Task: %s\n", taskID)
			}

			// Spawn agent if requested
			if agentType != "" {
				agentStore := agent.NewStore(database)
				session := &agent.Session{
					BranchRepo: repoName,
					BranchName: branchName,
					AgentType:  agent.AgentType(agentType),
					Prompt:     prompt,
				}

				if err := agentStore.Create(session); err != nil {
					return fmt.Errorf("failed to create agent session: %w", err)
				}

				// Spawn the agent process
				agentCmd, err := agent.Spawn(agent.AgentType(agentType), env.Path, prompt, repoName, branchName)
				if err != nil {
					return fmt.Errorf("failed to spawn agent: %w", err)
				}

				// Connect to stdio for interactive use
				agentCmd.Stdin = os.Stdin
				agentCmd.Stdout = os.Stdout
				agentCmd.Stderr = os.Stderr

				fmt.Printf("  Agent: %s\n", agentType)
				fmt.Println("\nStarting agent...")

				if err := agentCmd.Start(); err != nil {
					return fmt.Errorf("failed to start agent: %w", err)
				}

				pid := agentCmd.Process.Pid
				session.PID = &pid
				session.Status = agent.StatusRunning
				agentStore.Update(session)

				// Wait for agent to complete
				err = agentCmd.Wait()

				// Update session status
				if err != nil {
					if exitErr, ok := err.(*stdExec.ExitError); ok {
						code := exitErr.ExitCode()
						session.ExitCode = &code
					}
					session.Status = agent.StatusFailed
				} else {
					code := 0
					session.ExitCode = &code
					session.Status = agent.StatusCompleted
				}
				agentStore.Update(session)

				fmt.Printf("\nAgent exited with status: %s\n", session.Status)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&taskID, "task", "", "Link to a task")
	cmd.Flags().StringVar(&envSpec, "env", "local", "Environment spec (local, local:/path)")
	cmd.Flags().StringVar(&agentType, "agent", "", "Agent to spawn (claude, codex, opencode)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Initial prompt for the agent")

	return cmd
}

func newBranchListCmd() *cobra.Command {
	var repoFilter string
	var statusFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List branches",
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

			store := branch.NewStore(database, cfg.Server.DataDir)
			branches, err := store.List(repoFilter, statusFilter)
			if err != nil {
				return err
			}

			if len(branches) == 0 {
				fmt.Println("No branches found.")
				return nil
			}

			for _, b := range branches {
				statusIcon := "○"
				switch b.Status {
				case branch.StatusMerged:
					statusIcon = "●"
				case branch.StatusAbandoned:
					statusIcon = "✗"
				}

				taskInfo := ""
				if b.TaskRepo != nil && b.TaskSlug != nil {
					taskInfo = fmt.Sprintf(" [task: %s/%s]", *b.TaskRepo, *b.TaskSlug)
				}

				fmt.Printf("%s %s/%s%s\n", statusIcon, b.Repo, b.Name, taskInfo)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repoFilter, "repo", "", "Filter by repository")
	cmd.Flags().StringVar(&statusFilter, "status", "", "Filter by status (active, merged, abandoned)")

	return cmd
}

func newBranchShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <repo/name>",
		Short: "Show branch details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, name, err := requireRef(args[0], "branch")
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

			store := branch.NewStore(database, cfg.Server.DataDir)
			b, err := store.Get(repo, name)
			if err != nil {
				return err
			}
			if b == nil {
				return fmt.Errorf("branch %s/%s not found", repo, name)
			}

			fmt.Printf("Branch:   %s/%s\n", b.Repo, b.Name)
			fmt.Printf("Status:   %s\n", b.Status)
			fmt.Printf("Base:     %s\n", truncateRev(b.BaseRev))
			fmt.Printf("Head:     %s\n", truncateRev(b.HeadRev))
			fmt.Printf("Backend:  %s\n", b.Environment.Backend)
			fmt.Printf("Path:     %s\n", b.Environment.Path)
			fmt.Printf("Created:  %s\n", b.CreatedAt.Format("2006-01-02 15:04:05"))
			if b.TaskRepo != nil && b.TaskSlug != nil {
				fmt.Printf("Task:     %s/%s\n", *b.TaskRepo, *b.TaskSlug)
			}
			if b.MergedAt != nil {
				fmt.Printf("Merged:   %s\n", b.MergedAt.Format("2006-01-02 15:04:05"))
			}

			return nil
		},
	}
}

func parseEnvSpec(spec, branchName, dataDir string) (branch.EnvironmentSpec, error) {
	env := branch.EnvironmentSpec{}

	if spec == "" || spec == "local" {
		env.Backend = "local"
		env.Path = filepath.Join(dataDir, "checkouts", branchName)
		return env, nil
	}

	if strings.HasPrefix(spec, "local:") {
		env.Backend = "local"
		env.Path = strings.TrimPrefix(spec, "local:")
		return env, nil
	}

	return env, fmt.Errorf("invalid environment spec: %s (supported: local, local:/path)", spec)
}

func getRevision(repoPath, ref string) (string, error) {
	output, err := runGit(repoPath, "rev-parse", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func runGit(repoPath string, args ...string) (string, error) {
	cmd := stdExec.Command("git", append([]string{"-C", repoPath}, args...)...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func truncateRev(rev string) string {
	if len(rev) > 8 {
		return rev[:8]
	}
	return rev
}

func newBranchMergeCmd() *cobra.Command {
	var force bool
	var skipGates bool

	cmd := &cobra.Command{
		Use:   "merge <repo/name>",
		Short: "Merge a branch to master",
		Long:  "Merge a branch to master, delete the checkout, and close the linked task.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoName, name, err := requireRef(args[0], "branch")
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
			b, err := branchStore.Get(repoName, name)
			if err != nil {
				return err
			}
			if b == nil {
				return fmt.Errorf("branch %s/%s not found", repoName, name)
			}

			if b.Status != branch.StatusActive {
				return fmt.Errorf("branch %s/%s is not active (status: %s)", repoName, name, b.Status)
			}

			// Get repo (b.Repo is owner/name format)
			repoOwner, repoShortName, err := repo.ParseRepoRef(b.Repo)
			if err != nil {
				return err
			}
			repoStore := repo.NewStore(cfg.Server.DataDir)
			r, err := repoStore.Get(repoOwner, repoShortName)
			if err != nil {
				return err
			}
			if r == nil {
				return fmt.Errorf("repository %s not found", b.Repo)
			}

			// Check for uncommitted changes
			output, err := runGit(b.Environment.Path, "status", "--porcelain")
			if err != nil {
				return fmt.Errorf("failed to check git status: %w", err)
			}
			if strings.TrimSpace(output) != "" && !force {
				return fmt.Errorf("branch has uncommitted changes; commit or use --force")
			}

			// Check gates unless --skip-gates
			if !skipGates {
				repoConfig, err := gate.LoadRepoConfig(b.Environment.Path)
				if err != nil {
					return fmt.Errorf("failed to load cook.toml: %w", err)
				}

				if len(repoConfig.Gates) > 0 {
					gateStore := gate.NewStore(database, cfg.Server.DataDir)
					currentRev, _ := getRevision(b.Environment.Path, "HEAD")

					for _, g := range repoConfig.Gates {
						run, err := gateStore.GetLatestRun(repoName, name, g.Name)
						if err != nil {
							return err
						}

						branchRef := repoName + "/" + name
						if run == nil {
							return fmt.Errorf("gate %q has not been run; use 'cook gate run %s' first", g.Name, branchRef)
						}

						if run.Status != gate.StatusPassed {
							return fmt.Errorf("gate %q has not passed (status: %s); use 'cook gate run %s' to retry", g.Name, run.Status, branchRef)
						}

						// Check if gate was run on current HEAD
						if run.Rev != currentRev {
							return fmt.Errorf("gate %q was run on old commit; use 'cook gate run %s' to re-run", g.Name, branchRef)
						}
					}
					fmt.Println("All gates passed!")
				}
			}

			// Push branch to bare repo
			fmt.Println("Pushing branch to repository...")
			_, err = runGit(b.Environment.Path, "push", "origin", name)
			if err != nil {
				return fmt.Errorf("failed to push branch: %w", err)
			}

			// Merge branch to master in bare repo
			fmt.Println("Merging to master...")

			// Check if master exists
			masterExists := true
			_, err = runGit(r.Path, "rev-parse", "--verify", "refs/heads/master")
			if err != nil {
				masterExists = false
			}

			// If master exists, check for fast-forward
			if masterExists {
				_, err = runGit(r.Path, "merge-base", "--is-ancestor", "refs/heads/master", name)
				if err != nil {
					// Not a fast-forward, need actual merge
					// For now, we'll require fast-forward
					return fmt.Errorf("not a fast-forward merge; please rebase first")
				}
			}

			// Get the branch head
			headRev, err := getRevision(r.Path, name)
			if err != nil {
				return fmt.Errorf("failed to get branch head: %w", err)
			}

			// Update master to point to branch head
			_, err = runGit(r.Path, "update-ref", "refs/heads/master", headRev)
			if err != nil {
				return fmt.Errorf("failed to update master: %w", err)
			}

			// Delete the branch ref
			_, err = runGit(r.Path, "update-ref", "-d", "refs/heads/"+name)
			if err != nil {
				fmt.Printf("Warning: failed to delete branch ref: %v\n", err)
			}

			// Remove checkout
			if b.Environment.Path != "" {
				if err := branchStore.RemoveLocalCheckout(b.Environment.Path); err != nil {
					fmt.Printf("Warning: failed to remove checkout: %v\n", err)
				}
			}

			// Update branch status
			if err := branchStore.UpdateStatus(repoName, name, branch.StatusMerged); err != nil {
				return err
			}

			// Close linked task
			if b.TaskRepo != nil && b.TaskSlug != nil {
				taskStore := task.NewStore(database)
				if err := taskStore.UpdateStatus(*b.TaskRepo, *b.TaskSlug, task.StatusClosed); err != nil {
					fmt.Printf("Warning: failed to close task: %v\n", err)
				} else {
					fmt.Printf("Closed task: %s/%s\n", *b.TaskRepo, *b.TaskSlug)
				}
			}

			// Publish event
			if bus := getEventBus(cfg); bus != nil {
				defer bus.Close()
				publishEvent(bus, events.Event{
					Type:   events.EventBranchMerged,
					Branch: name,
					Repo:   b.Repo,
				})
			}

			fmt.Printf("Merged branch: %s -> master\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force merge even with uncommitted changes")
	cmd.Flags().BoolVar(&skipGates, "skip-gates", false, "Skip gate checks")

	return cmd
}

func newBranchAbandonCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "abandon <repo/name>",
		Short: "Abandon a branch",
		Long:  "Abandon a branch, removing its checkout and marking it as abandoned.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoName, name, err := requireRef(args[0], "branch")
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

			store := branch.NewStore(database, cfg.Server.DataDir)
			b, err := store.Get(repoName, name)
			if err != nil {
				return err
			}
			if b == nil {
				return fmt.Errorf("branch %s/%s not found", repoName, name)
			}

			if b.Status != branch.StatusActive {
				return fmt.Errorf("branch %s/%s is not active (status: %s)", repoName, name, b.Status)
			}

			// Confirm unless --force
			if !force {
				fmt.Printf("Abandon branch %s/%s? This will delete the checkout. [y/N]: ", repoName, name)
				var response string
				fmt.Scanln(&response)
				response = strings.ToLower(strings.TrimSpace(response))
				if response != "y" && response != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			// Remove checkout
			if b.Environment.Path != "" {
				if err := store.RemoveLocalCheckout(b.Environment.Path); err != nil {
					fmt.Printf("Warning: failed to remove checkout: %v\n", err)
				}
			}

			// Update status
			if err := store.UpdateStatus(repoName, name, branch.StatusAbandoned); err != nil {
				return err
			}

			// Reset task status if linked
			if b.TaskRepo != nil && b.TaskSlug != nil {
				taskStore := task.NewStore(database)
				if err := taskStore.UpdateStatus(*b.TaskRepo, *b.TaskSlug, task.StatusOpen); err != nil {
					fmt.Printf("Warning: failed to reset task status: %v\n", err)
				}
			}

			// Publish event
			if bus := getEventBus(cfg); bus != nil {
				defer bus.Close()
				publishEvent(bus, events.Event{
					Type:   events.EventBranchAbandoned,
					Branch: name,
					Repo:   repoName,
				})
			}

			fmt.Printf("Abandoned branch: %s/%s\n", repoName, name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation")

	return cmd
}
