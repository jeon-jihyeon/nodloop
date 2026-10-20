package jsonl

import "errors"

var (
	ErrNotDirectory = errors.New("is not a directory")
	// Lines that fail to decode
	// The read returns the records of every other line with it
	ErrCorrupt = errors.New("has corrupt lines")
	// A position of no line the file holds such as one a rewrite removed
	ErrPositionGone = errors.New("holds no line at the position")
)
