// Package file finds and loads veto files on disk
package file

import (
	"errors"
	"os"
	"path/filepath"
)

// Standard relative path of a veto file
// Resolved against cwd for the project and against home for the user
const RelPath = dir + "/vetoes.yaml"

// Find and load the project file under cwd and then the user file and every approved file under home
// 1. a missing file is not an error and is left out
// 2. a broken entry is left out and its error names the path while the rest of its file still applies
// 3. a file that fails to read or parse as YAML is left out and its error names the path
// 4. the sources that did load come back with the joined errors so one broken file cannot disable the others
// 5. an empty cwd or home skips its files
// 6. approved files come last so a hand written veto wins on the same id
func Discover(cwd, home string) (Sources, error) {
	var paths []string
	var errs []error
	if cwd != "" {
		paths = append(paths, filepath.Join(cwd, RelPath))
	}
	if home != "" {
		approved, err := approvedPaths(home)
		errs = append(errs, err)
		paths = append(append(paths, filepath.Join(home, RelPath)), approved...)
	}
	var sources Sources
	for _, path := range paths {
		vetoes, err := Load(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		errs = append(errs, err)
		if err == nil || len(vetoes) > 0 {
			sources = append(sources, Source{Path: path, Vetoes: vetoes})
		}
	}
	return sources, errors.Join(errs...)
}
