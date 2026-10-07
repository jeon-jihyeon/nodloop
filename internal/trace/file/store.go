// Package file keeps traces on one append only JSONL file
package file

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const tracesFile = "traces.jsonl"

type Store struct {
	file jsonl.File[trace.Trace]
}

func New(dir string) (*Store, error) {
	return open(dir, tracesFile)
}

func open(dir, name string) (*Store, error) {
	f, err := jsonl.Open[trace.Trace](dir, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	return &Store{file: f}, nil
}

func (s *Store) Append(_ context.Context, t trace.Trace) error {
	if err := s.file.Append(t); err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
}

// A corrupt line returns the trace found with the error as List does
func (s *Store) Get(_ context.Context, id string) (trace.Trace, error) {
	found, err := s.file.Newest(trace.Filter{ID: id}.Matches, nil, 1)
	switch {
	case len(found) == 0 && err != nil:
		return trace.Trace{}, readFailed(err)
	case len(found) == 0:
		return trace.Trace{}, fmt.Errorf("%w: %q", trace.ErrNotFound, id)
	case err != nil:
		return found[0], readFailed(err)
	}
	return found[0], nil
}

// Newest first
// A corrupt line returns the traces of the other lines with the error
func (s *Store) List(_ context.Context, f trace.Filter) (trace.Traces, error) {
	found, err := s.file.Newest(f.Matches, f.Older, f.Limit)
	if err != nil {
		return found, readFailed(err)
	}
	return found, nil
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
