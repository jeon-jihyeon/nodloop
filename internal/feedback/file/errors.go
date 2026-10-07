package file

import "errors"

// Every jsonl and OS failure reaches a caller as one of these with the cause wrapped behind it
var (
	ErrOpen   = errors.New("feedback file: record directory unusable")
	ErrAppend = errors.New("feedback file: append failed")
	ErrRead   = errors.New("feedback file: read failed")
	// Lines that fail to decode
	// The read returns the records of the other lines with it
	ErrCorrupt = errors.New("feedback file: corrupt lines")
	ErrRepair  = errors.New("feedback file: repair failed")
)

// A read that left out corrupt lines
// 1. it reads as the jsonl error alone since that names the file and each line once
// 2. errors.Is still matches ErrRead and ErrCorrupt and the jsonl error behind them
type corruptError struct {
	err error
}

func (e corruptError) Error() string {
	return e.err.Error()
}

func (e corruptError) Unwrap() []error {
	return []error{ErrRead, ErrCorrupt, e.err}
}
