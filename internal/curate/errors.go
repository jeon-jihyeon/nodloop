package curate

import "errors"

var (
	// A judge request whose state is no case of a change
	ErrCaseInvalid = errors.New("curate: judge state is not a case")
	// A judge answer that does not decode
	ErrAnswerInvalid = errors.New("curate: judge answer is not valid")
	// A few-shot set names a decision the records no longer hold
	ErrShotUnknown = errors.New("curate: few-shot decision not found")
)
