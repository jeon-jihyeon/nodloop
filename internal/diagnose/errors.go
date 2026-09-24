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
	// The model answered outside the schema
	ErrBadOutput = errors.New("diagnose: output does not match the schema")
	// A context or select trace whose recorded JSON no longer decodes
	ErrMalformed = errors.New("diagnose: malformed trace")
	// An example candidate whose trace carries no feedback
	ErrNoFeedback = errors.New("diagnose: no feedback on trace")
	// Run on a Diagnoser built for the conversation without a model client
	ErrNoClient = errors.New("diagnose: no model client for the batch path")
	// Prepare refuses a mode outside interactive and batch
	ErrUnknownMode = errors.New("diagnose: unknown mode")
	// Run refuses a knowledge mode outside none, selected and all
	ErrUnknownKnowledgeMode = errors.New("diagnose: unknown knowledge mode")
	// A policy file whose limits section does not decode
	ErrBadLimits = errors.New("diagnose: limits do not decode")
)
