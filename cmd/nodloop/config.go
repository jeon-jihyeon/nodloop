package main

import (
	"cmp"
	"fmt"
	"path/filepath"
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
	recordDir string
	home      homeDir
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
	return config{recordDir: records, home: h}, nil
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
