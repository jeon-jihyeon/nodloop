package trace

import "errors"

// Returned by Get for an unknown id
var ErrNotFound = errors.New("trace: not found")

// A correction of a review reads only a diagnose trace
var ErrNotReview = errors.New("trace: not a diagnose trace")

// Feedback and outcomes and knowledge cite only a diagnose or a run trace
var ErrNotRun = errors.New("trace: not a recorded review or run")

// A failed run holds no output so a correction or an outcome has nothing to apply to
var ErrFailedRun = errors.New("trace: the run failed and holds no output")

// A run names the producer that made it
var ErrProducerRequired = errors.New("trace: a run needs its producer")

// A label key or value that is empty could never be matched by a scope
var ErrLabelEmpty = errors.New("trace: empty label")
