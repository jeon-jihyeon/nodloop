package mcp

import "errors"

// A detail start or end that is not RFC3339
var ErrTimeInvalid = errors.New("mcp: time is not RFC3339")

// A proposal from a run teaches what a person corrected so the run needs an edit or a reject
var ErrRunNotCorrected = errors.New("mcp: propose from a run needs a person's edit or reject on it")

// An edit is either a corrected review or a corrected run output
var ErrEditedTwice = errors.New("mcp: give edited for a review or edited_output for a run, not both")
