package config

import (
	"claude-squad/log"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const StateFileName = "state.json"

// recentLimit bounds how many recently opened directories are remembered.
const recentLimit = 8

// State is what persists between runs.
type State struct {
	// Workspace is the open workspace, serialized by the workspace package.
	Workspace json.RawMessage `json:"workspace,omitempty"`
	// Recent lists recently opened directories, newest first.
	Recent []string `json:"recent,omitempty"`

	path string
}

// LoadState loads the state from disk; a missing or unreadable file gives an
// empty state.
func LoadState() *State {
	configDir, err := GetConfigDir()
	if err != nil {
		log.ErrorLog.Printf("failed to get config directory: %v", err)
		return &State{}
	}
	return LoadStateFrom(filepath.Join(configDir, StateFileName))
}

// LoadStateFrom loads the state kept at path.
func LoadStateFrom(path string) *State {
	state := &State{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WarningLog.Printf("failed to read state file: %v", err)
		}
		return state
	}
	if err := json.Unmarshal(data, state); err != nil {
		log.ErrorLog.Printf("failed to parse state file: %v", err)
		return &State{path: path}
	}
	return state
}

// Save writes the state to the file it was loaded from.
func (s *State) Save() error {
	if s.path == "" {
		return fmt.Errorf("state has no file to save to")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}
	return os.WriteFile(s.path, data, 0644)
}

// AddRecent moves dir to the front of the recent directories.
func (s *State) AddRecent(dir string) {
	recent := []string{dir}
	for _, d := range s.Recent {
		if d != dir && len(recent) < recentLimit {
			recent = append(recent, d)
		}
	}
	s.Recent = recent
}
