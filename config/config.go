package config

import (
	"claude-squad/log"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

const ConfigFileName = "config.json"

// GetConfigDir returns the path to the application's configuration directory
func GetConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config home directory: %w", err)
	}
	return filepath.Join(homeDir, ".claude-squad"), nil
}

// Profile is a command for running an agent; the agent is recognized from the
// command's executable.
type Profile struct {
	Name    string `json:"name"`
	Program string `json:"program"`
}

// Config represents the application configuration
type Config struct {
	// BranchPrefix is the prefix used for git branches created by the application.
	BranchPrefix string `json:"branch_prefix"`
	// Profiles are the commands agents run with, such as extra flags.
	Profiles []Profile `json:"profiles,omitempty"`
	// ProjectsDir is where the launcher lists repositories and creates new
	// directories; "~/" is the home directory. Defaults to ~/src.
	ProjectsDir string `json:"projects_dir,omitempty"`
}

// Projects returns ProjectsDir with "~" expanded.
func (c *Config) Projects() string {
	dir := c.ProjectsDir
	if dir == "" {
		dir = "~/src"
	}
	return ExpandHome(dir)
}

// ExpandHome replaces a leading "~" with the home directory.
func ExpandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{BranchPrefix: defaultBranchPrefix()}
}

func defaultBranchPrefix() string {
	u, err := user.Current()
	if err != nil || u == nil || u.Username == "" {
		log.ErrorLog.Printf("failed to get current user: %v", err)
		return "session/"
	}
	return fmt.Sprintf("%s/", strings.ToLower(u.Username))
}

// LoadConfig reads the configuration, creating the default one on first run.
func LoadConfig() *Config {
	configDir, err := GetConfigDir()
	if err != nil {
		log.ErrorLog.Printf("failed to get config directory: %v", err)
		return DefaultConfig()
	}
	return loadConfigFrom(filepath.Join(configDir, ConfigFileName))
}

func loadConfigFrom(path string) *Config {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			defaultCfg := DefaultConfig()
			if saveErr := saveConfigTo(path, defaultCfg); saveErr != nil {
				log.WarningLog.Printf("failed to save default config: %v", saveErr)
			}
			return defaultCfg
		}
		log.WarningLog.Printf("failed to get config file: %v", err)
		return DefaultConfig()
	}

	config := DefaultConfig()
	if err := json.Unmarshal(data, config); err != nil {
		log.ErrorLog.Printf("failed to parse config file: %v", err)
		return DefaultConfig()
	}
	return config
}

func saveConfigTo(path string, config *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}
