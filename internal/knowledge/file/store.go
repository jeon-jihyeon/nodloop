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

func (s *Store) List(_ context.Context) ([]knowledge.Knowledge, error) {
	records, err := s.file.All()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRead, err)
	}
	slices.Reverse(records)
	return records, nil
}
