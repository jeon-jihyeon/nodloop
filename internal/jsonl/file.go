// Package jsonl is one append only file with one JSON record per line
package jsonl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Records hold AI output and verdicts of one user
// Nobody else on the machine reads them
const perms = 0o600

// A file saved by an editor that writes one still reads
var byteOrderMark = []byte("\uFEFF")

// Every read walks the whole file because the data is small
// 1. Append holds an exclusive flock so writers in any process such as the MCP server and the CLI take turns
// 2. Append writes one record in one syscall with O_APPEND
// 3. readers take no lock and skip an unterminated last line that fails to decode: a write in flight or a crash
// 4. Append cuts such a torn tail before it writes so the torn bytes never end up inside the file
// 5. an unterminated tail that decodes as a record lost only its newline and Append keeps it
// Records are JSON objects so no torn prefix of one parses
type File[T any] struct {
	path string
}

// The directory must exist
// The file is created on the first append
func Open[T any](dir, name string) (File[T], error) {
	info, err := os.Stat(dir)
	if err != nil {
		return File[T]{}, fmt.Errorf("%s: %w", name, err)
	}
	if !info.IsDir() {
		return File[T]{}, fmt.Errorf("%s: %s %w", name, dir, ErrNotDirectory)
	}
	return File[T]{path: filepath.Join(dir, name)}, nil
}

func (f File[T]) Append(v T) error {
	return f.append(func([]byte) ([]T, error) { return []T{v}, nil })
}

// Appends the records decide returns for the records the file holds in file order
// 1. decide runs once under the lock of the append so no writer slips in between
// 2. the records go out in one write so a reader sees all of them or none
// 3. an error or no record from decide writes nothing
func (f File[T]) AppendDecided(decide func(current []T) ([]T, error)) error {
	return f.append(func(data []byte) ([]T, error) {
		current, err := f.decode(data)
		if err != nil {
			return nil, err
		}
		return decide(current)
	})
}

func (f File[T]) append(decide func(data []byte) ([]T, error)) error {
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, perms)
	if err != nil {
		return fmt.Errorf("%s: %w", f.name(), err)
	}
	if err = errors.Join(f.write(file, decide), file.Close()); err != nil {
		return fmt.Errorf("%s: %w", f.name(), err)
	}
	return nil
}

func (f File[T]) write(file *os.File, decide func(data []byte) ([]T, error)) error {
	if err := lock(file); err != nil {
		return err
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		return err
	}
	vs, err := decide(data)
	if err != nil {
		return err
	}
	var record []byte
	for _, v := range vs {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		record = append(append(record, b...), '\n')
	}
	if len(record) == 0 {
		return nil
	}
	end := bytes.LastIndexByte(data, '\n') + 1
	if tail := data[end:]; len(tail) > 0 && f.torn(tail) {
		if err = file.Truncate(int64(end)); err != nil {
			return err
		}
		data = data[:end]
	}
	if len(data) > end {
		record = slices.Concat([]byte{'\n'}, record)
	}
	_, err = file.Write(record)
	return err
}

// File order
// A missing file reads as empty only while its directory exists
// A directory moved or removed after Open is a store failure and never an empty store
// A record that fails a check fails the read with its line named
func (f File[T]) All(checks ...func(T) error) ([]T, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		if _, err = os.Stat(filepath.Dir(f.path)); err != nil {
			return nil, fmt.Errorf("%s: %w", f.name(), err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.name(), err)
	}
	return f.decode(data, checks...)
}

func (f File[T]) decode(data []byte, checks ...func(T) error) ([]T, error) {
	var all []T
	line := 0
	for raw := range bytes.Lines(bytes.TrimPrefix(data, byteOrderMark)) {
		line++
		if !bytes.HasSuffix(raw, []byte{'\n'}) && f.torn(raw) {
			continue
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", f.name(), line, err)
		}
		for _, check := range checks {
			if err := check(v); err != nil {
				return nil, fmt.Errorf("%s line %d: %w", f.name(), line, err)
			}
		}
		all = append(all, v)
	}
	return all, nil
}

// Newest first
// A record that fails keep is skipped and limit zero means all
func (f File[T]) Newest(keep func(T) bool, limit int) ([]T, error) {
	all, err := f.All()
	if err != nil {
		return nil, err
	}
	var out []T
	for _, v := range slices.Backward(all) {
		if !keep(v) {
			continue
		}
		out = append(out, v)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// Whether an unterminated last line fails to decode as a record
// A file holding one record after a byte order mark keeps it
func (File[T]) torn(tail []byte) bool {
	var v T
	return json.Unmarshal(bytes.TrimPrefix(tail, byteOrderMark), &v) != nil
}

func (f File[T]) name() string {
	return filepath.Base(f.path)
}
