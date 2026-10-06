package main

import (
	"cmp"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
)

const (
	envRecordDir = "NODLOOP_RECORD_DIR"
	envClaudeBin = "NODLOOP_CLAUDE_BIN"
	envLLMModel  = "NODLOOP_LLM_MODEL"
	// Set by the plugin launcher to the version it runs this binary for
	envPluginVersion = "NODLOOP_PLUGIN_VERSION"
	// A PostgreSQL URL nodloop server serve keeps the records of every tenant in
	envPostgres = "NODLOOP_POSTGRES"
)

// What every command reads and writes
type config struct {
	recordDir   string
	home        homeDir
	classifiers classify.Endpoints
	decisions   classify.Decisions
	holdout     holdout
}

// The record directory as the flag, then NODLOOP_RECORD_DIR, then config.json, then the default under home
func resolveConfig(getenv func(string) string, recordDir string) (config, error) {
	var uc userConfig
	var homeRecords string
	h := homeDir(getenv("HOME"))
	if h != "" {
		var err error
		if uc, err = h.readConfig(); err != nil {
			return config{}, err
		}
		homeRecords = h.recordDir()
	}
	records, err := recordDirOf(recordDir, getenv(envRecordDir), uc.RecordDir, homeRecords)
	if err != nil {
		return config{}, err
	}
	return config{recordDir: records, home: h, classifiers: uc.Classifiers, decisions: uc.Decisions, holdout: holdout(uc.Holdout)}, nil
}

// The record flag a command pasted into another shell needs to read these records
func (c config) recordArgs() string {
	if c.recordDir == "" {
		return ""
	}
	return "--record-dir " + shellWord(c.recordDir)
}

// The record directory
// 1. the flag wins, then the env, then the saved config, then the default under home
// 2. a relative env or config value names other records in every working directory so it is refused
// 3. empty when nothing names one and no home is known
func recordDirOf(flag, env, configured, fallback string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	if env != "" && !filepath.IsAbs(env) {
		return "", fmt.Errorf("%w: %s is %q. Set it to an absolute path or unset it", errRecordDirRelative, envRecordDir, env)
	}
	if env == "" && configured != "" && !filepath.IsAbs(configured) {
		return "", fmt.Errorf("%w: record_dir in %s is %q. Set it to an absolute path", errRecordDirRelative, configFile, configured)
	}
	if dir := cmp.Or(env, configured, fallback); dir != "" {
		return filepath.Clean(dir), nil
	}
	return "", nil
}

// config approver and config holdout print the saved value and save the value given after them
func runConfig(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "config", errNoAction)
	}
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "config", errHomeUnknown)
	}
	cmd := configCommand{home: h, out: stdout}
	value := strings.TrimSpace(strings.Join(args[1:], " "))
	var err error
	switch args[0] {
	case "approver":
		err = cmd.approver(value)
	case "holdout":
		err = cmd.holdout(value)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[0])
	}
	if err != nil {
		return fail(stderr, "config", err)
	}
	return 0
}

type configCommand struct {
	home homeDir
	out  io.Writer
}

// Prints the saved name or saves the one given
func (c configCommand) approver(name string) error {
	if name != "" {
		if err := c.home.save("approver", name); err != nil {
			return err
		}
		fmt.Fprintln(c.out, name)
		return nil
	}
	uc, err := c.home.readConfig()
	if err != nil {
		return err
	}
	if uc.Approver != "" {
		fmt.Fprintln(c.out, uc.Approver)
	}
	return nil
}

// Prints the saved share or saves the one given
// A share of 1 or more would withhold every item so it is refused
func (c configCommand) holdout(value string) error {
	if value == "" {
		uc, err := c.home.readConfig()
		if err != nil {
			return err
		}
		fmt.Fprintln(c.out, strconv.FormatFloat(uc.Holdout, 'g', -1, 64))
		return nil
	}
	share, err := strconv.ParseFloat(value, 64)
	if err != nil || share < 0 || share >= 1 {
		return fmt.Errorf("%w: %q. Give a share from 0 up to but not including 1, such as 0.1", errHoldoutInvalid, value)
	}
	if err := c.home.save("holdout", share); err != nil {
		return err
	}
	fmt.Fprintln(c.out, strconv.FormatFloat(share, 'g', -1, 64))
	return nil
}
