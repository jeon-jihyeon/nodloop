package pg

import "errors"

var (
	ErrOpen   = errors.New("postgres: open failed")
	ErrAppend = errors.New("postgres: append failed")
	ErrRead   = errors.New("postgres: read failed")
	ErrWrite  = errors.New("postgres: write failed")
)
