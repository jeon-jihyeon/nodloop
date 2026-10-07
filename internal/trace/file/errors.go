package file

import "errors"

var (
	ErrOpen   = errors.New("trace file: open failed")
	ErrAppend = errors.New("trace file: append failed")
	ErrRead   = errors.New("trace file: read failed")
	// Lines that fail to decode
	// The read returns the records of the other lines with it
	ErrCorrupt = errors.New("trace file: corrupt lines")
	ErrRepair  = errors.New("trace file: repair failed")
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
