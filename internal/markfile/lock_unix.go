//go:build unix

package markfile

import (
	"errors"
	"os"
	"syscall"
)

// Whether the lock was taken without waiting
// Held until the file is closed
func tryLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
