package jsonl

import "errors"

var (
	ErrNotDirectory = errors.New("is not a directory")
	// Lines that fail to decode
	// The read returns the records of every other line with it
	ErrCorrupt = errors.New("has corrupt lines")
)
