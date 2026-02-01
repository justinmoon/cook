package gate

import (
	"os"
	"os/exec"
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

// LoadRepoConfigFromBareRepo loads gate configuration from cook.toml in a bare repo
func LoadRepoConfigFromBareRepo(bareRepoPath string) (*RepoConfig, error) {
	cmd := exec.Command("git", "-C", bareRepoPath, "show", "HEAD:cook.toml")
	output, err := cmd.Output()
	if err != nil {
		// No config file or error reading, return empty config
		return &RepoConfig{}, nil
	}

	var config RepoConfig
	if _, err := toml.Decode(string(output), &config); err != nil {
		return nil, err
	}

	return &config, nil
}
