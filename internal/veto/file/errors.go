package file

import "errors"

// Every OS failure reaches a caller as this with the cause wrapped behind it
var ErrRead = errors.New("failed to read veto file")
