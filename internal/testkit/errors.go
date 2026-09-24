package testkit

import "errors"

// A tool answered with an error result
// The server message follows the sentinel since errors do not cross the transport
var ErrTool = errors.New("testkit: tool error")
