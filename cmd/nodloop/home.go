package main

import (
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/userconfig"
)

const configFile = userconfig.File

// What `~/.nodloop/config.json` holds
// A file_dir an older setup saved is read and ignored so its config still loads
type userConfig struct {
	RecordDir string `json:"record_dir,omitempty"`
	// The name a person gave to approve under, saved so a review asks for it once
	Approver string `json:"approver,omitempty"`
	// The share of conversations whose prompts receive no item so report effect has a comparison
	Holdout float64 `json:"holdout,omitempty"`
	// Whole days of unjudged and uncited runs a prune keeps and 0 for every run
	Retention int `json:"retention,omitempty"`
	// How a conversation records the verdicts it infers
	SessionMode string `json:"session_mode,omitempty"`
	// The backend every command that calls a model builds its client from
	LLM llm.Config `json:"llm"`
	// Where every export sends and nil when nothing is exported on its own
	OTel *otelConfig `json:"otel,omitempty"`
	// The documents the person designates as direction
	Anchor anchorConfig `json:"anchor"`
	classifierConfig
}

// The anchor key of config.json
type anchorConfig struct {
	// Absolute paths read at check time where the person keeps them
	Files []string `json:"files"`
}

// nodloop keeps the config and the default records under `.nodloop` there
type homeDir string

func (h homeDir) config() userconfig.Home {
	return userconfig.Home(h)
}

func (h homeDir) dir() string {
	return h.config().Dir()
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

func (h homeDir) readConfig() (userConfig, error) {
	var c userConfig
	if err := h.config().Read(&c); err != nil {
		return userConfig{}, err
	}
	return c, nil
}
