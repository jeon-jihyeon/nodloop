package feedback

import "errors"

var (
	ErrTraceIDRequired  = errors.New("feedback: a trace id is required")
	ErrVerdictUnknown   = errors.New("feedback: verdict must be approve or edit or reject")
	ErrEditedInvalid    = errors.New("feedback: the edited review is not valid JSON")
	ErrEditedRequired   = errors.New("feedback: an edit verdict needs the edited review")
	ErrEditedUnexpected = errors.New("feedback: only an edit verdict carries an edited review")
	ErrResultUnknown    = errors.New("feedback: result must be confirmed or refuted or inconclusive")
	ErrCauseUnexpected  = errors.New("feedback: only a confirmed result names a confirmed cause")
)
