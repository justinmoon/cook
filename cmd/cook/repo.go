package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/repo"
	"github.com/spf13/cobra"
)

func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage repositories",
	}

	cmd.AddCommand(newRepoListCmd())
	cmd.AddCommand(newRepoAddCmd())
	cmd.AddCommand(newRepoRemoveCmd())

	return cmd
}

func newRepoListCmd() *cobra.Command {
	var ownerFilter string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List repositories",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			store := repo.NewStore(cfg.Server.DataDir)
			repos, err := store.List(ownerFilter)
			if err != nil {
				return err
			}

			if len(repos) == 0 {
				fmt.Println("No repositories found.")
				return nil
			}

			for _, r := range repos {
				fmt.Println(r.FullName())
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&ownerFilter, "owner", "", "Filter by owner (pubkey)")

	return cmd
}

func newRepoAddCmd() *cobra.Command {
	var cloneURL string

	cmd := &cobra.Command{
		Use:   "add <owner/name>",
		Short: "Create a new repository",
		Long:  "Create a new repository. Owner is typically your npub or hex pubkey.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := repo.ParseRepoRef(args[0])
			if err != nil {
				return err
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := cfg.EnsureDataDir(); err != nil {
				return err
			}

			store := repo.NewStore(cfg.Server.DataDir)

			var r *repo.Repo
			if cloneURL != "" {
				fmt.Printf("Cloning %s...\n", cloneURL)
				r, err = store.Clone(owner, name, cloneURL)
			} else {
				r, err = store.Create(owner, name)
			}

			if err != nil {
				return err
			}

			fmt.Printf("Created repository: %s\n", r.FullName())
			return nil
		},
	}

	cmd.Flags().StringVar(&cloneURL, "clone", "", "URL to clone from")

	return cmd
}

func newRepoRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <owner/name>",
		Short: "Remove a repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, name, err := repo.ParseRepoRef(args[0])
			if err != nil {
				return err
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			store := repo.NewStore(cfg.Server.DataDir)

			// Check if exists
			r, err := store.Get(owner, name)
			if err != nil {
				return err
			}
			if r == nil {
				return fmt.Errorf("repository %s/%s not found", owner, name)
			}

			// Confirm unless --force
			if !force {
				fmt.Printf("Remove repository %s/%s? This cannot be undone. [y/N]: ", owner, name)
				reader := bufio.NewReader(os.Stdin)
				response, _ := reader.ReadString('\n')
				response = strings.TrimSpace(strings.ToLower(response))
				if response != "y" && response != "yes" {
					fmt.Println("Aborted.")
					return nil
				}
			}

			if err := store.Remove(owner, name); err != nil {
				return err
			}

			fmt.Printf("Removed repository: %s/%s\n", owner, name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation")

	return cmd
}
