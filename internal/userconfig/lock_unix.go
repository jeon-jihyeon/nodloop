//go:build unix

package userconfig

import (
	"os"
	"syscall"
)

// Held until the file is closed
// flock binds to each open so two opens exclude each other even inside one process
func lock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}
