// Package file implements the knowledge store on one append only JSONL file
package file

import (
	"context"
	"fmt"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

const knowledgeFile = "knowledge.jsonl"

type Store struct {
	file jsonl.File[knowledge.Knowledge]
}

func New(dir string) (*Store, error) {
	f, err := jsonl.Open[knowledge.Knowledge](dir, knowledgeFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpen, err)
	}
	return &Store{file: f}, nil
}

func (s *Store) Append(_ context.Context, k knowledge.Knowledge) error {
	if err := s.file.Append(k); err != nil {
		return fmt.Errorf("%w: %w", ErrAppend, err)
	}
	return nil
}

// decide gets the records newest first as List gives them and its records land in their order
// Its refusal comes back as it is and only a file failure becomes ErrAppend so a caller can tell the two apart
func (s *Store) AppendDecided(_ context.Context, decide func(all knowledge.Set) ([]knowledge.Knowledge, error)) error {
	var refused error
	err := s.file.AppendDecided(func(current []knowledge.Knowledge) ([]knowledge.Knowledge, error) {
		all := slices.Clone(current)
		slices.Reverse(all)
		records, err := decide(all)
		refused = err
		return records, err
	})
	switch {
	case err == nil:
		return nil
	case refused != nil:
		return refused
	}
	return fmt.Errorf("%w: %w", ErrAppend, err)
}

func (s *Store) List(_ context.Context) ([]knowledge.Knowledge, error) {
	records, err := s.file.All()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	slices.Reverse(records)
	return records, nil
}
