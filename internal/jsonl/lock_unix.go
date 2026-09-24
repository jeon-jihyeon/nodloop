//go:build unix

package jsonl

import (
	"os"
	"syscall"
)

// Held until the file is closed so concurrent appenders never interleave a record
func lock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}
