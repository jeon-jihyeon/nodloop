package atomicfile

import "errors"

var (
	// Replacing the link or creating its target would both guess which file the user meant
	ErrLinkDangling = errors.New("symlink to a missing file")
	// A fifo would hold a read until a writer comes and a device such as /dev/zero never ends
	ErrNotRegular = errors.New("not a regular file")
	ErrTooLarge   = errors.New("file past its size limit")
)
