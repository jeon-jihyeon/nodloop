package atomicfile

import "errors"

var (
	// Replacing the link or creating its target would both guess which file the user meant
	ErrLinkDangling = errors.New("symlink to a missing file")
	// A fifo could wait for a writer and a device could stream without end
	ErrNotRegular = errors.New("not a regular file")
	ErrTooLarge   = errors.New("file past its size limit")
)
