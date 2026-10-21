package trace

import "errors"

// Feedback and outcomes and knowledge cite only a run trace
var ErrNotRun = errors.New("trace: not a recorded run")

// A failed run holds no output so a correction or an outcome has nothing to apply to
var ErrFailedRun = errors.New("trace: the run failed and holds no output")

// A run names the producer that made it
var ErrProducerRequired = errors.New("trace: a run needs its producer")

// A label key or value that is empty could never be matched by a scope
var ErrLabelEmpty = errors.New("trace: empty label")

// A run carries one value per label key
var ErrLabelValues = errors.New("trace: a run label has more than one value")

// A run ends at its time so a duration below zero would start it after it ended
var ErrDurationNegative = errors.New("trace: negative duration")

// An intervention with inconsistent action metadata
var ErrInterventionInvalid = errors.New("trace: invalid intervention")

// A draw envelope that does not fit its source
var ErrDrawInvalid = errors.New("trace: invalid draw")
