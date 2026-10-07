// Package userconfig owns `~/.nodloop` where the nodloop CLI and nodloop-server keep config.json and the default records
package userconfig

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
)

const (
	File = "config.json"
	// Named in the error of a relative value
	// Only the commands read the environment and pass its value in
	EnvRecordDir = "NODLOOP_RECORD_DIR"
	lockSuffix   = ".lock"
)

// The home whose `.nodloop` holds the config and the default records
// Empty when no home is known
type Home string

func (h Home) Dir() string {
	return filepath.Join(string(h), ".nodloop")
}

func (h Home) ConfigPath() string {
	return filepath.Join(h.Dir(), File)
}

// The records when nothing else names a directory
func (h Home) Records() string {
	return filepath.Join(h.Dir(), "records")
}

// Decodes config.json into v
// 1. a missing file leaves v as it is
// 2. a key v does not name is ignored so each reader declares only the keys it reads
func (h Home) Read(v any) error {
	b, err := os.ReadFile(h.ConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, h.ConfigPath(), err)
	}
	return nil
}

// Writes the keys of values in one replace and keeps the value of every other key
// A nil value removes its key
func (h Home) Save(values map[string]any) error {
	return h.Update(func(func(string, any) error) (map[string]any, error) { return values, nil })
}

// Writes the keys change returns from the config change read under the same lock
// 1. change decodes the current value of a key through read so a decision on it and its write are one step
// 2. a nil value removes its key and every other key keeps its value
// 3. a lock file beside config.json is held from the read to the rename so the CLI and the server never lose each other's writes
// 4. the file is written again indented with its keys sorted
func (h Home) Update(change func(read func(key string, v any) error) (map[string]any, error)) error {
	if err := os.MkdirAll(h.Dir(), 0o700); err != nil {
		return err
	}
	held, err := os.OpenFile(h.ConfigPath()+lockSuffix, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLock, err)
	}
	defer func() { _ = held.Close() }()
	if err := lock(held); err != nil {
		return fmt.Errorf("%w: %w", ErrLock, err)
	}
	doc := map[string]json.RawMessage{}
	if err := h.Read(&doc); err != nil {
		return err
	}
	read := func(key string, v any) error {
		raw, ok := doc[key]
		if !ok {
			return nil
		}
		if err := json.Unmarshal(raw, v); err != nil {
			return fmt.Errorf("%w: %s in %s: %w", ErrInvalid, key, h.ConfigPath(), err)
		}
		return nil
	}
	values, err := change(read)
	if err != nil {
		return err
	}
	for key, value := range values {
		if value == nil {
			delete(doc, key)
			continue
		}
		if doc[key], err = json.Marshal(value); err != nil {
			return err
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Replace(h.ConfigPath(), append(out, '\n'))
}

// The record directory
// 1. the flag wins, then the env, then record_dir of config.json, then the records under home
// 2. a relative env or config value names other records in every working directory so it is refused
// 3. empty when nothing names one and no home is known
func (h Home) RecordDir(flag, env, configured string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	if env != "" && !filepath.IsAbs(env) {
		return "", fmt.Errorf("%w: %s is %q. Set it to an absolute path or unset it", ErrRecordDirRelative, EnvRecordDir, env)
	}
	if env == "" && configured != "" && !filepath.IsAbs(configured) {
		return "", fmt.Errorf("%w: record_dir in %s is %q. Set it to an absolute path", ErrRecordDirRelative, File, configured)
	}
	fallback := ""
	if h != "" {
		fallback = h.Records()
	}
	if dir := cmp.Or(env, configured, fallback); dir != "" {
		return filepath.Clean(dir), nil
	}
	return "", nil
}
