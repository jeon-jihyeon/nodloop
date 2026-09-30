// Package file finds and loads veto files on disk
package file

import (
	"errors"
	"os"
	"path/filepath"
)

// Standard relative path of a veto file
// Resolved against the project directory and against home for the user
const RelPath = dir + "/vetoes.yaml"

// Find and load the project files from cwd up to the project root and then the user file and every approved file under home
// 1. a missing file is not an error and is left out
// 2. a broken entry is left out and its error names the path while the rest of its file still applies
// 3. a file that fails to read or parse as YAML is left out and its error names the path
// 4. the sources that did load come back with the joined errors so one broken file cannot disable the others
// 5. an empty cwd or home skips its files
// 6. approved files come last so a hand written veto wins on the same id
func Discover(cwd, home string) (Sources, error) {
	paths := projectPaths(cwd, home)
	var errs []error
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

// The veto files of cwd and of every ancestor up to the project root nearest first
// 1. anything at the path other than nothing is kept so a broken file is reported and never skipped
// 2. every project file up the walk applies and a nearer file wins on the same id
func projectPaths(cwd, home string) []string {
	if cwd == "" {
		return nil
	}
	if home != "" {
		home = filepath.Clean(home)
	}
	var paths []string
	for _, dir := range projectDirs(filepath.Clean(cwd), home) {
		path := filepath.Join(dir, RelPath)
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			paths = append(paths, path)
		}
	}
	return paths
}

// cwd and its ancestors up to the project root nearest first
// 1. the hook cwd follows every cd so a project file must apply below its own directory too
// 2. the nearest directory holding .git is the root and is read even when .git is a file as in a worktree
// 3. the walk stops at home without reading it because the file there is the user file
// 4. home is checked first so a home under version control never reads the user file as a project file
// 5. with neither up to the filesystem root only cwd is read because the directories above belong to no project and may be shared
func projectDirs(cwd, home string) []string {
	var dirs []string
	for dir := cwd; dir != home; {
		dirs = append(dirs, dir)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dirs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dirs[:1]
		}
		dir = parent
	}
	return dirs
}
