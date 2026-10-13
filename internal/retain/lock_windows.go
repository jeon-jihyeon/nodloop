//go:build windows

package retain

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Whether the lock on the first byte was taken without waiting
// Held until the file is closed
func tryLock(file *os.File) (bool, error) {
	var overlap windows.Overlapped
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &overlap)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
