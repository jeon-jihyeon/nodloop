package schema

import "errors"

var (
	ErrIndexInvalid   = errors.New("schema: a published file does not decode")
	ErrKindUnknown    = errors.New("schema: unknown record kind")
	ErrVersionUnknown = errors.New("schema: no file for the schema version")
	ErrStoreUnknown   = errors.New("schema: unknown record store")
	ErrRecordInvalid  = errors.New("schema: the record is no JSON object of its store")
	ErrInvalid        = errors.New("schema: the record does not match its published schema")
	ErrCursorInvalid  = errors.New("schema: the cursor names no record")
)
