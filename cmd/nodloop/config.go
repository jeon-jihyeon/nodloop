package main

import (
	"cmp"
	"fmt"
	"path/filepath"
)

const (
	envSource    = "NODLOOP_SOURCE"
	envFileDir   = "NODLOOP_FILE_DIR"
	envRecordDir = "NODLOOP_RECORD_DIR"
	envClaudeBin = "NODLOOP_CLAUDE_BIN"
	envLLMModel  = "NODLOOP_LLM_MODEL"
	// Set by the plugin launcher to the version it runs this binary for
	envPluginVersion = "NODLOOP_PLUGIN_VERSION"
)

// The evidence implementation a data command reads
type source string

const sourceFile source = "file" // reference data files in the data directory

// Where the data commands read and write
type config struct {
	// Reference data that nodloop only reads
	dataDir string
	// Traces and feedback and knowledge that nodloop only appends to
	// Empty without a home and then only the commands that write records fail
	recordDir string
	// Where approved vetoes are exported
	// Empty without a home
	home homeDir
}

// For each value the flag wins over the variable and the variable over the setup config
// 1. records default to `~/.nodloop/records` in the CLI and in mcp alike
// 2. never the working directory because a plugin server starts in the plugin folder
// 3. HOME comes from getenv only so a test with an empty environment never reads the real config
// 4. the source is only checked since the file source is the one implementation
// 5. the record dir is one absolute path because it names the records and the approved veto file of every process
func resolveConfig(getenv func(string) string, src, dataDir, recordDir string) (config, error) {
	cfg, err := resolveRecordConfig(getenv, src, dataDir, recordDir)
	if err != nil {
		return config{}, err
	}
	if cfg.dataDir == "" {
		return config{}, fmt.Errorf("%w: run nodloop setup or set the variable", errDataDirUnset)
	}
	return cfg, nil
}

// The config of a command that works on the records alone
// A data dir is resolved when one is set and left empty otherwise, so only a read of the data fails on it
func resolveRecordConfig(getenv func(string) string, src, dataDir, recordDir string) (config, error) {
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
	cfg := config{dataDir: cmp.Or(dataDir, getenv(envFileDir), uc.DataDir), recordDir: records, home: h}
	switch s := source(cmp.Or(src, getenv(envSource), string(sourceFile))); s {
	case sourceFile:
		return cfg, nil
	default:
		return config{}, fmt.Errorf("%w: %q", errUnknownSource, s)
	}
}

// The data flags a command pasted into another shell needs to read these directories
// 1. that shell has neither the flags nor the env of the process that resolved them
// 2. both are absolute because it may start in another folder
// 3. the source is left out since the file source is the one implementation
func (c config) dataArgs() (string, error) {
	data, err := filepath.Abs(c.dataDir)
	if err != nil {
		return "", err
	}
	if c.recordDir == "" {
		return "--data-dir " + shellWord(data), nil
	}
	return "--data-dir " + shellWord(data) + " --record-dir " + shellWord(c.recordDir), nil
}

// The record dir as one clean absolute path
// 1. a flag is relative to the working directory of the command that names it
// 2. the variable and the setup config must be absolute because a server started in another folder would resolve them there
func recordDirOf(flag, env, configured, fallback string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	if env != "" && !filepath.IsAbs(env) {
		return "", fmt.Errorf("%w: %s is %q. Set it to an absolute path or unset it", errRecordDirRelative, envRecordDir, env)
	}
	if env == "" && configured != "" && !filepath.IsAbs(configured) {
		return "", fmt.Errorf("%w: record_dir in %s is %q. Run nodloop setup again or set it to an absolute path",
			errRecordDirRelative, configFile, configured)
	}
	if dir := cmp.Or(env, configured, fallback); dir != "" {
		return filepath.Clean(dir), nil
	}
	return "", nil
}
