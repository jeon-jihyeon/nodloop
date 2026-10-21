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
	f, err := Open(path)
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

// The caller closes the returned file
// Validation uses the opened descriptor so a path replacement cannot substitute a device or fifo
func Open(path string) (*os.File, error) {
	if _, err := regular(path); err != nil {
		return nil, err
	}
	f, err := openRead(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("%w: %s", ErrNotRegular, path), f.Close())
	}
	return f, nil
}

// The stat of a regular file
// Only a regular file can pass the mode check before an open
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
