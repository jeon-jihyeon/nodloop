package mcp

import "errors"

// A call that answers a confirm form this server never sent for its payload
var ErrFormUnknown = errors.New("mcp: the call answers no form this server sent")

var ErrDurationInvalid = errors.New("mcp: duration exceeds 9223372036854 milliseconds")
