package evidence

import "errors"

var (
	ErrNotFound       = errors.New("evidence: not found")
	ErrUnknownContext = errors.New("evidence: unknown change context")
	ErrMalformed      = errors.New("evidence: malformed data")
)
