package file

import (
	"context"
	"fmt"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
)

const outcomeFile = "outcomes.jsonl"

// Same directory as feedback but its own file
type OutcomeStore struct {
	file jsonl.File[feedback.Outcome]
}

func NewOutcomeStore(dir string) (*OutcomeStore, error) {
	f, err := jsonl.Open[feedback.Outcome](dir, outcomeFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	return &OutcomeStore{file: f}, nil
}

func (s *OutcomeStore) Append(_ context.Context, o feedback.Outcome) error {
	if err := s.file.Append(o); err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
}

// Newest first
// A corrupt line returns the outcomes of the other lines with the error
func (s *OutcomeStore) List(_ context.Context, traceID string) ([]feedback.Outcome, error) {
	outcomes, err := s.file.Newest(feedback.OutcomeFilter{TraceID: traceID}.Matches, nil, 0)
	if err != nil {
		return outcomes, readFailed(err)
	}
	return outcomes, nil
}

// How many records the file holds and the error naming its corrupt lines
func (s *OutcomeStore) Check() (int, error) {
	n, err := s.file.Check()
	if err != nil {
		return n, readFailed(err)
	}
	return n, nil
}

// Moves the corrupt lines to a file beside it and returns how many
func (s *OutcomeStore) Repair() (int, error) {
	n, err := s.file.Repair()
	if err != nil {
		return n, fmt.Errorf("%w: %w", ErrRepair, err)
	}
	return n, nil
}

func (s *OutcomeStore) Name() string {
	return s.file.Name()
}
