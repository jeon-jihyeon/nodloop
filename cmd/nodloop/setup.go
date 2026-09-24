package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
)

const configFile = "config.json"

// Written by setup and read when NODLOOP_FILE_DIR is unset
// The plugin's MCP server has no way to receive the user's directory otherwise
type userConfig struct {
	DataDir   string `json:"file_dir"`
	RecordDir string `json:"record_dir,omitempty"`
}

// nodloop keeps the setup config and the demo set and the default records under `.nodloop` there
type homeDir string

func (h homeDir) dir() string {
	return filepath.Join(string(h), ".nodloop")
}

func (h homeDir) demoDir() string {
	return filepath.Join(h.dir(), "demo")
}

func (h homeDir) recordDir() string {
	return filepath.Join(h.dir(), "records")
}

func (h homeDir) configPath() string {
	return filepath.Join(h.dir(), configFile)
}

// An unknown home has no config
func (h homeDir) configured() bool {
	if h == "" {
		return false
	}
	_, err := os.Stat(h.configPath())
	return err == nil
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
	return userConfig{DataDir: abs, RecordDir: recordDir}, nil
}

func (h homeDir) setup(dataDir, recordDir string) (userConfig, error) {
	uc, err := newUserConfig(dataDir, recordDir)
	if err != nil {
		return userConfig{}, err
	}
	if err := os.MkdirAll(h.dir(), 0o755); err != nil {
		return userConfig{}, err
	}
	b, err := json.MarshalIndent(uc, "", "  ")
	if err != nil {
		return userConfig{}, err
	}
	if err := os.WriteFile(h.configPath(), append(b, '\n'), 0o600); err != nil {
		return userConfig{}, err
	}
	return uc, nil
}

// The demo set is unpacked under home and becomes the data directory
func (h homeDir) setupDemo(recordDir string) (userConfig, error) {
	if err := evidencefile.WriteDemo(h.demoDir()); err != nil {
		return userConfig{}, err
	}
	return h.setup(h.demoDir(), recordDir)
}

func runSetup(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "reference data directory with events.csv and runbooks")
	recordDir := fs.String("record-dir", "", "record directory. Empty means ~/.nodloop/records")
	useDemo := fs.Bool("demo", false, "unpack the demo data set under ~/.nodloop/demo and use it")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "setup", fmt.Errorf("%w: HOME is not set", errHomeUnknown))
	}
	cmd := setupCommand{home: h, out: stdout}
	var err error
	switch {
	case *useDemo:
		err = cmd.demo(*recordDir)
	case *dataDir != "":
		err = cmd.data(*dataDir, *recordDir)
	default:
		err = fmt.Errorf("--data-dir or --demo %w", errRequired)
	}
	if err != nil {
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

func (c setupCommand) demo(recordDir string) error {
	uc, err := c.home.setupDemo(recordDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "data %s\nconfig %s\n", uc.DataDir, c.home.configPath())
	fmt.Fprintln(c.out, "demo events tq-001 to tq-024. Try: review event tq-023")
	return nil
}
