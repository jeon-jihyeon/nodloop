package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Refuses anything but a regular file of at most limit bytes with its links followed
func Check(path string, limit int64) error {
	info, err := regular(path)
	if err != nil {
		return err
	}
	if info.Size() > limit {
		return fmt.Errorf("%w: %s holds %d bytes past %d", ErrTooLarge, path, info.Size(), limit)
	}
	return nil
}

// The whole content of a regular file of at most limit bytes with its links followed
// The read stops one byte past the limit so a file that grows while it is read never fills memory
func Read(path string, limit int64) ([]byte, error) {
	if _, err := regular(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%w: %s holds more than %d bytes", ErrTooLarge, path, limit)
	}
	return b, nil
}

// The stat of a regular file
// A fifo, a device or a folder is refused by its mode before anything opens it so no read waits on a writer
func regular(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	return info, nil
}
