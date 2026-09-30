// Package file reads events and procedures and labels from a directory of plain files
package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
)

const (
	eventsFile    = "events.csv"
	contextsFile  = "contexts.csv"
	proceduresDir = "procedures"
	runbooksDir   = "runbooks"
	labelsFile    = "labels.jsonl"
	// Some editors and spreadsheet exports write it before Unicode text
	byteOrderMark = "\uFEFF"
)

// Stateless
// Every call re-reads the files because the data is small
type Source struct {
	dir string
}

func New(dir string) (*Source, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("evidence file source: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDirectory, dir)
	}
	return &Source{dir: dir}, nil
}

// Reads events.csv only so a broken contexts.csv still lists
func (s *Source) Events(_ context.Context) ([]evidence.EventRef, error) {
	events, err := s.loadEvents()
	if err != nil {
		return nil, err
	}
	refs := make([]evidence.EventRef, 0, len(events))
	for _, ev := range events {
		refs = append(refs, ev.Ref())
	}
	return refs, nil
}

// Joins contexts.csv so an event without a row keeps the unknown context
func (s *Source) Event(_ context.Context, id string) (evidence.Event, error) {
	events, err := s.loadEvents()
	if err != nil {
		return evidence.Event{}, err
	}
	contexts, err := s.loadContexts()
	if err != nil {
		return evidence.Event{}, err
	}
	i := slices.IndexFunc(events, func(ev evidence.Event) bool { return ev.ID == id })
	if i < 0 {
		return evidence.Event{}, fmt.Errorf("event %q: %w", id, evidence.ErrNotFound)
	}
	ev := events[i]
	if c, ok := contexts[id]; ok {
		ev.ChangeContext = c
	}
	return ev, nil
}

// Procedures in file name order
// 1. the directory is listed rather than globbed so a data path with glob characters still reads
// 2. a runbooks folder fails the read because its procedures would otherwise vanish without a word
// 3. a missing procedures directory reads as no procedures
func (s *Source) Procedures(_ context.Context) (evidence.Procedures, error) {
	old := filepath.Join(s.dir, runbooksDir)
	if _, err := os.Stat(old); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrRunbooksFolder, old)
	}
	dir := filepath.Join(s.dir, proceduresDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out evidence.Procedures
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		p, err := parseProcedure(entry.Name(), string(b))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// File order
// A missing file reads as empty because labels are optional
// An invalid label fails the read with its line named
func (s *Source) Labels(_ context.Context) ([]evidence.Label, error) {
	f, err := jsonl.Open[evidence.Label](s.dir, labelsFile)
	if err != nil {
		return nil, err
	}
	return f.All(evidence.Label.Validate)
}

func (s *Source) loadEvents() ([]evidence.Event, error) {
	f, err := os.Open(filepath.Join(s.dir, eventsFile))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parseEvents(f)
}

// A missing file means every event is unknown
func (s *Source) loadContexts() (map[string]evidence.Context, error) {
	f, err := os.Open(filepath.Join(s.dir, contextsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parseContexts(f)
}
