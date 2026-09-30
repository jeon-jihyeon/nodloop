package diagnose

import "errors"

var (
	// Select and Record refuse a pending id that already has a diagnose trace
	ErrRecorded = errors.New("diagnose: context already recorded")
	// Record refuses a context that offered candidates and saw no select
	ErrNotSelected = errors.New("diagnose: candidates were offered and select was not called")
	// Select refuses ids that were not offered
	ErrNotOffered = errors.New("diagnose: id was not offered as a candidate")
	// A pending id that names a trace of another kind
	ErrNotContext = errors.New("diagnose: not a context trace")
	// Select and Record refuse a context the batch path built because only that path closes it
	ErrBatchContext = errors.New("diagnose: context belongs to the batch path")
	// The model answered outside the schema
	ErrBadOutput = errors.New("diagnose: output does not match the schema")
	// A context or select trace whose recorded JSON no longer decodes
	ErrMalformed = errors.New("diagnose: malformed trace")
	// A trace that lacks the feedback its caller needs
	// 1. an example candidate whose trace carries no feedback
	// 2. a correction asked of a review with no verdict a person gave
	ErrNoFeedback = errors.New("diagnose: no feedback on trace")
	// An edited review whose keys or status or paragraph ids a later review could not follow
	ErrEditInvalid = errors.New("diagnose: the edited review does not fit the review it corrects")
	// A correction was asked of a review whose latest human verdict approves it
	ErrNotCorrected = errors.New("diagnose: the latest verdict on the review is not an edit or reject")
	// Run on a Diagnoser built for the conversation without a model client
	ErrNoClient = errors.New("diagnose: no model client for the batch path")
	// Prepare refuses a mode outside interactive and batch
	ErrUnknownMode = errors.New("diagnose: unknown mode")
	// Run refuses a knowledge mode outside none, selected and all
	ErrUnknownKnowledgeMode = errors.New("diagnose: unknown knowledge mode")
	// RunAll refuses a negative count of reviews in flight
	ErrNegativeParallel = errors.New("diagnose: negative parallel")
	// A review of RunAll panicked and the panic became its error
	ErrReviewPanicked = errors.New("diagnose: review panicked")
	// A review of RunAll failed without recording a trace so the store failed or no context was built
	ErrNoFailedTrace = errors.New("diagnose: no trace recorded for the failed review")
)
