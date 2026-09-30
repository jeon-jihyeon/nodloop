package file

import "errors"

// Every OS failure reaches a caller as one of these with the cause wrapped behind it
var (
	ErrRead  = errors.New("failed to read veto file")
	ErrWrite = errors.New("failed to write veto file")
)

var (
	// An older build hashed a relative record directory as it was typed
	ErrOrphan = errors.New("relative so no knowledge status change rewrites or removes it. Delete it by hand")
)
