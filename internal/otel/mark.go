package otel

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/markfile"
	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// The mark of the last export in the record directory
const MarkFile = "export.json"

// The mark of the newest record an export of one record directory sent
type Marker struct {
	dir string
}

func NewMarker(dir string) Marker {
	return Marker{dir: dir}
}

// The mark one export holds until Release
type Held struct {
	held *markfile.Held
	// The newest record the last export sent and zero before the first
	At time.Time
}

// Takes the mark without waiting and reads where the last export ended
// 1. nil without an error when another export holds it so the caller ends at once
// 2. a mark that does not decode reads as no export so far
func (m Marker) Hold() (*Held, error) {
	held, err := markfile.Hold(filepath.Join(m.dir, MarkFile))
	if err != nil || held == nil {
		return nil, markErr(err)
	}
	return &Held{held: held, At: held.Mark.At}, nil
}

// Moves the mark to the newest record the export sent
func (h *Held) Move(at time.Time) error {
	err := h.held.Write(record.Mark{At: at.UTC()})
	if err == nil {
		h.At = at
	}
	return markErr(err)
}

// Lets the next export take the mark
func (h *Held) Release() error {
	return markErr(h.held.Release())
}

func markErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrMark, err)
}
