package eval

import "errors"

var (
	ErrRepeat = errors.New("eval: repeat must not be negative")
	// A run with repeats in a session half without them or the reverse
	ErrRepeatSession = errors.New("eval: the session already holds reviews with a different repeat setting")
	// Report on a session with no diagnose trace
	ErrNoTraces         = errors.New("eval: no traces for the session")
	ErrNoLabels         = errors.New("eval: no labels for that half of the set")
	ErrEventOutsideSet  = errors.New("eval: event is not a label of that half")
	ErrUnknownCondition = errors.New("eval: unknown condition")
	ErrSeedConditions   = errors.New("eval: conditions apply to holdout only")
	ErrHoldoutFeedback  = errors.New("eval: holdout trace has feedback")
	ErrHoldoutKnowledge = errors.New("eval: knowledge cites a holdout trace")
	// A condition whose reviews would carry nothing and repeat the baseline at full cost
	ErrNoCorrections       = errors.New("eval: no edit or reject verdict on a review outside the holdout half")
	ErrNoApprovedKnowledge = errors.New("eval: no approved knowledge")
)
