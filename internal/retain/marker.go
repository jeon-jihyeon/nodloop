package retain

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/markfile"
	"github.com/jeon-jihyeon/nodloop/internal/record"
)

const (
	// The marker of the last daily prune in the record directory
	MarkerFile = "prune.json"
	// At most one daily prune per interval whatever records the runs
	Interval = 24 * time.Hour
)

// One prune of a record store
type Mark struct {
	// When the prune started
	At time.Time
	// The runs before it may be gone so a report reads from it on
	// Zero on a marker of a release that did not record it
	Cutoff time.Time
}

// Whether the prune started less than every before now
// A mark from the future reads as elapsed so a clock set back never stops the prune
func (m Mark) fresh(now time.Time, every time.Duration) bool {
	elapsed := now.Sub(m.At)
	return elapsed >= 0 && elapsed < every
}

// Where the last prune of one record store is kept so every surface over the store reads the same window
type Marks interface {
	// Records the prune of the cutoff that starts at the time unless the last one started less than every before
	// Reports whether it recorded it so only one process runs the prune
	Claim(ctx context.Context, at, cutoff time.Time, every time.Duration) (bool, error)
	// The last prune recorded and the zero Mark when none was
	Last(ctx context.Context) (record.Mark, error)
}

// The marks of a record directory in its MarkerFile
type Marker struct {
	dir string
}

func NewMarker(dir string) Marker {
	return Marker{dir: dir}
}

func (m Marker) path() string {
	return filepath.Join(m.dir, MarkerFile)
}

// 1. a process that finds the marker locked claims nothing without waiting since another process is claiming
// 2. a marker that does not decode reads as no prune so far
// 3. the marker is written in place and a torn write reads as no prune so far and costs one more prune
func (m Marker) Claim(_ context.Context, at, cutoff time.Time, every time.Duration) (bool, error) {
	held, err := markfile.Hold(m.path())
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrMarker, err)
	}
	if held == nil {
		return false, nil
	}
	defer func() { _ = held.Release() }()
	if Mark(held.Mark).fresh(at, every) {
		return false, nil
	}
	if err := held.Write(record.Mark{At: at, Cutoff: cutoff}); err != nil {
		return false, fmt.Errorf("%w: %w", ErrMarker, err)
	}
	return true, nil
}

// Read without the lock
// A missing marker and one that does not decode read as no prune so far
func (m Marker) Last(context.Context) (record.Mark, error) {
	last, err := markfile.Read(m.path())
	if err != nil {
		return record.Mark{}, fmt.Errorf("%w: %w", ErrMarker, err)
	}
	return last, nil
}

// The daily prune of one record store
type Daily struct {
	marks Marks
	days  Days
	// Opens the retainer only when a prune is due since most triggers find the mark fresh
	open func() (*Retainer, error)
	now  func() time.Time
}

func NewDaily(marks Marks, days Days, open func() (*Retainer, error), now func() time.Time) Daily {
	return Daily{marks: marks, days: days, open: open, now: now}
}

// Whether the last mark says a prune is due
// A caller that starts the prune elsewhere asks this first so it starts nothing on most triggers
func (d Daily) Due(ctx context.Context) (bool, error) {
	if d.days == 0 {
		return false, nil
	}
	if err := d.days.Check(); err != nil {
		return false, err
	}
	last, err := d.marks.Last(ctx)
	if err != nil {
		return false, err
	}
	return !Mark(last).fresh(d.now(), Interval), nil
}

// Prunes the record store when Interval passed since the last mark and reports whether it ran
// 1. nothing runs without retention so an existing record store changes nothing
// 2. days that Check refuses fail before the mark is read
// 3. the mark with its cutoff is claimed before the prune so a prune that fails is tried again a day later and not after every run
func (d Daily) Run(ctx context.Context) (bool, error) {
	if d.days == 0 {
		return false, nil
	}
	if err := d.days.Check(); err != nil {
		return false, err
	}
	now := d.now()
	cutoff := d.days.Cutoff(now)
	claimed, err := d.marks.Claim(ctx, now, cutoff, Interval)
	if err != nil || !claimed {
		return false, err
	}
	retainer, err := d.open()
	if err != nil {
		return true, err
	}
	_, err = retainer.Prune(ctx, cutoff)
	return true, err
}
