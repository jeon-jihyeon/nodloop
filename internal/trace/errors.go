package trace

import "errors"

// Returned by Get for an unknown id
var ErrNotFound = errors.New("trace: not found")

// Feedback and outcomes and knowledge cite only a diagnose trace
var ErrNotReview = errors.New("trace: not a diagnose trace")

// A failed review holds no output so a correction or an outcome has nothing to apply to
var ErrFailedReview = errors.New("trace: the review failed and holds no output")
