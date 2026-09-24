package file

import "errors"

var (
	ErrOpen   = errors.New("trace file: open failed")
	ErrAppend = errors.New("trace file: append failed")
	ErrRead   = errors.New("trace file: read failed")
)
