package file

import "errors"

// Every jsonl and OS failure reaches a caller as one of these with the cause wrapped behind it
var (
	ErrOpen   = errors.New("feedback file: record directory unusable")
	ErrAppend = errors.New("feedback file: append failed")
	ErrRead   = errors.New("feedback file: read failed")
)
