package record

import "errors"

// Returned by a trace store Get for an unknown id
var ErrTraceNotFound = errors.New("trace: not found")
