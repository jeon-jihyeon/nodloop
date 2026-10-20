// Package markfile keeps one mark in a file that one process at a time holds without waiting
package markfile

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

// A mark file one process holds until Release in the shape its owner keeps
// The owner names the shape so a package of infrastructure never imports the one that owns a mark
type Held[M any] struct {
	file *os.File
	// The mark the file held when it was taken
	// Zero when it held none that decodes
	Mark M
}

// Takes the file at path without waiting and reads its mark
// 1. nil without an error when another process holds it so the caller ends at once
// 2. a mark that does not decode reads as the zero mark
func Hold[M any](path string) (*Held[M], error) {
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
	var last M
	if json.Unmarshal(b, &last) != nil {
		var zero M
		last = zero
	}
	return &Held[M]{file: file, Mark: last}, nil
}

// Writes the mark in place as one JSON line
// A torn write reads as the zero mark on the next read
func (h *Held[M]) Write(m M) error {
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
func (h *Held[M]) Release() error {
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
