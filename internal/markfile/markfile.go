// Package markfile keeps one mark in a file that one process at a time holds without waiting
package markfile

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// A mark file one process holds until Release
type Held struct {
	file *os.File
	// The mark the file held when it was taken
	// Zero when it held none that decodes
	Mark record.Mark
}

// Takes the file at path without waiting and reads its mark
// 1. nil without an error when another process holds it so the caller ends at once
// 2. a mark that does not decode reads as the zero Mark
func Hold(path string) (*Held, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	locked, err := tryLock(file)
	if err != nil || !locked {
		return nil, errors.Join(err, file.Close())
	}
	b, err := io.ReadAll(file)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	var last record.Mark
	if json.Unmarshal(b, &last) != nil {
		last = record.Mark{}
	}
	return &Held{file: file, Mark: last}, nil
}

// Writes the mark in place as one JSON line
// A torn write reads as the zero Mark on the next read
func (h *Held) Write(m record.Mark) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := h.file.Truncate(0); err != nil {
		return err
	}
	_, err = h.file.WriteAt(append(b, '\n'), 0)
	return err
}

// Lets the next process take the file
func (h *Held) Release() error {
	return h.file.Close()
}

// The mark at path read without the lock in the shape its owner keeps
// A missing file and one that does not decode read as the zero mark
func Read[M any](path string) (M, error) {
	var zero M
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	var last M
	if json.Unmarshal(b, &last) != nil {
		return zero, nil
	}
	return last, nil
}
