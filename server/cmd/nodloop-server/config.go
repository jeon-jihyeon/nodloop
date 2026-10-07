package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
)

const configFile = "config.json"

var (
	errHomeUnknown       = errors.New("home directory unknown")
	errConfigInvalid     = errors.New(configFile + " is not valid JSON")
	errRecordDirRelative = errors.New("the record directory must be an absolute path")
	errUnknownAction     = errors.New("unknown action")
	errRequired          = errors.New("is required")
	errKeyExists         = errors.New("a server key of that name exists")
	errKeyUnknown        = errors.New("no server key of that name")
	errTenantInvalid     = errors.New("invalid tenant")
	errRoleInvalid       = errors.New("invalid role")
	errNoKeys            = errors.New("no server key")
)

// The keys of `~/.nodloop/config.json` the server reads
// The nodloop CLI writes the same file so every other key is kept as it is
type fileConfig struct {
	RecordDir string       `json:"record_dir,omitempty"`
	Server    serverConfig `json:"server,omitzero"`
}

// The home whose `.nodloop` holds the config and the default records
type homeDir string

func (h homeDir) configPath() string {
	return filepath.Join(string(h), ".nodloop", configFile)
}

func (h homeDir) readConfig() (fileConfig, error) {
	b, err := os.ReadFile(h.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return fileConfig{}, nil
	}
	if err != nil {
		return fileConfig{}, err
	}
	var c fileConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return fileConfig{}, fmt.Errorf("%w: %s: %w", errConfigInvalid, h.configPath(), err)
	}
	return c, nil
}

// Writes the server key of config.json and keeps the value of every other key
func (h homeDir) saveKeys(keys serverConfig) error {
	doc := map[string]json.RawMessage{}
	b, err := os.ReadFile(h.configPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("%w: %s: %w", errConfigInvalid, h.configPath(), err)
		}
	}
	if doc["server"], err = json.Marshal(keys); err != nil {
		return err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.configPath()), 0o700); err != nil {
		return err
	}
	return atomicfile.Replace(h.configPath(), append(out, '\n'))
}

// The record directory as the flag, then NODLOOP_RECORD_DIR, then config.json, then the default under home
// The same order as the nodloop CLI so both read one directory
func (h homeDir) recordDir(flag, env, configured string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	if env != "" && !filepath.IsAbs(env) {
		return "", fmt.Errorf("%w: %s is %q. Set it to an absolute path or unset it", errRecordDirRelative, envRecordDir, env)
	}
	if env == "" && configured != "" && !filepath.IsAbs(configured) {
		return "", fmt.Errorf("%w: record_dir in %s is %q. Set it to an absolute path", errRecordDirRelative, configFile, configured)
	}
	fallback := ""
	if h != "" {
		fallback = filepath.Join(string(h), ".nodloop", "records")
	}
	if dir := cmp.Or(env, configured, fallback); dir != "" {
		return filepath.Clean(dir), nil
	}
	return "", errHomeUnknown
}
