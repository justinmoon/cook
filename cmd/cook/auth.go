package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/config"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
)

const (
	keychainService = "cook"
	keychainAccount = "nsec"
)

func newLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Login with your nostr nsec",
		Long: `Store your nostr private key (nsec) in the system keychain.

The nsec is stored securely using your operating system's native keychain:
- macOS: Keychain
- Linux: Secret Service (GNOME Keyring, KWallet)
- Windows: Credential Manager

Your nsec never leaves your machine and is only used to sign API requests.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if already logged in
			existing, err := keyring.Get(keychainService, keychainAccount)
			if err == nil && existing != "" {
				fmt.Println("Already logged in. Use 'cook logout' first to switch accounts.")
				return nil
			}

			// Prompt for nsec
			fmt.Print("Enter your nsec: ")
			reader := bufio.NewReader(os.Stdin)
			nsec, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("failed to read input: %w", err)
			}
			nsec = strings.TrimSpace(nsec)

			// Validate nsec format
			if !strings.HasPrefix(nsec, "nsec1") {
				return fmt.Errorf("invalid nsec: must start with 'nsec1'")
			}

			// Decode to verify it's valid
			prefix, data, err := nip19.Decode(nsec)
			if err != nil {
				return fmt.Errorf("invalid nsec: %w", err)
			}
			if prefix != "nsec" {
				return fmt.Errorf("invalid nsec: wrong prefix")
			}

			// Get public key from private key
			privkey := data.(string)
			pubkey, err := auth.GetPubkeyFromPrivkey(privkey)
			if err != nil {
				return fmt.Errorf("failed to derive pubkey: %w", err)
			}

			// Store in keychain
			if err := keyring.Set(keychainService, keychainAccount, nsec); err != nil {
				return fmt.Errorf("failed to store nsec in keychain: %w", err)
			}

			npub, _ := auth.PubkeyToNpub(pubkey)
			fmt.Printf("Logged in as %s\n", npub)
			fmt.Printf("Your nsec is stored securely in the system keychain.\n")
			return nil
		},
	}
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove nsec from keychain",
		RunE: func(cmd *cobra.Command, args []string) error {
			err := keyring.Delete(keychainService, keychainAccount)
			if err != nil {
				if err == keyring.ErrNotFound {
					fmt.Println("Not logged in.")
					return nil
				}
				return fmt.Errorf("failed to remove nsec: %w", err)
			}
			fmt.Println("Logged out. Your nsec has been removed from the keychain.")
			return nil
		},
	}
}

func newWhoamiCmd() *cobra.Command {
	var remote bool

	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show current identity",
		RunE: func(cmd *cobra.Command, args []string) error {
			nsec, err := keyring.Get(keychainService, keychainAccount)
			if err != nil {
				if err == keyring.ErrNotFound {
					fmt.Println("Not logged in. Use 'cook login' first.")
					return nil
				}
				return fmt.Errorf("failed to get nsec: %w", err)
			}

			// Decode nsec to get privkey
			_, data, err := nip19.Decode(nsec)
			if err != nil {
				return fmt.Errorf("invalid stored nsec: %w", err)
			}
			privkey := data.(string)

			pubkey, err := auth.GetPubkeyFromPrivkey(privkey)
			if err != nil {
				return fmt.Errorf("failed to derive pubkey: %w", err)
			}

			npub, _ := auth.PubkeyToNpub(pubkey)

			if remote {
				// Query server for whoami
				cfg, err := config.Load()
				if err != nil {
					return err
				}

				resp, err := makeAuthenticatedRequest(privkey, "GET", cfg.Client.ServerURL+"/api/v1/whoami", nil)
				if err != nil {
					return fmt.Errorf("server request failed: %w", err)
				}

				var result map[string]interface{}
				if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
					return err
				}
				resp.Body.Close()

				fmt.Printf("Pubkey: %s\n", pubkey)
				fmt.Printf("Npub:   %s\n", npub)
				if owner, ok := result["owner"].(bool); ok && owner {
					fmt.Printf("Role:   Instance Owner\n")
				}
			} else {
				fmt.Printf("Pubkey: %s\n", pubkey)
				fmt.Printf("Npub:   %s\n", npub)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&remote, "remote", false, "Query server for additional info")
	return cmd
}

// makeAuthenticatedRequest makes an HTTP request with NIP-98 authentication
func makeAuthenticatedRequest(privkey, method, url string, body []byte) (*http.Response, error) {
	// Create NIP-98 event
	event, err := auth.CreateNIP98Event(privkey, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create auth event: %w", err)
	}

	// Encode as header
	authHeader, err := auth.EncodeNIP98Header(event)
	if err != nil {
		return nil, fmt.Errorf("failed to encode auth header: %w", err)
	}

	// Make request
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", authHeader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return http.DefaultClient.Do(req)
}

// getStoredPrivkey retrieves the private key from keychain
func getStoredPrivkey() (string, error) {
	nsec, err := keyring.Get(keychainService, keychainAccount)
	if err != nil {
		if err == keyring.ErrNotFound {
			return "", fmt.Errorf("not logged in - use 'cook login' first")
		}
		return "", fmt.Errorf("failed to get nsec from keychain: %w", err)
	}

	_, data, err := nip19.Decode(nsec)
	if err != nil {
		return "", fmt.Errorf("invalid stored nsec: %w", err)
	}

	return data.(string), nil
}
