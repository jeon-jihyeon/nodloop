package main

import (
	"cmp"
	"fmt"
)

const (
	envSource    = "NODLOOP_SOURCE"
	envFileDir   = "NODLOOP_FILE_DIR"
	envRecordDir = "NODLOOP_RECORD_DIR"
	envClaudeBin = "NODLOOP_CLAUDE_BIN"
	envLLMModel  = "NODLOOP_LLM_MODEL"
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
}

// For each value the flag wins over the variable and the variable over the setup config
// 1. records default to `~/.nodloop/records` in the CLI and in mcp alike
// 2. never the working directory because a plugin server starts in the plugin folder
// 3. HOME comes from getenv only so a test with an empty environment never reads the real config
// 4. the source is only checked since the file source is the one implementation
func resolveConfig(getenv func(string) string, src, dataDir, recordDir string) (config, error) {
	var uc userConfig
	var homeRecords string
	if h := homeDir(getenv("HOME")); h != "" {
		var err error
		if uc, err = h.readConfig(); err != nil {
			return config{}, err
		}
		homeRecords = h.recordDir()
	}
	cfg := config{
		dataDir:   cmp.Or(dataDir, getenv(envFileDir), uc.DataDir),
		recordDir: cmp.Or(recordDir, getenv(envRecordDir), uc.RecordDir, homeRecords),
	}
	if cfg.dataDir == "" {
		return config{}, fmt.Errorf("%w: run nodloop setup or set the variable", errDataDirUnset)
	}
	switch s := source(cmp.Or(src, getenv(envSource), string(sourceFile))); s {
	case sourceFile:
		return cfg, nil
	default:
		return config{}, fmt.Errorf("%w: %q", errUnknownSource, s)
	}
}
