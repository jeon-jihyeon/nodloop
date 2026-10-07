// Package file keeps feedback and outcomes on append only JSONL files
package file

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
)

const feedbackFile = "feedback.jsonl"

type Store struct {
	file jsonl.File[feedback.Feedback]
}

func New(dir string) (*Store, error) {
	f, err := jsonl.Open[feedback.Feedback](dir, feedbackFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	return &Store{file: f}, nil
}

func (s *Store) Append(_ context.Context, fb feedback.Feedback) error {
	if err := s.file.Append(fb); err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
}

// Newest first
// A corrupt line returns the verdicts of the other lines with the error
func (s *Store) List(_ context.Context, f feedback.Filter) ([]feedback.Feedback, error) {
	records, err := s.file.Newest(f.Matches, f.Older, f.Limit)
	if err != nil {
		return records, readFailed(err)
	}
	return records, nil
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
