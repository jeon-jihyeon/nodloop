// Package file reads events and procedures and labels from a directory of plain files
package file

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
)

const (
	eventsFile    = "events.csv"
	contextsFile  = "contexts.csv"
	proceduresDir = "procedures"
	procedureExt  = ".md"
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
// 2. a runbooks folder fails with a rename hint
// 3. a missing procedures directory or one without a procedure fails because no review could cite a paragraph
// 4. when that directory holds Markdown it does not read the failure names those entries
// 5. beside a procedure such Markdown is skipped so an archive folder never stops the reviews and Skipped names it
// 6. a folder that cannot be opened is skipped and named the same way
func (s *Source) Procedures(_ context.Context) (evidence.Procedures, error) {
	old := filepath.Join(s.dir, runbooksDir)
	if _, err := os.Stat(old); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrRunbooksFolder, old)
	}
	l, err := s.list()
	if err != nil {
		return nil, err
	}
	switch {
	case len(l.procedures) == 0 && len(l.skipped) > 0:
		return nil, fmt.Errorf("%w: %s", ErrProcedureSkipped, strings.Join(l.skipped, ", "))
	case len(l.procedures) == 0:
		return nil, fmt.Errorf("%w: %s", ErrNoProcedures, l.dir)
	}
	out := make(evidence.Procedures, 0, len(l.procedures))
	for _, name := range l.procedures {
		b, err := os.ReadFile(filepath.Join(l.dir, name))
		if err != nil {
			return nil, err
		}
		p, err := parseProcedure(name, string(b))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Paths of the Markdown entries under procedures that Procedures does not read
func (s *Source) Skipped(_ context.Context) ([]string, error) {
	l, err := s.list()
	if err != nil {
		return nil, err
	}
	return l.skipped, nil
}

// Entries of the procedures directory in name order
type listing struct {
	dir string
	// File names ending in `.md` directly under dir
	procedures []string
	// 1. a Markdown file spelled other than `.md`
	// 2. a folder that holds Markdown at any depth
	// 3. a folder that cannot be opened at any depth
	skipped []string
}

// Sorts the procedures directory into procedures and skipped Markdown
// 1. an entry starting with a dot is neither read nor named whatever its extension so editor and AppleDouble files stay out
// 2. other files and folders without Markdown at any depth are ignored so images may sit beside
func (s *Source) list() (listing, error) {
	l := listing{dir: filepath.Join(s.dir, proceduresDir)}
	entries, err := os.ReadDir(l.dir)
	if errors.Is(err, os.ErrNotExist) {
		return listing{}, fmt.Errorf("%w: %s", ErrNoProcedures, l.dir)
	}
	if err != nil {
		return listing{}, err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(l.dir, name)
		switch {
		case strings.HasPrefix(name, "."):
		case filepath.Ext(name) == procedureExt:
			l.procedures = append(l.procedures, name)
		case !entry.IsDir() && markdown(name):
			l.skipped = append(l.skipped, path)
		case entry.IsDir() && holdsMarkdown(path):
			l.skipped = append(l.skipped, path)
		}
	}
	return l, nil
}

// Markdown at any depth so a procedure nested by team or topic is still named
// 1. entries starting with a dot are passed over at every depth as list passes them over
// 2. a folder that cannot be opened counts as holding Markdown because nothing proves it holds none
// 3. that folder never fails the listing because the walk only feeds a warning and must not stop reviews
func holdsMarkdown(dir string) bool {
	found := false
	// The callback never returns an error of its own so WalkDir returns nil
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			found = true
			return fs.SkipAll
		case path != dir && strings.HasPrefix(entry.Name(), ".") && entry.IsDir():
			return fs.SkipDir
		case path != dir && strings.HasPrefix(entry.Name(), "."):
			return nil
		case !entry.IsDir() && markdown(entry.Name()):
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// Any spelling of a Markdown extension
func markdown(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == procedureExt || ext == ".markdown"
}

// File order
// A missing file reads as empty because labels are optional
// 1. an invalid label fails the read with its line named
// 2. a repeated event id fails the same way because the later line would silently decide the score
func (s *Source) Labels(_ context.Context) ([]evidence.Label, error) {
	f, err := jsonl.Open[evidence.Label](s.dir, labelsFile)
	if err != nil {
		return nil, err
	}
	return f.All(evidence.Label.Validate, labeledEvents{}.add)
}

// Event ids of the labels read so far
type labeledEvents map[string]struct{}

func (seen labeledEvents) add(l evidence.Label) error {
	if _, ok := seen[l.EventID]; ok {
		return fmt.Errorf("%w: label repeats event_id %q", evidence.ErrMalformed, l.EventID)
	}
	seen[l.EventID] = struct{}{}
	return nil
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
