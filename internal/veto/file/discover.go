// Package file finds and loads veto files on disk
package file

import (
	"errors"
	"os"
	"path/filepath"
)

// Standard relative path of a veto file
// Resolved against cwd for the project and against home for the user
const RelPath = ".claude/nodloop/vetoes.yaml"

// Find and load veto files under cwd and home
// 1. a missing file is not an error and is left out
// 2. a file that fails to read or parse is left out and its error names the path
// 3. the sources that did load come back with the joined errors so one broken file cannot disable the other
// 4. an empty home skips the user file
func Discover(cwd, home string) (Sources, error) {
	var sources Sources
	var errs []error
	for _, base := range []string{cwd, home} {
		if base == "" {
			continue
		}
		path := filepath.Join(base, RelPath)
		vetoes, err := Load(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sources = append(sources, Source{Path: path, Vetoes: vetoes})
	}
	return sources, errors.Join(errs...)
}
