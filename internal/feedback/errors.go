package feedback

import "errors"

var (
	ErrTraceIDRequired      = errors.New("feedback: a trace id is required")
	ErrVerdictUnknown       = errors.New("feedback: verdict must be approve or edit or reject or withdraw")
	ErrEditedInvalid        = errors.New("feedback: the edited output is not valid JSON")
	ErrEditedRequired       = errors.New("feedback: an edit verdict needs the edited output")
	ErrEditedUnexpected     = errors.New("feedback: only an edit verdict carries an edited output")
	ErrResultUnknown        = errors.New("feedback: result must be confirmed or refuted or inconclusive")
	ErrCauseUnexpected      = errors.New("feedback: only a confirmed result names a confirmed cause")
	ErrReasonCodeUnknown    = errors.New("feedback: reason code must be fact or approach or scope or form or other")
	ErrReasonCodeUnexpected = errors.New("feedback: only an edit or reject verdict carries a reason code")
	// A missing name read as a person's verdict before and silently stopped the extraction of an inferred correction
	ErrReviewerRequired = errors.New("feedback: a reviewer is required")
)
