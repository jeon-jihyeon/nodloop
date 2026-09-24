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
func (s *OutcomeStore) List(_ context.Context, traceID string) ([]feedback.Outcome, error) {
	outcomes, err := s.file.Newest(feedback.OutcomeFilter{TraceID: traceID}.Matches, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return outcomes, nil
}
