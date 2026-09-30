package atomicfile

import "errors"

// Replacing the link or creating its target would both guess which file the user meant
var ErrLinkDangling = errors.New("symlink to a missing file")
