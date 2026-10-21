package otel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
	"github.com/jeon-jihyeon/nodloop/internal/markfile"
)

// The mark of the last export in the record directory
const MarkFile = "export.json"

// Where the last export of one record directory ended
type Mark struct {
	// The newest resolved record time and zero before the first export
	At time.Time `json:"at"`
	// Per store the position the export read up to in the order the store appended its records
	// So the next export reads only what was appended since and a record with an older time still goes once
	// Absent in a mark that kept the time alone
	After     map[string]string `json:"after,omitempty"`
	Completed []string          `json:"completed,omitempty"`
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
	held *markfile.Held[struct{}]
	path string
	Mark
}

// Takes the mark without waiting and reads where the last export ended
// 1. nil without an error when another export holds it so the caller ends at once
// 2. a mark that does not decode reads as no export so far
func (m Marker) Hold() (*Held, error) {
	held, err := markfile.Hold[struct{}](filepath.Join(m.dir, "export.lock"))
	if err != nil || held == nil {
		return nil, markErr(err)
	}
	path := filepath.Join(m.dir, MarkFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, markErr(errors.Join(err, held.Release()))
	}
	b, err := io.ReadAll(file)
	if err = errors.Join(err, file.Close()); err != nil {
		return nil, markErr(errors.Join(err, held.Release()))
	}
	var mark Mark
	if json.Unmarshal(b, &mark) != nil {
		mark = Mark{}
	}
	return &Held{held: held, path: path, Mark: mark}, nil
}

// Moves the mark to where the export ended
func (h *Held) Move(m Mark) error {
	if h.held == nil {
		return markErr(os.ErrClosed)
	}
	m.At = m.At.UTC()
	b, err := json.Marshal(m)
	if err != nil {
		return markErr(err)
	}
	err = atomicfile.Replace(h.path, append(b, '\n'))
	if err == nil {
		h.Mark = m
	}
	return markErr(err)
}

// Lets the next export take the mark
func (h *Held) Release() error {
	if h.held == nil {
		return markErr(os.ErrClosed)
	}
	err := h.held.Release()
	h.held = nil
	return markErr(err)
}

func markErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrMark, err)
}
