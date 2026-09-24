package mcp

import "errors"

// A detail start or end that is not RFC3339
var ErrTimeInvalid = errors.New("mcp: time is not RFC3339")
