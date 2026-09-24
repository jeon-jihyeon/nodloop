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

// Records hold review text and verdicts of one user
// Nobody else on the machine reads them
const perms = 0o600

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
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%s: %w", f.name(), err)
	}
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, perms)
	if err != nil {
		return fmt.Errorf("%s: %w", f.name(), err)
	}
	if err = errors.Join(f.write(file, append(b, '\n')), file.Close()); err != nil {
		return fmt.Errorf("%s: %w", f.name(), err)
	}
	return nil
}

func (f File[T]) write(file *os.File, record []byte) error {
	if err := lock(file); err != nil {
		return err
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		return err
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
func (f File[T]) All() ([]T, error) {
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
	var all []T
	line := 0
	for raw := range bytes.Lines(data) {
		line++
		if !bytes.HasSuffix(raw, []byte{'\n'}) && f.torn(raw) {
			continue
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		var v T
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", f.name(), line, err)
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
func (File[T]) torn(tail []byte) bool {
	var v T
	return json.Unmarshal(tail, &v) != nil
}

func (f File[T]) name() string {
	return filepath.Base(f.path)
}
