package eval

import "errors"

var (
	// Report on a session with no diagnose trace
	ErrNoTraces         = errors.New("eval: no traces for the session")
	ErrNoLabels         = errors.New("eval: no labels for that half of the set")
	ErrEventOutsideSet  = errors.New("eval: event is not a label of that half")
	ErrUnknownCondition = errors.New("eval: unknown condition")
	ErrSeedConditions   = errors.New("eval: conditions apply to holdout only")
	ErrHoldoutFeedback  = errors.New("eval: holdout trace has feedback")
	ErrHoldoutKnowledge = errors.New("eval: knowledge cites a holdout trace")
	ErrNoFailedTrace    = errors.New("eval: no trace recorded for the failed review")
	ErrReviewPanicked   = errors.New("eval: review panicked")
	ErrNegativeParallel = errors.New("eval: negative parallel")
)
