// Package file keeps feedback and outcomes on append only JSONL files
package file

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/record"
)

const feedbackFile = "feedback.jsonl"

type Store struct {
	file jsonl.File[record.Feedback]
}

func New(dir string) (*Store, error) {
	f, err := jsonl.Open[record.Feedback](dir, feedbackFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	return &Store{file: f}, nil
}

func (s *Store) Append(_ context.Context, fb record.Feedback) error {
	if err := s.file.Append(fb); err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
}

// Newest first
// A corrupt line returns the verdicts of the other lines with the error
func (s *Store) List(_ context.Context, f record.FeedbackFilter) ([]record.Feedback, error) {
	records, err := s.file.Newest(f.Matches, f.Older, f.Limit)
	if err != nil {
		return records, readFailed(err)
	}
	return records, nil
}

// The verdicts the file holds after the position oldest first with the position after the last line read
// 1. the empty position starts at the first verdict and a limit of zero reads every one
// 2. a position whose verdict a rewrite removed fails with record.ErrPositionGone
// A corrupt line returns the verdicts of the other lines with the error as List does
func (s *Store) After(_ context.Context, from string, limit int) ([]record.Feedback, string, error) {
	return after(s.file, from, limit)
}

// The records of one file after the position with the errors of the store
func after[T any](file jsonl.File[T], from string, limit int) ([]T, string, error) {
	found, next, err := file.After(from, func(T) bool { return true }, limit)
	switch {
	case errors.Is(err, jsonl.ErrPositionGone):
		return nil, from, fmt.Errorf("%w: %w", record.ErrPositionGone, err)
	case err != nil:
		return found, next, readFailed(err)
	}
	return found, next, nil
}

// How many records the file holds and the error naming its corrupt lines
func (s *Store) Check() (int, error) {
	n, err := s.file.Check()
	if err != nil {
		return n, readFailed(err)
	}
	return n, nil
}

// Moves the corrupt lines to a file beside it and returns how many
func (s *Store) Repair() (int, error) {
	n, err := s.file.Repair()
	if err != nil {
		return n, fmt.Errorf("%w: %w", ErrRepair, err)
	}
	return n, nil
}

func (s *Store) Name() string {
	return s.file.Name()
}

// A corrupt line also matches ErrCorrupt so a caller tells it from a file it cannot read
func readFailed(err error) error {
	if errors.Is(err, jsonl.ErrCorrupt) {
		return corruptError{err}
	}
	return fmt.Errorf("%w: %w", ErrRead, err)
}
