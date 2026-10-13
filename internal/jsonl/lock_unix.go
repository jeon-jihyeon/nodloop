//go:build unix

package jsonl

import (
	"errors"
	"os"
	"syscall"
)

// Held until the file is closed so concurrent appenders never interleave a record
func lock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

// Locks the data file itself as releases before the lock file did for every write
// A process of such a release that still runs thus never appends while a rewrite reads the file
// It can still append to the old file it opened before the rename so every process restarts on one version before a prune
func lockData(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, perms)
	if err != nil {
		return nil, err
	}
	if err := lock(file); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}
