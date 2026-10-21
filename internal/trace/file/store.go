// Package file keeps traces on one append only JSONL file
package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/record"
)

const tracesFile = "traces.jsonl"

type Store struct {
	file jsonl.File[record.Trace]
	// The same file read as heads by a prune
	heads jsonl.File[head]
	ids   jsonl.File[idLine]
	// The same file read by a list that leaves the output out
	bare jsonl.File[bare]
}

// A trace line read without its output
type bare struct {
	record.Trace
	// Shadows the output of the trace so the decoder passes over it and never copies it
	Output skipped `json:"output"`
}

// A value whose decoding keeps nothing
type skipped struct{}

func (*skipped) UnmarshalJSON([]byte) error { return nil }

// The fields of a trace a prune reads
type head struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Ref   string          `json:"ref,omitempty"`
	Time  time.Time       `json:"time"`
	Input json.RawMessage `json:"input"`
	Error string          `json:"error,omitempty"`
}

// The one field of a trace a removal reads
type idLine struct {
	ID string `json:"id"`
}

func New(dir string) (*Store, error) {
	return open(dir, tracesFile)
}

func open(dir, name string) (*Store, error) {
	f, err := jsonl.Open[record.Trace](dir, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	heads, err := jsonl.Open[head](dir, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	ids, err := jsonl.Open[idLine](dir, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	b, err := jsonl.Open[bare](dir, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	return &Store{file: f, heads: heads, ids: ids, bare: b}, nil
}

func (s *Store) Append(_ context.Context, t record.Trace) error {
	if err := s.file.Append(t); err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
}

// A corrupt line returns the trace found with the error as List does
func (s *Store) Get(_ context.Context, id string) (record.Trace, error) {
	found, err := s.file.Newest(record.TraceFilter{ID: id}.Matches, nil, 1)
	switch {
	case len(found) == 0 && err != nil:
		return record.Trace{}, readFailed(err)
	case len(found) == 0:
		return record.Trace{}, fmt.Errorf("%w: %q", record.ErrTraceNotFound, id)
	case err != nil:
		return found[0], readFailed(err)
	}
	return found[0], nil
}

// Newest first
// A corrupt line returns the traces of the other lines with the error
func (s *Store) List(_ context.Context, f record.TraceFilter) ([]record.Trace, error) {
	if !f.WithoutOutput {
		found, err := s.file.Newest(f.Matches, nil, f.Limit)
		return found, readFailed(err)
	}
	found, err := s.bare.Newest(func(b bare) bool { return f.Matches(b.Trace) }, nil, f.Limit)
	var out []record.Trace
	for _, b := range found {
		out = append(out, b.Trace)
	}
	return out, readFailed(err)
}

// The traces the filter keeps that the file holds after the position oldest first with the position after the last line read
// 1. the empty position starts at the first trace and Limit ends the read early
// 2. a position whose trace a rewrite removed fails with ErrPositionGone from record
// A corrupt line returns the traces of the other lines with the error as List does
func (s *Store) After(_ context.Context, f record.TraceFilter, from string) ([]record.Trace, string, error) {
	found, next, err := s.file.After(from, f.Matches, f.Limit)
	if errors.Is(err, jsonl.ErrPositionGone) {
		return nil, from, fmt.Errorf("%w: %w", record.ErrPositionGone, err)
	}
	return found, next, readFailed(err)
}

// The traces of the names newest first without their output
// Each line decodes into the fields a prune reads so no output is ever decoded
// A corrupt line returns the traces of the other lines with the error as List does
func (s *Store) Heads(_ context.Context, names ...string) ([]record.Trace, error) {
	found, err := s.heads.Newest(func(h head) bool { return slices.Contains(names, h.Name) }, nil, 0)
	out := make([]record.Trace, 0, len(found))
	for _, h := range found {
		out = append(out, record.Trace{ID: h.ID, Name: h.Name, Ref: h.Ref, Time: h.Time, Input: h.Input, Error: h.Error})
	}
	if err != nil {
		return out, readFailed(err)
	}
	return out, nil
}

// Removes the traces of the ids decide returns and returns how many it removed
// decide runs under the lock every writer of the file takes so no run is appended between it and the rewrite
// Each line decodes only its id
func (s *Store) Prune(ctx context.Context, decide func(context.Context) ([]string, error)) (int, error) {
	n, err := s.ids.Remove(func() (func(idLine) bool, error) {
		ids, err := decide(ctx)
		if err != nil || len(ids) == 0 {
			return nil, err
		}
		drop := make(map[string]bool, len(ids))
		for _, id := range ids {
			drop[id] = true
		}
		return func(l idLine) bool { return drop[l.ID] }, nil
	})
	if err != nil {
		return n, fmt.Errorf("%w: %w", ErrPrune, err)
	}
	return n, nil
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
	if err == nil {
		return nil
	}
	if errors.Is(err, jsonl.ErrCorrupt) {
		return corruptError{err}
	}
	return fmt.Errorf("%w: %w", ErrRead, err)
}
