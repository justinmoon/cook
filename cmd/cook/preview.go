package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/justinmoon/cook/internal/config"
	"github.com/spf13/cobra"
)

func newPreviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview <url>",
		Short: "Open a URL in the preview pane",
		Long: `Open a URL in the Cook preview pane for the current branch.

This command is intended to be run from within a branch environment
(e.g., by an AI agent) to display a development server in the preview tab.

Example:
  cook preview http://localhost:3000
  cook preview localhost:5173`,
		Args: cobra.ExactArgs(1),
		RunE: runPreview,
	}
	return cmd
}

func runPreview(cmd *cobra.Command, args []string) error {
	url := args[0]

	// Normalize URL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "http://" + url
	}

	// Get branch info from environment
	// The agent environment should have COOK_BRANCH_REPO and COOK_BRANCH_NAME set
	repoRef := os.Getenv("COOK_BRANCH_REPO")
	branchName := os.Getenv("COOK_BRANCH_NAME")

	if repoRef == "" || branchName == "" {
		return fmt.Errorf("not running in a Cook branch environment (COOK_BRANCH_REPO and COOK_BRANCH_NAME not set)")
	}

	// Parse repo ref (owner/repo)
	parts := strings.SplitN(repoRef, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid COOK_BRANCH_REPO format: %s", repoRef)
	}
	owner := parts[0]
	repo := parts[1]

	// Load config to get server URL
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Call the preview API
	apiURL := fmt.Sprintf("%s/api/v1/branches/%s/%s/%s/preview", cfg.Client.ServerURL, owner, repo, branchName)

	body, _ := json.Marshal(map[string]string{"url": url})
	resp, err := http.Post(apiURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to call preview API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("preview API returned status %d", resp.StatusCode)
	}

	fmt.Printf("Opening %s in preview pane\n", url)
	return nil
}
