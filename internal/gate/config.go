package gate

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type RepoConfig struct {
	Gates []Gate `toml:"gates"`
}

// LoadRepoConfig loads gate configuration from cook.toml in the checkout
func LoadRepoConfig(checkoutPath string) (*RepoConfig, error) {
	configPath := filepath.Join(checkoutPath, "cook.toml")

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// No config file, return empty config
		return &RepoConfig{}, nil
	}

	var config RepoConfig
	if _, err := toml.DecodeFile(configPath, &config); err != nil {
		return nil, err
	}

	return &config, nil
}
