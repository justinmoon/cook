package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/db"
	"github.com/justinmoon/cook/internal/server"
	"github.com/spf13/cobra"
)

var version = "0.1.0"

func main() {
	rootCmd := &cobra.Command{
		Use:   "cook",
		Short: "AI-native software factory",
		Long:  "Cook is an AI-native software factory that manages git repos, coding agents, and CI/merge workflows.",
	}

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("cook version %s\n", version)
		},
	}

	var serveHost string
	var servePort int
	var dataDir string
	var databaseURL string

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the cook server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Override with flags if provided
			if serveHost != "" {
				cfg.Server.Host = serveHost
			}
			if servePort != 0 {
				cfg.Server.Port = servePort
			}
			if dataDir != "" {
				cfg.Server.DataDir = dataDir
			}
			if databaseURL != "" {
				cfg.Server.DatabaseURL = databaseURL
			}

			// Ensure data directories exist
			if err := cfg.EnsureDataDir(); err != nil {
				return fmt.Errorf("failed to create data directories: %w", err)
			}

			if cfg.Server.DatabaseURL == "" {
				return fmt.Errorf("COOK_DATABASE_URL is required")
			}

			// Open database
			database, err := db.Open(cfg.Server.DatabaseURL)
			if err != nil {
				return fmt.Errorf("failed to open database: %w", err)
			}
			defer database.Close()

			fmt.Printf("data directory: %s\n", cfg.Server.DataDir)
			if cfg.Server.NatsURL != "" {
				fmt.Printf("NATS URL: %s\n", cfg.Server.NatsURL)
			}

			// Create and start server
			srv, err := server.New(cfg, database)
			if err != nil {
				return fmt.Errorf("failed to create server: %w", err)
			}

			// Wait for interrupt in goroutine
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			go func() {
				<-sigCh
				fmt.Println("\nshutting down...")
				srv.Shutdown(context.Background())
			}()

			// Start server (blocks until shutdown)
			if err := srv.Start(); err != nil && err.Error() != "http: Server closed" {
				return fmt.Errorf("server error: %w", err)
			}

			return nil
		},
	}

	serveCmd.Flags().StringVar(&serveHost, "host", "", "host to bind (default from config)")
	serveCmd.Flags().IntVar(&servePort, "port", 0, "port to bind (default from config)")
	serveCmd.Flags().StringVar(&dataDir, "data-dir", "", "data directory (default from config)")
	serveCmd.Flags().StringVar(&databaseURL, "database-url", "", "database URL (default from config)")

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(newRepoCmd())
	rootCmd.AddCommand(newTaskCmd())
	rootCmd.AddCommand(newBranchCmd())
	rootCmd.AddCommand(newGateCmd())
	rootCmd.AddCommand(newAgentCmd())
	rootCmd.AddCommand(newSSHKeyCmd())
	rootCmd.AddCommand(newGitShellCmd())
	rootCmd.AddCommand(newLoginCmd())
	rootCmd.AddCommand(newLogoutCmd())
	rootCmd.AddCommand(newWhoamiCmd())
	rootCmd.AddCommand(newPreviewCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
