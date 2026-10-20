package consolidate

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/markfile"
	"github.com/jeon-jihyeon/nodloop/internal/record"
)

const (
	// The mark of the last consolidate in the record directory
	MarkFile = "consolidate.json"
	// At most one daily run per interval whatever stops the conversations make
	Interval = 24 * time.Hour
)

// The mark of the last run of one record directory
type Marker struct {
	dir string
}

func NewMarker(dir string) Marker {
	return Marker{dir: dir}
}

func (m Marker) path() string {
	return filepath.Join(m.dir, MarkFile)
}

// The last run as the mark records it
type mark record.Mark

// Whether the run started less than every before now
// A mark from the future reads as elapsed so a clock set back never stops the runs
func (m mark) fresh(now time.Time, every time.Duration) bool {
	elapsed := now.Sub(m.At)
	return elapsed >= 0 && elapsed < every
}

// Records a run at the time unless the last one started less than every before and reports whether it did
// 1. a mark another process holds claims nothing without waiting since that process runs
// 2. a mark that does not decode reads as no run so far
// 3. the caller claims before its calls so a run that fails waits a day
func (m Marker) Claim(at time.Time, every time.Duration) (bool, error) {
	held, err := markfile.Hold[record.Mark](m.path())
	if err != nil || held == nil {
		return false, markErr(err)
	}
	defer func() { _ = held.Release() }()
	if mark(held.Mark).fresh(at, every) {
		return false, nil
	}
	err = held.Write(record.Mark{At: at})
	return err == nil, markErr(err)
}

// Whether Interval passed since the last run
// Read without the lock so most stops start nothing and a missing or torn mark reads as due
func (m Marker) Due(now time.Time) (bool, error) {
	last, err := markfile.Read[record.Mark](m.path())
	if err != nil {
		return false, markErr(err)
	}
	return !mark(last).fresh(now, Interval), nil
}

func markErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrMark, err)
}
