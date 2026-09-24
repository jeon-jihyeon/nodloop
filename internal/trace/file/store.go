// Package file keeps traces on one append only JSONL file
package file

import (
	"context"
	"fmt"

	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const tracesFile = "traces.jsonl"

type Store struct {
	file jsonl.File[trace.Trace]
}

func New(dir string) (*Store, error) {
	f, err := jsonl.Open[trace.Trace](dir, tracesFile)
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

func (s *Store) Get(_ context.Context, id string) (trace.Trace, error) {
	found, err := s.file.Newest(trace.Filter{ID: id}.Matches, 1)
	if err != nil {
		return trace.Trace{}, fmt.Errorf("%w: %w", ErrRead, err)
	}
	if len(found) == 0 {
		return trace.Trace{}, fmt.Errorf("trace %q: %w", id, trace.ErrNotFound)
	}
	return found[0], nil
}

func (s *Store) List(_ context.Context, f trace.Filter) (trace.Traces, error) {
	found, err := s.file.Newest(f.Matches, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	return found, nil
}
