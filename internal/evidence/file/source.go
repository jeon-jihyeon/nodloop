// Package file reads events and runbooks and labels from a directory of plain files
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
	eventsFile   = "events.csv"
	contextsFile = "contexts.csv"
	runbooksDir  = "runbooks"
	labelsFile   = "labels.jsonl"
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
		start, end := ev.Points[0].Time, ev.Points[len(ev.Points)-1].Time
		refs = append(refs, evidence.EventRef{ID: ev.ID, Start: start, End: end})
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

// Runbooks in name order
// 1. the directory is listed rather than globbed so a data path with glob characters still reads
// 2. a missing runbooks directory reads as no paragraphs
func (s *Source) Paragraphs(_ context.Context) ([]evidence.Paragraph, error) {
	dir := filepath.Join(s.dir, runbooksDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []evidence.Paragraph
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, splitParagraphs(entry.Name(), string(b))...)
	}
	return out, nil
}

// File order
// A missing file reads as empty because labels are optional
func (s *Source) Labels(_ context.Context) ([]evidence.Label, error) {
	f, err := jsonl.Open[evidence.Label](s.dir, labelsFile)
	if err != nil {
		return nil, err
	}
	return f.All()
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
