package compact

import "errors"

var (
	// No evidence event of the old items has an expected status so a replay could check nothing
	ErrNothingToReplay = errors.New("compact: no evidence event has an expected status")
	// The model answered outside the draft schema
	ErrDraftInvalid = errors.New("compact: draft does not match the schema")
	// A replay names an event that no old item came from
	ErrEventOutsideReplay = errors.New("compact: event is not a replay event of the compaction")
)
