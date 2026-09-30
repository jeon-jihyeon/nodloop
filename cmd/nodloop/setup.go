package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
)

const configFile = "config.json"

// Written by setup and read when NODLOOP_FILE_DIR is unset
// The plugin's MCP server has no way to receive the user's directory otherwise
type userConfig struct {
	DataDir   string `json:"file_dir"`
	RecordDir string `json:"record_dir,omitempty"`
}

// nodloop keeps the setup config and the default records under `.nodloop` there
type homeDir string

func (h homeDir) dir() string {
	return filepath.Join(string(h), ".nodloop")
}

func (h homeDir) recordDir() string {
	return filepath.Join(h.dir(), "records")
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

// Both directories are stored absolute because the plugin's MCP server starts in the plugin directory
func newUserConfig(dataDir, recordDir string) (userConfig, error) {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return userConfig{}, err
	}
	if recordDir != "" {
		if recordDir, err = filepath.Abs(recordDir); err != nil {
			return userConfig{}, err
		}
	}
	if _, err := os.Stat(filepath.Join(abs, "events.csv")); err != nil {
		return userConfig{}, fmt.Errorf("%w: %s", errNoEvents, abs)
	}
	if _, err := os.Stat(filepath.Join(abs, "policy.yaml")); err != nil {
		return userConfig{}, fmt.Errorf("%w: %s", errPolicyMissing, abs)
	}
	return userConfig{DataDir: abs, RecordDir: recordDir}, nil
}

func (h homeDir) setup(dataDir, recordDir string) (userConfig, error) {
	uc, err := newUserConfig(dataDir, recordDir)
	if err != nil {
		return userConfig{}, err
	}
	if err := h.save(uc); err != nil {
		return userConfig{}, err
	}
	return uc, nil
}

func (h homeDir) save(uc userConfig) error {
	if err := os.MkdirAll(h.dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(uc, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Replace(h.configPath(), append(b, '\n'))
}

func runSetup(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "reference data directory with events.csv and policy.yaml and procedures")
	recordDir := fs.String("record-dir", "", "record directory. Empty means ~/.nodloop/records")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "setup", fmt.Errorf("%w: HOME is not set", errHomeUnknown))
	}
	cmd := setupCommand{home: h, out: stdout}
	if *dataDir == "" {
		return fail(stderr, "setup", fmt.Errorf("--data-dir %w", errRequired))
	}
	if err := cmd.data(*dataDir, *recordDir); err != nil {
		return fail(stderr, "setup", err)
	}
	return 0
}

type setupCommand struct {
	home homeDir
	out  io.Writer
}

func (c setupCommand) data(dataDir, recordDir string) error {
	uc, err := c.home.setup(dataDir, recordDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "data %s\nconfig %s\n", uc.DataDir, c.home.configPath())
	return nil
}
