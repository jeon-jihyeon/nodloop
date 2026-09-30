// Package atomicfile replaces a whole file in one rename and keeps a symlink that points at it
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The file path names after every symlink
// 1. a missing file resolves to itself so it is created there
// 2. a link to a missing file fails with ErrLinkDangling
func Resolve(path string) (string, error) {
	target, err := filepath.EvalSymlinks(path)
	if err == nil {
		return target, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	return "", fmt.Errorf("%w: %s", ErrLinkDangling, path)
}

// Write b to a temp file beside the resolved target and rename it over that target
// 1. a crash never leaves a partial file
// 2. a link to the file stays a link so a file kept in a dotfiles repo gets the new bytes
// 3. the temp file is synced before the rename so the rename never outlives its content
// 4. the temp file is removed on any failure
// 5. the file ends at mode 0600 whatever mode the target had since only its owner reads it
func Replace(path string, b []byte) (err error) {
	target, err := Resolve(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+"-*")
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
	return os.Rename(tmp.Name(), target)
}
