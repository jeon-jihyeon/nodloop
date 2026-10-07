package userconfig

import "errors"

var (
	ErrInvalid = errors.New(File + " is not valid JSON")
	// A relative path names other records in every working directory
	ErrRecordDirRelative = errors.New("the record directory must be an absolute path")
	ErrLock              = errors.New("failed to lock " + File)
)
