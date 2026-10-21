//go:build unix

package atomicfile

import (
	"os"
	"syscall"
)

// A fifo substituted before open must not wait for a writer
func openRead(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
