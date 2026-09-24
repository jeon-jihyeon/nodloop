package file

import "errors"

// Every jsonl and OS failure reaches a caller as one of these with the cause wrapped behind it
var (
	ErrOpen   = errors.New("knowledge file: record directory unusable")
	ErrAppend = errors.New("knowledge file: append failed")
	ErrRead   = errors.New("knowledge file: read failed")
)
