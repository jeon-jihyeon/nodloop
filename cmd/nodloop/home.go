package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const configFile = "config.json"

// Read when no flag or env names the record directory
// A file_dir an older setup saved is read and ignored so its config still loads
type userConfig struct {
	RecordDir string `json:"record_dir,omitempty"`
}

// nodloop keeps the config and the default records under `.nodloop` there
type homeDir string

func (h homeDir) dir() string {
	return filepath.Join(string(h), ".nodloop")
}

func (h homeDir) recordDir() string {
	return filepath.Join(h.dir(), "records")
}

// The link the plugin launcher points at the binary it runs before every run
func (h homeDir) stableBinary() string {
	return filepath.Join(h.dir(), "bin", "nodloop")
}

// What the processes the hooks start print, appended so a failed extraction stays readable
func (h homeDir) hookLog() string {
	return filepath.Join(h.dir(), "hook.log")
}

// Claude Code `settings.json` that holds the guard hook
func (h homeDir) settingsPath() string {
	return filepath.Join(string(h), ".claude", "settings.json")
}

func (h homeDir) configPath() string {
	return filepath.Join(h.dir(), configFile)
}

func (h homeDir) readConfig() (userConfig, error) {
	b, err := os.ReadFile(h.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return userConfig{}, nil
	}
	if err != nil {
		return userConfig{}, err
	}
	var c userConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return userConfig{}, fmt.Errorf("%w: %s: %w", errConfigInvalid, h.configPath(), err)
	}
	return c, nil
}
