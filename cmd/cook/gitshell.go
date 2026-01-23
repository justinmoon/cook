package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/repo"
	"github.com/spf13/cobra"
)

func newGitShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "git-shell <pubkey>",
		Short:  "Git shell for SSH access (internal use)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			userPubkey := args[0]

			// Get the original SSH command
			sshCmd := os.Getenv("SSH_ORIGINAL_COMMAND")
			if sshCmd == "" {
				return fmt.Errorf("no SSH command provided")
			}

			// Parse git command: "git-upload-pack 'owner/repo.git'" or "git-receive-pack 'owner/repo.git'"
			gitCmd, repoPath, err := parseGitCommand(sshCmd)
			if err != nil {
				return err
			}

			// Extract owner and repo name from path
			owner, repoName, err := parseRepoPath(repoPath)
			if err != nil {
				return err
			}

			// Load config
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Check repo exists
			repoStore := repo.NewStore(cfg.Server.DataDir)
			r, err := repoStore.Get(owner, repoName)
			if err != nil {
				return fmt.Errorf("failed to get repo: %w", err)
			}
			if r == nil {
				return fmt.Errorf("repository not found: %s/%s", owner, repoName)
			}

			// Check permissions
			// For now: only owner can push (git-receive-pack), anyone can pull (git-upload-pack)
			if gitCmd == "git-receive-pack" && owner != userPubkey {
				return fmt.Errorf("permission denied: you can only push to your own repositories")
			}

			// Execute the git command
			gitExec := exec.Command(gitCmd, r.Path)
			gitExec.Stdin = os.Stdin
			gitExec.Stdout = os.Stdout
			gitExec.Stderr = os.Stderr

			return gitExec.Run()
		},
	}
}

// parseGitCommand parses SSH_ORIGINAL_COMMAND like "git-upload-pack 'owner/repo.git'"
func parseGitCommand(sshCmd string) (gitCmd, repoPath string, err error) {
	// Allowed commands
	allowedCmds := map[string]bool{
		"git-upload-pack":    true, // fetch/pull
		"git-receive-pack":   true, // push
		"git-upload-archive": true, // archive
	}

	parts := strings.SplitN(sshCmd, " ", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid git command")
	}

	gitCmd = parts[0]
	if !allowedCmds[gitCmd] {
		return "", "", fmt.Errorf("command not allowed: %s", gitCmd)
	}

	// Extract repo path (may be quoted)
	repoPath = strings.Trim(parts[1], "'\"")

	return gitCmd, repoPath, nil
}

// parseRepoPath extracts owner and repo name from "owner/repo.git"
func parseRepoPath(path string) (owner, name string, err error) {
	// Remove .git suffix if present
	path = strings.TrimSuffix(path, ".git")

	// Remove leading slash if present
	path = strings.TrimPrefix(path, "/")

	// Split into owner/name
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo path: %s (expected owner/name)", path)
	}

	return parts[0], parts[1], nil
}

// getRepoFullPath returns the full filesystem path for a repo
func getRepoFullPath(cfg *config.Config, owner, name string) string {
	return filepath.Join(cfg.Server.DataDir, "repos", owner, name+".git")
}
