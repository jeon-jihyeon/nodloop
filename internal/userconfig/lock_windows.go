//go:build windows

package userconfig

import (
	"os"

	"golang.org/x/sys/windows"
)

// An exclusive lock on the first byte held until the file is closed
func lock(file *os.File) error {
	var overlap windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlap)
}
