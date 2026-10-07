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
)

// What every command reads and writes
type config struct {
	recordDir   string
	home        homeDir
	classifiers classifierConfig
	holdout     holdout
	// session_mode of config.json as written, checked where a hook reads it
	sessionMode string
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
	return config{recordDir: records, home: h, classifiers: uc.classifierConfig, holdout: holdout(uc.Holdout), sessionMode: uc.SessionMode}, nil
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

// config approver, holdout and session_mode print the saved value and save the value given after them
// config alone lists every setting with its value and where the value came from
func runConfig(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "config", errHomeUnknown)
	}
	cmd := configCommand{home: h, out: stdout}
	if len(args) == 0 {
		if err := cmd.list(getenv); err != nil {
			return fail(stderr, "config", err)
		}
		return 0
	}
	value := strings.TrimSpace(strings.Join(args[1:], " "))
	var err error
	switch args[0] {
	case "approver":
		err = cmd.approver(value)
	case "holdout":
		err = cmd.holdout(value)
	case "session_mode":
		err = cmd.sessionMode(value)
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

// One line per setting: its name, its value and where the value came from
// 1. an environment variable wins over config.json and config.json over the default
// 2. classifiers show the points they are set for
func (c configCommand) list(getenv func(string) string) error {
	uc, err := c.home.readConfig()
	if err != nil {
		return err
	}
	records, err := recordDirOf("", getenv(envRecordDir), uc.RecordDir, c.home.recordDir())
	if err != nil {
		return err
	}
	mode, err := sessionModeOf(getenv(envSession), uc.SessionMode)
	if err != nil {
		return err
	}
	holdout := ""
	if uc.Holdout != 0 {
		holdout = strconv.FormatFloat(uc.Holdout, 'g', -1, 64)
	}
	points := "unreadable, run nodloop classifier list"
	if endpoints, err := uc.endpoints(); err == nil {
		var set []string
		for _, p := range classify.Points() {
			if _, ok := endpoints[p]; ok {
				set = append(set, string(p))
			}
		}
		points = strings.Join(set, ",")
	}
	rows := []struct{ name, env, configured, value string }{
		{"record_dir", envRecordDir, uc.RecordDir, records},
		{"session_mode", envSession, uc.SessionMode, string(mode)},
		{"approver", "", uc.Approver, uc.Approver},
		{"holdout", "", holdout, cmp.Or(holdout, "0")},
		{"classifiers", "", points, ""},
		{"claude binary", envClaudeBin, "", cmp.Or(getenv(envClaudeBin), "claude")},
		{"model", envLLMModel, "", cmp.Or(getenv(envLLMModel), "sonnet")},
	}
	for _, r := range rows {
		from := "default"
		switch {
		case r.env != "" && getenv(r.env) != "":
			from = r.env
		case r.configured != "":
			from = configFile
		}
		fmt.Fprintf(c.out, "%s\t%s\t%s\n", r.name, cmp.Or(r.value, r.configured, "-"), from)
	}
	return nil
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

// Prints the saved mode or deferred, or saves the one given
func (c configCommand) sessionMode(value string) error {
	if value == "" {
		uc, err := c.home.readConfig()
		if err != nil {
			return err
		}
		mode, err := sessionModeOf("", uc.SessionMode)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.out, mode)
		return nil
	}
	if !sessionMode(value).valid() {
		return fmt.Errorf("%w: %q. %s", errSessionModeUnknown, value, sessionModeHint)
	}
	if err := c.home.save("session_mode", value); err != nil {
		return err
	}
	fmt.Fprintln(c.out, value)
	return nil
}
