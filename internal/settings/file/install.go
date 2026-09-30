// Package file reads and writes the Claude Code settings.json that holds the nodloop hook
package file

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
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

// Whether `settings.json` registers the nodloop hook and whether that hook can block
// 1. a missing file registers nothing
// 2. a hook whose matcher leaves tools out fails with settings.ErrHookNarrow
// 3. a hook whose absolute executable is gone fails with ErrHookMissing because the shell then exits 127 and Claude Code lets the call through
// 4. a bare command name resolves on the PATH of the hook process and is not checked
// 5. disableAllHooks fails with settings.ErrHooksOff because no registered hook runs then
// 6. a hook on another binary in the folder of the stable link fails with ErrHookStale because upgrades move only the link and never rewrite settings.json
// 7. a hook outside that folder and a missing stable link are not compared so a binary the user chose is never called stale
func Installed(path, stable string) (bool, error) {
	_, doc, err := load(path)
	if err != nil {
		return false, err
	}
	hooks := doc.Hooks()
	errs := []error{doc.CheckHooksOn()}
	for _, h := range hooks {
		errs = append(errs, h.CheckMatcher())
		exe := h.Exe()
		if !filepath.IsAbs(exe) {
			continue
		}
		info, err := os.Stat(exe)
		if err != nil || info.IsDir() {
			errs = append(errs, fmt.Errorf("%w: %s", ErrHookMissing, exe))
			continue
		}
		if staleBeside(exe, info, stable) {
			errs = append(errs, fmt.Errorf("%w: %s runs while the stable link runs %s", ErrHookStale, exe, stable))
		}
	}
	return len(hooks) > 0, errors.Join(errs...)
}

// Whether exe sits under the folder of the stable link and is not the binary the link names
func staleBeside(exe string, running os.FileInfo, stable string) bool {
	rel, err := filepath.Rel(filepath.Dir(stable), exe)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	linked, err := os.Stat(stable)
	return err == nil && !os.SameFile(running, linked)
}

// The bytes read once are the backup so a file that changed between read and write is never backed up as something else
// A symlinked settings.json is read and written at its target so the link stays and the backup stays beside the link
func edit(path string, change func(settings.Document) (bool, error)) (bool, error) {
	target, err := atomicfile.Resolve(path)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrRead, err)
	}
	raw, doc, err := load(target)
	if err != nil {
		return false, err
	}
	changed, err := change(doc)
	if err != nil || !changed {
		return false, err
	}
	if err = save(path, target, raw, doc); err != nil {
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
// The backup goes beside path and the document replaces target
func save(path, target string, raw []byte, doc settings.Document) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("%w: %w", ErrWrite, err)
	}
	if raw != nil {
		if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
			return fmt.Errorf("%w: %w", ErrBackup, err)
		}
	}
	// A decoded document holds only JSON values so encoding cannot fail
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := atomicfile.Replace(target, append(b, '\n')); err != nil {
		return fmt.Errorf("%w: %w", ErrWrite, err)
	}
	return nil
}
