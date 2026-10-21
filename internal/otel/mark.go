package otel

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/markfile"
)

// The mark of the last export in the record directory
const MarkFile = "export.json"

// Where the last export of one record directory ended
type Mark struct {
	// The newest record time the export sent and zero before the first
	At time.Time `json:"at"`
	// Per store the position the export read up to in the order the store appended its records
	// So the next export reads only what was appended since and a record with an older time still goes once
	// Absent in a mark that kept the time alone
	After map[string]string `json:"after,omitempty"`
}

// The mark an export of one record directory keeps
type Marker struct {
	dir string
}

func NewMarker(dir string) Marker {
	return Marker{dir: dir}
}

// The mark one export holds until Release
type Held struct {
	held *markfile.Held[Mark]
	Mark
}

// Takes the mark without waiting and reads where the last export ended
// 1. nil without an error when another export holds it so the caller ends at once
// 2. a mark that does not decode reads as no export so far
func (m Marker) Hold() (*Held, error) {
	held, err := markfile.Hold[Mark](filepath.Join(m.dir, MarkFile))
	if err != nil || held == nil {
		return nil, markErr(err)
	}
	return &Held{held: held, Mark: held.Mark}, nil
}

// Moves the mark to where the export ended
func (h *Held) Move(m Mark) error {
	m.At = m.At.UTC()
	err := h.held.Write(m)
	if err == nil {
		h.Mark = m
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
