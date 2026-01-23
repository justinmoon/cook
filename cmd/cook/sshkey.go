package main

import (
	"fmt"
	"path/filepath"
	"os"

	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/db"
	"github.com/spf13/cobra"
)

// defaultSSHKeyPath returns the default SSH public key path
func defaultSSHKeyPath() string {
	home, _ := os.UserHomeDir()
	// Standard location for ed25519 public keys
	return filepath.Join(home, ".ssh", "id_"+"ed25519.pub")
}

func newSSHKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh-key",
		Short: "Manage SSH keys",
	}

	cmd.AddCommand(newSSHKeyAddCmd())
	cmd.AddCommand(newSSHKeyListCmd())
	cmd.AddCommand(newSSHKeyRemoveCmd())

	return cmd
}

func newSSHKeyAddCmd() *cobra.Command {
	var name string
	var pubkey string

	cmd := &cobra.Command{
		Use:   "add [key-file]",
		Short: "Add an SSH public key",
		Long: `Add an SSH public key for git access.

If no key file is specified, uses the default public key location.
The --pubkey flag specifies which nostr identity owns this key.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			keyFile := defaultSSHKeyPath()
			if len(args) > 0 {
				keyFile = args[0]
			}

			if pubkey == "" {
				return fmt.Errorf("--pubkey is required (your nostr hex pubkey)")
			}

			if !auth.IsValidPubkey(pubkey) {
				// Try to convert from npub
				hex, err := auth.NpubToPubkey(pubkey)
				if err != nil {
					return fmt.Errorf("invalid pubkey: must be hex or npub format")
				}
				pubkey = hex
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

			// Read SSH key from file
			sshKey, err := auth.ReadSSHKeyFile(keyFile)
			if err != nil {
				return fmt.Errorf("failed to read key file: %w", err)
			}

			store := auth.NewSSHKeyStore(database, cfg.Server.DataDir)
			key, err := store.Add(pubkey, sshKey, name)
			if err != nil {
				return err
			}

			fmt.Printf("Added SSH key: %s\n", key.Fingerprint)
			if key.Name != "" {
				fmt.Printf("  Name: %s\n", key.Name)
			}
			fmt.Printf("  Owner: %s\n", auth.ShortPubkey(key.Pubkey))
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Label for this key (e.g., 'laptop', 'work')")
	cmd.Flags().StringVar(&pubkey, "pubkey", "", "Nostr pubkey (hex or npub) that owns this key")
	cmd.MarkFlagRequired("pubkey")

	return cmd
}

func newSSHKeyListCmd() *cobra.Command {
	var pubkey string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List SSH keys",
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

			// Convert npub to hex if needed
			if pubkey != "" && !auth.IsValidPubkey(pubkey) {
				hex, err := auth.NpubToPubkey(pubkey)
				if err != nil {
					return fmt.Errorf("invalid pubkey: must be hex or npub format")
				}
				pubkey = hex
			}

			store := auth.NewSSHKeyStore(database, cfg.Server.DataDir)
			keys, err := store.List(pubkey)
			if err != nil {
				return err
			}

			if len(keys) == 0 {
				fmt.Println("No SSH keys found.")
				return nil
			}

			for _, key := range keys {
				name := key.Name
				if name == "" {
					name = "(unnamed)"
				}
				fmt.Printf("%s  %s  %s  %s\n",
					key.Fingerprint,
					name,
					auth.ShortPubkey(key.Pubkey),
					key.CreatedAt.Format("2006-01-02"),
				)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&pubkey, "pubkey", "", "Filter by owner pubkey")

	return cmd
}

func newSSHKeyRemoveCmd() *cobra.Command {
	var pubkey string

	cmd := &cobra.Command{
		Use:   "remove <fingerprint>",
		Short: "Remove an SSH key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fingerprint := args[0]

			if pubkey == "" {
				return fmt.Errorf("--pubkey is required")
			}

			if !auth.IsValidPubkey(pubkey) {
				hex, err := auth.NpubToPubkey(pubkey)
				if err != nil {
					return fmt.Errorf("invalid pubkey: must be hex or npub format")
				}
				pubkey = hex
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

			store := auth.NewSSHKeyStore(database, cfg.Server.DataDir)
			if err := store.Remove(pubkey, fingerprint); err != nil {
				return err
			}

			fmt.Printf("Removed SSH key: %s\n", fingerprint)
			return nil
		},
	}

	cmd.Flags().StringVar(&pubkey, "pubkey", "", "Owner pubkey (required)")
	cmd.MarkFlagRequired("pubkey")

	return cmd
}
