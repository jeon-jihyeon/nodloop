package jsonl

import "errors"

var (
	ErrNotDirectory = errors.New("is not a directory")
	ErrChanged      = errors.New("records changed since they were read")
)
