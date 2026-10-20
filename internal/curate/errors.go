package curate

import "errors"

var (
	// A judge request whose state is no case of a change
	ErrCaseInvalid = errors.New("curate: judge state is not a case")
	// A judge answer that does not decode
	ErrAnswerInvalid = errors.New("curate: judge answer is not valid")
	// A few-shot set names a decision the records no longer hold
	ErrShotUnknown = errors.New("curate: few-shot decision not found")
	// curate.json exists and cannot be read
	ErrMark = errors.New("curate: mark unreadable")
	// A decide whose change, decision or draw does not hold together
	ErrDecideInvalid = errors.New("curate: invalid decide")
	// A knob event outside the boundaries or the ranges of the knobs
	ErrKnobInvalid = errors.New("curate: invalid knob event")
	// An anchor version without its number, its cause or the HMAC of a file
	ErrAnchorInvalid = errors.New("curate: invalid anchor version")
	// A question shown without its kind, its key, its choices or what it asks about
	ErrExposeInvalid = errors.New("curate: invalid expose")
	// An answer whose choice, proof or person does not hold together
	ErrReplyInvalid = errors.New("curate: invalid answer to a question")
	// An intervention that names nothing it can act on or a value outside the range of its knob
	ErrTargetInvalid = errors.New("curate: the intervention names no target it can act on")
)
