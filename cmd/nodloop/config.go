package main

import (
	"cmp"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
)

const (
	envRecordDir = "NODLOOP_RECORD_DIR"
	envClaudeBin = "NODLOOP_CLAUDE_BIN"
	envLLMModel  = "NODLOOP_LLM_MODEL"
	// Set by the plugin launcher to the version it runs this binary for
	envPluginVersion = "NODLOOP_PLUGIN_VERSION"
)

// What every command reads and writes
type config struct {
	recordDir   string
	home        homeDir
	classifiers classify.Endpoints
	decisions   classify.Decisions
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
	return config{recordDir: records, home: h, classifiers: uc.Classifiers, decisions: uc.Decisions}, nil
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

// config approver prints the saved name and config approver <name> saves one
func runConfig(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "approver" {
		return fail(stderr, "config", fmt.Errorf("%w %q", errUnknownAction, strings.Join(args, " ")))
	}
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "config", errHomeUnknown)
	}
	if name := strings.TrimSpace(strings.Join(args[1:], " ")); name != "" {
		if err := h.saveApprover(name); err != nil {
			return fail(stderr, "config", err)
		}
		fmt.Fprintln(stdout, name)
		return 0
	}
	uc, err := h.readConfig()
	if err != nil {
		return fail(stderr, "config", err)
	}
	if uc.Approver != "" {
		fmt.Fprintln(stdout, uc.Approver)
	}
	return 0
}
