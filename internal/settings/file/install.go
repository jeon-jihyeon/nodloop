// Package file reads and writes the Claude Code settings.json that holds the nodloop hook
package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/settings"
)

// Register the PreToolUse hook in settings.json
// 1. returns false without changes when already registered
// 2. creates the file when missing
// 3. writes a .bak backup first
// 4. returns false with the error when the write fails
func Install(path, exe string) (changed bool, err error) {
	return edit(path, func(d settings.Document) (bool, error) { return d.Install(exe) })
}

// Remove only our hook from settings.json
// 1. returns false without changes when not registered
// 2. writes a .bak backup first
// 3. returns false with the error when the write fails
func Uninstall(path string) (changed bool, err error) {
	return edit(path, func(d settings.Document) (bool, error) { return d.Uninstall(), nil })
}

// The bytes read once are the backup so a file that changed between read and write is never backed up as something else
func edit(path string, change func(settings.Document) (bool, error)) (bool, error) {
	raw, doc, err := load(path)
	if err != nil {
		return false, err
	}
	changed, err := change(doc)
	if err != nil || !changed {
		return false, err
	}
	if err = save(path, raw, doc); err != nil {
		return false, err
	}
	return true, nil
}

// A missing file is an empty document with nil bytes and a blank file is an empty document with its bytes
func load(path string) ([]byte, settings.Document, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, settings.Document{}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	doc := settings.Document{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, doc, nil
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %w", ErrJSONInvalid, path, err)
	}
	return raw, doc, nil
}

// Nil bytes mean the file did not exist and there is nothing to back up
func save(path string, raw []byte, doc settings.Document) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("%w: %w", ErrWrite, err)
	}
	if raw != nil {
		if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
			return fmt.Errorf("%w: %w", ErrBackup, err)
		}
	}
	// A decoded document holds only JSON values so encoding cannot fail
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := replace(path, append(b, '\n')); err != nil {
		return fmt.Errorf("%w: %w", ErrWrite, err)
	}
	return nil
}

// Write to a temp file in the same directory and rename it so a crash never leaves a partial file
// 1. the temp file is synced before the rename so the rename never outlives its content
// 2. the temp file is removed on any failure
func replace(path string, b []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(b); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err = tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
