//go:build windows

package jsonl

import (
	"os"

	"golang.org/x/sys/windows"
)

// An exclusive lock on one byte far past the end of the file
// Every writer locks the same byte so writers exclude each other while reads of the data are never blocked
func lock(file *os.File) error {
	overlap := windows.Overlapped{Offset: 0xffffffff, OffsetHigh: 0x7fffffff}
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlap)
}

// No lock on the data file since a handle open on it fails the rename of a rewrite
// No release archive ships for Windows so no process of an earlier release locks it there
func lockData(string) (*os.File, error) {
	return nil, nil
}
