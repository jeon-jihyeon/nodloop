package compact

import "errors"

var (
	// No evidence event of the old items has an expected status so a replay could check nothing
	ErrNothingToReplay = errors.New("compact: no evidence event has an expected status")
	// The model answered outside the draft schema
	ErrDraftInvalid = errors.New("compact: draft does not match the schema")
	// A replay names an event that no old item came from
	ErrEventOutsideReplay = errors.New("compact: event is not a replay event of the compaction")
	// A folder of the data review needs the data source a server on records alone has not
	ErrNoData = errors.New("compact: a folder of the data review needs a data directory")
	// A compaction of run items is approved only after a coverage check
	ErrNoCoverage = errors.New("compact: the compaction has no coverage check")
	// A coverage names an item outside the compaction
	ErrCoverageInvalid = errors.New("compact: coverage does not match the compaction")
)
