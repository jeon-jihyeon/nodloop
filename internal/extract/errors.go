package extract

import "errors"

var (
	// The run has no edit or reject to learn from
	ErrNotCorrected = errors.New("extract: the run has no edit or reject on it")
	// The model answered outside the schema
	ErrDraftInvalid = errors.New("extract: draft does not match the schema")
	// A relation outside the set or one that names no item the run reaches
	ErrRelationInvalid = errors.New("extract: relation does not match the items the run reaches")
	// Content that is not one sentence or that copies the output
	ErrNotLesson = errors.New("extract: content is not a one sentence lesson")
	// An update that adds the values of a run to the item as one more case
	ErrCaseList = errors.New("extract: the update lists cases instead of restating the rule")
	// A key the run does not carry
	ErrKeyUnknown = errors.New("extract: the run carries no such label key")
	// The critic answered false to one of its questions
	ErrCriticRefused = errors.New("extract: the critic refused the draft")
)
