package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// stripANSI removes ANSI escape codes from a string
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

type Config struct {
	Server ServerConfig `toml:"server"`
	Client ClientConfig `toml:"client"`
}

type ServerConfig struct {
	Host           string   `toml:"host"`
	Port           int      `toml:"port"`
	DataDir        string   `toml:"data_dir"`
	DatabaseURL    string   `toml:"database_url"`
	NatsURL        string   `toml:"nats_url"`
	Auth           string   `toml:"auth"`            // "none" or "nostr"
	AllowedPubkeys []string `toml:"allowed_pubkeys"` // empty = allow all, otherwise whitelist
	Owner          string   `toml:"owner"`           // hex pubkey of instance owner (empty = first login claims)
}

type ClientConfig struct {
	ServerURL string `toml:"server_url"`
}

func DefaultConfig() *Config {
	dataDir := "/var/lib/cook"
	if home, err := os.UserHomeDir(); err == nil {
		dataDir = filepath.Join(home, ".local", "share", "cook")
	}

	return &Config{
		Server: ServerConfig{
			Host:           "127.0.0.1",
			Port:           7420,
			DataDir:        dataDir,
			Auth:           "none", // "none" or "nostr"
			AllowedPubkeys: []string{},
		},
		Client: ClientConfig{
			ServerURL: "http://127.0.0.1:7420",
		},
	}
}

// AuthEnabled returns true if authentication is enabled
func (c *Config) AuthEnabled() bool {
	return c.Server.Auth == "nostr"
}

func Load() (*Config, error) {
	cfg := DefaultConfig()

	// Try system config first
	if _, err := os.Stat("/etc/cook/config.toml"); err == nil {
		if _, err := toml.DecodeFile("/etc/cook/config.toml", cfg); err != nil {
			return nil, err
		}
	}

	// Then user config (overrides system)
	home, err := os.UserHomeDir()
	if err == nil {
		userConfig := filepath.Join(home, ".config", "cook", "config.toml")
		if _, err := os.Stat(userConfig); err == nil {
			if _, err := toml.DecodeFile(userConfig, cfg); err != nil {
				return nil, err
			}
		}
	}

	// Environment variable overrides
	if serverURL := os.Getenv("COOK_SERVER"); serverURL != "" {
		cfg.Client.ServerURL = serverURL
	}

	if dataDir := os.Getenv("COOK_DATA_DIR"); dataDir != "" {
		cfg.Server.DataDir = dataDir
	}

	if dbURL := os.Getenv("COOK_DATABASE_URL"); dbURL != "" {
		cfg.Server.DatabaseURL = dbURL
	} else if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		cfg.Server.DatabaseURL = dbURL
	}

	if allowed := os.Getenv("COOK_ALLOWED_PUBKEYS"); allowed != "" {
		cfg.Server.AllowedPubkeys = splitList(allowed)
	}

	if natsURL := os.Getenv("COOK_NATS_URL"); natsURL != "" {
		cfg.Server.NatsURL = natsURL
	}

	if authMode := os.Getenv("COOK_AUTH"); authMode != "" {
		cfg.Server.Auth = authMode
	}

	if host := os.Getenv("COOK_HOST"); host != "" {
		cfg.Server.Host = host
	}

	if portStr := os.Getenv("COOK_PORT"); portStr != "" {
		portStr = stripANSI(portStr) // Handle ANSI codes from colored shell output
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 || port > 65535 {
			return nil, fmt.Errorf("invalid COOK_PORT: %q", portStr)
		}
		cfg.Server.Port = port
		// Keep CLI default aligned unless COOK_SERVER explicitly set.
		if os.Getenv("COOK_SERVER") == "" {
			host := cfg.Server.Host
			if host == "" || host == "0.0.0.0" {
				host = "127.0.0.1"
			}
			cfg.Client.ServerURL = fmt.Sprintf("http://%s:%d", host, port)
		}
	}

	// Finally, data_dir config (for runtime-set values like owner)
	dataDirConfig := filepath.Join(cfg.Server.DataDir, "config.toml")
	if _, err := os.Stat(dataDirConfig); err == nil {
		if _, err := toml.DecodeFile(dataDirConfig, cfg); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

func splitList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (c *Config) EnsureDataDir() error {
	dirs := []string{
		c.Server.DataDir,
		filepath.Join(c.Server.DataDir, "repos"),
		filepath.Join(c.Server.DataDir, "logs"),
		filepath.Join(c.Server.DataDir, "checkouts"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return nil
}

// SetOwner sets the instance owner and persists to config file in data_dir
func (c *Config) SetOwner(pubkey string) error {
	c.Server.Owner = pubkey

	// Write to data_dir/config.toml (not system/user config)
	configPath := filepath.Join(c.Server.DataDir, "config.toml")

	// Read existing config if present
	existingCfg := make(map[string]interface{})
	if _, err := os.Stat(configPath); err == nil {
		if _, err := toml.DecodeFile(configPath, &existingCfg); err != nil {
			return err
		}
	}

	// Update owner in server section
	serverCfg, ok := existingCfg["server"].(map[string]interface{})
	if !ok {
		serverCfg = make(map[string]interface{})
		existingCfg["server"] = serverCfg
	}
	serverCfg["owner"] = pubkey

	// Write back
	f, err := os.Create(configPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewEncoder(f).Encode(existingCfg)
}

// HasOwner returns true if an owner has been set
func (c *Config) HasOwner() bool {
	return c.Server.Owner != ""
}

// IsOwner checks if the given pubkey is the instance owner
func (c *Config) IsOwner(pubkey string) bool {
	return c.Server.Owner == pubkey
}
