package mcp

import "errors"

// A proposal from a run teaches what a person corrected so the run needs an edit or a reject
var ErrRunNotCorrected = errors.New("mcp: propose from a run needs a person's edit or reject on it")
