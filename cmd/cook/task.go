package main

import (
	"fmt"
	"strings"

	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/events"
	"github.com/justinmoon/cook/internal/task"
	"github.com/spf13/cobra"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage tasks",
	}

	cmd.AddCommand(newTaskListCmd())
	cmd.AddCommand(newTaskCreateCmd())
	cmd.AddCommand(newTaskShowCmd())
	cmd.AddCommand(newTaskCloseCmd())

	return cmd
}

func newTaskListCmd() *cobra.Command {
	var repoFilter string
	var statusFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := openDatabase(cfg)
			if err != nil {
				return err
			}
			defer database.Close()

			store := task.NewStore(database)
			tasks, err := store.List(repoFilter, statusFilter)
			if err != nil {
				return err
			}

			if len(tasks) == 0 {
				fmt.Println("No tasks found.")
				return nil
			}

			for _, t := range tasks {
				statusIcon := "○"
				switch t.Status {
				case task.StatusInProgress:
					statusIcon = "◐"
				case task.StatusClosed:
					statusIcon = "●"
				case task.StatusNeedsHuman:
					statusIcon = "!"
				}

				// Check if blocked
				blocked, blockers, _ := store.IsBlocked(&t)
				suffix := ""
				if blocked {
					statusIcon = "⊘"
					suffix = fmt.Sprintf(" [blocked by: %s]", strings.Join(blockers, ", "))
				}

				fmt.Printf("%s [P%d] %s/%s: %s%s\n", statusIcon, t.Priority, t.Repo, t.Slug, t.Title, suffix)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repoFilter, "repo", "", "Filter by repository")
	cmd.Flags().StringVar(&statusFilter, "status", "", "Filter by status (open, in_progress, needs_human, closed)")

	return cmd
}

func newTaskCreateCmd() *cobra.Command {
	var title string
	var body string
	var priority int
	var dependsOn []string

	cmd := &cobra.Command{
		Use:   "create <repo> <slug>",
		Short: "Create a new task",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := args[0]
			slug := args[1]

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			database, err := openDatabase(cfg)
			if err != nil {
				return err
			}
			defer database.Close()

			store := task.NewStore(database)

			t := &task.Task{
				Slug:      slug,
				Repo:      repo,
				Title:     title,
				Body:      body,
				Priority:  priority,
				DependsOn: dependsOn,
			}

			if err := store.Create(t); err != nil {
				if strings.Contains(err.Error(), "UNIQUE constraint") {
					return fmt.Errorf("task %s/%s already exists", repo, slug)
				}
				return err
			}

			// Publish event
			if bus := getEventBus(cfg); bus != nil {
				defer bus.Close()
				publishEvent(bus, events.Event{
					Type:   events.EventTaskCreated,
					TaskID: slug,
					Repo:   repo,
				})
			}

			fmt.Printf("Created task: %s/%s\n", repo, slug)
			return nil
		},
	}

	cmd.Flags().StringVar(&title, "title", "", "Task title (required)")
	cmd.Flags().StringVar(&body, "body", "", "Task body/description")
	cmd.Flags().IntVar(&priority, "priority", 3, "Priority (1-5, higher is more urgent)")
	cmd.Flags().StringSliceVar(&dependsOn, "depends-on", nil, "Task IDs this task depends on (format: repo/slug)")
	cmd.MarkFlagRequired("title")

	return cmd
}

func newTaskShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <repo/slug>",
		Short: "Show task details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, slug, err := requireRef(args[0], "task")
			if err != nil {
				return err
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := openDatabase(cfg)
			if err != nil {
				return err
			}
			defer database.Close()

			store := task.NewStore(database)
			t, err := store.Get(repo, slug)
			if err != nil {
				return err
			}
			if t == nil {
				return fmt.Errorf("task %s/%s not found", repo, slug)
			}

			fmt.Printf("Task:     %s/%s\n", t.Repo, t.Slug)
			fmt.Printf("Title:    %s\n", t.Title)
			fmt.Printf("Status:   %s\n", t.Status)
			fmt.Printf("Priority: %d\n", t.Priority)
			fmt.Printf("Created:  %s\n", t.CreatedAt.Format("2006-01-02 15:04:05"))
			if t.Body != "" {
				fmt.Printf("\n%s\n", t.Body)
			}
			if len(t.DependsOn) > 0 {
				fmt.Printf("\nDepends on: %s\n", strings.Join(t.DependsOn, ", "))
			}

			return nil
		},
	}
}

func newTaskCloseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close <repo/slug>",
		Short: "Close a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, slug, err := requireRef(args[0], "task")
			if err != nil {
				return err
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := openDatabase(cfg)
			if err != nil {
				return err
			}
			defer database.Close()

			store := task.NewStore(database)

			// Get task to know repo for event
			t, err := store.Get(repo, slug)
			if err != nil {
				return err
			}
			if t == nil {
				return fmt.Errorf("task %s/%s not found", repo, slug)
			}

			if err := store.UpdateStatus(repo, slug, task.StatusClosed); err != nil {
				return err
			}

			// Publish event
			if bus := getEventBus(cfg); bus != nil {
				defer bus.Close()
				publishEvent(bus, events.Event{
					Type:   events.EventTaskClosed,
					TaskID: slug,
					Repo:   repo,
				})
			}

			fmt.Printf("Closed task: %s/%s\n", repo, slug)
			return nil
		},
	}
}
