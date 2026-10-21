//go:build !unix

package atomicfile

import "os"

func openRead(path string) (*os.File, error) {
	return os.Open(path)
}
