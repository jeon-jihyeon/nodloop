package compact

import "errors"

var (
	// The model answered outside the draft schema
	ErrDraftInvalid = errors.New("compact: draft does not match the schema")
	// A compaction is approved only after a coverage check
	ErrNoCoverage = errors.New("compact: the compaction has no coverage check")
	// A coverage names an item outside the compaction
	ErrCoverageInvalid = errors.New("compact: coverage does not match the compaction")
)
