package mcp

import "errors"

// A call that answers a confirm form this server never sent for its payload
var ErrFormUnknown = errors.New("mcp: the call answers no form this server sent")
