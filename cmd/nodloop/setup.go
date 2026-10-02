package main

import (
	"cmp"
	"context"
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

// The link the plugin launcher points at the binary it runs before every run
func (h homeDir) stableBinary() string {
	return filepath.Join(h.dir(), "bin", "nodloop")
}

// Claude Code `settings.json` that holds the guard hook
func (h homeDir) settingsPath() string {
	return filepath.Join(string(h), ".claude", "settings.json")
}

func (h homeDir) configPath() string {
	return filepath.Join(h.dir(), configFile)
}

// A directory as stored or given
// Paths stay as written so the printed and saved dirs never change spelling
type dirPath string

// Whether both paths name one directory
// 1. a path through a symlink names the directory the link points at
// 2. a path spelled in another case names the same directory on a file system that ignores case
// 3. a path that cannot be read names none so a data dir removed since the last setup still counts as another
func (p dirPath) sameAs(other string) bool {
	if string(p) == other {
		return true
	}
	a, err := os.Stat(string(p))
	if err != nil {
		return false
	}
	b, err := os.Stat(other)
	return err == nil && os.SameFile(a, b)
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
	return userConfig{DataDir: abs, RecordDir: recordDir}, nil
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
	recordDir := fs.String("record-dir", "", "record directory. Empty keeps the one saved before and otherwise means ~/.nodloop/records")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "setup", fmt.Errorf("%w: HOME is not set", errHomeUnknown))
	}
	cmd := setupCommand{
		home: h, fileEnv: getenv(envFileDir), recordEnv: getenv(envRecordDir),
		version: pluginVersion(getenv(envPluginVersion)).mismatch(buildVersion(), executable()), out: stdout, log: stderr,
	}
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
	// NODLOOP_FILE_DIR that wins over the saved data dir in every later command
	fileEnv string
	// NODLOOP_RECORD_DIR that wins over the saved record dir in every later command
	recordEnv string
	// The sentence on a binary of another version than the plugin runs
	// Empty when they match or outside the plugin
	version  string
	out, log io.Writer
}

// Prints the data and the records every later command uses so a changed or shadowed dir is never silent
// Every check runs before the config is written so a failed setup changes nothing
func (c setupCommand) data(dataDir, recordDir string) error {
	uc, report, err := c.prepare(context.Background(), dataDir, recordDir)
	if err != nil {
		return err
	}
	if err := c.home.save(uc); err != nil {
		return err
	}
	fmt.Fprintf(c.out, "data %s\nrecords %s\nconfig %s\n", report.data, report.records, c.home.configPath())
	if c.version != "" {
		report.warnings = append(report.warnings, c.version)
	}
	for _, w := range report.warnings {
		fmt.Fprintf(c.log, "nodloop setup: warning: %s\n", w)
	}
	return nil
}

// What a setup would save and use
type setupReport struct {
	// The data dir every later command reads
	data string
	// The record dir every later command writes
	records  string
	warnings []string
}

// The config to save and what to report without writing anything
// 1. an empty record dir keeps the one saved before so rerunning setup with the data dir alone never drops the records
// 2. a broken config fails unless --record-dir names the records since the record dir saved in it cannot be read
// 3. NODLOOP_FILE_DIR naming another directory warns because every command started with it reviews that dir
// 4. a data dir naming another directory over the records in use before warns when they hold files because their knowledge and corrections carry into its reviews
// 5. two spellings of one directory are one dir so a symlinked path never splits the records
// 6. the warnings of check follow
func (c setupCommand) prepare(ctx context.Context, dataDir, recordDir string) (userConfig, setupReport, error) {
	prior, err := c.prior(recordDir)
	if err != nil {
		return userConfig{}, setupReport{}, err
	}
	uc, err := newUserConfig(dataDir, cmp.Or(recordDir, prior.RecordDir))
	if err != nil {
		return userConfig{}, setupReport{}, err
	}
	// 1. a policy the server could not load fails here so rerunning setup never reports success on it
	// 2. a name no event carries fails here too so a misspelled name never leaves reviews without their numbers
	// 3. the server reports that name in every review instead because it cannot tell a typo from an outage
	// 4. a contexts.csv value or procedure scope the policy does not declare fails here as check fails it
	data, err := dataCheck{dir: uc.DataDir}.run(ctx)
	if err != nil {
		return userConfig{}, setupReport{}, err
	}
	var report setupReport
	if report.records, err = recordDirOf("", c.recordEnv, uc.RecordDir, c.home.recordDir()); err != nil {
		return userConfig{}, setupReport{}, err
	}
	// The variable is taken as is by every later command so it resolves against the working directory like theirs
	if report.data, err = filepath.Abs(cmp.Or(c.fileEnv, uc.DataDir)); err != nil {
		return userConfig{}, setupReport{}, err
	}
	if !dirPath(uc.DataDir).sameAs(report.data) {
		report.warnings = append(report.warnings, fmt.Sprintf("%s is %s and wins over the saved data dir so every command started with it reviews %s. "+
			"Unset it to review %s", envFileDir, c.fileEnv, report.data, uc.DataDir))
	}
	report.warnings = append(report.warnings, data.Warnings...)
	if w := c.carryOver(prior, uc.DataDir, report.records); w != "" {
		report.warnings = append(report.warnings, w)
	}
	return uc, report, nil
}

// The config saved before
// A broken one counts as none only when --record-dir names the records
func (c setupCommand) prior(recordDir string) (userConfig, error) {
	uc, err := c.home.readConfig()
	if !errors.Is(err, errConfigInvalid) {
		return uc, err
	}
	if recordDir != "" {
		return userConfig{}, nil
	}
	return userConfig{}, fmt.Errorf("%w. Fix it or run setup again with --record-dir <dir> since the record dir saved in it cannot be read", err)
}

// The warning when the records in use before hold files of another data dir
// Empty when they do not
func (c setupCommand) carryOver(prior userConfig, dataDir, records string) string {
	if prior.DataDir == "" || dirPath(prior.DataDir).sameAs(dataDir) {
		return ""
	}
	// A prior record dir that does not resolve failed every earlier command so no records came from it
	used, err := recordDirOf("", c.recordEnv, prior.RecordDir, c.home.recordDir())
	if err != nil || !dirPath(used).sameAs(records) {
		return ""
	}
	if entries, err := os.ReadDir(records); err != nil || len(entries) == 0 {
		return ""
	}
	return fmt.Sprintf("%s holds the reviews and knowledge of %s and they carry into reviews of %s. "+
		"%s to keep them apart", records, prior.DataDir, dataDir, c.recordHint())
}

// NODLOOP_RECORD_DIR wins over the saved record dir so a new --record-dir alone would not move the records
func (c setupCommand) recordHint() string {
	if c.recordEnv != "" {
		return "Set " + envRecordDir + " to a new dir"
	}
	return "Run setup again with --record-dir <new dir>"
}
