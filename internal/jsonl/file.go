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
	"strings"
)

// Records hold AI output and verdicts of one user
// Nobody else on the machine reads them
const perms = 0o600

// A file saved by an editor that writes one still reads
var byteOrderMark = []byte("\uFEFF")

// Bytes a read from the end of the file takes at a time
const chunk = 64 << 10

// One append only file of records
// 1. Append holds an exclusive flock so writers in any process such as the MCP server and the CLI take turns
// 2. Append writes one record in one syscall with O_APPEND and reads only the last line of the file
// 3. readers take no lock and skip an unterminated last line that fails to decode: a write in flight or a crash
// 4. Append cuts such a torn tail before it writes so the torn bytes never end up inside the file
// 5. an unterminated tail that decodes as a record lost only its newline and Append keeps it
// 6. a read skips any other line that fails to decode and returns the records it read with ErrCorrupt naming the lines
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
	return f.append(func() ([]T, error) { return []T{v}, nil })
}

// Appends the records decide returns for the records the file holds in file order
// 1. decide runs once under the lock of the append so no writer slips in between
// 2. the records go out in one write so a reader sees all of them or none
// 3. an error or no record from decide writes nothing
// 4. a corrupt line writes nothing since decide would judge without the record it held
func (f File[T]) AppendDecided(decide func(current []T) ([]T, error)) error {
	return f.append(func() ([]T, error) {
		current, err := f.All()
		if err != nil {
			return nil, err
		}
		return decide(current)
	})
}

func (f File[T]) append(decide func() ([]T, error)) error {
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, perms)
	if err != nil {
		return fmt.Errorf("%s: %w", f.name(), err)
	}
	if err = errors.Join(f.write(file, decide), file.Close()); err != nil {
		return fmt.Errorf("%s: %w", f.name(), err)
	}
	return nil
}

func (f File[T]) write(file *os.File, decide func() ([]T, error)) error {
	if err := lock(file); err != nil {
		return err
	}
	vs, err := decide()
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
	end, tail, err := f.lastLine(file)
	if err != nil {
		return err
	}
	switch {
	case len(tail) > 0 && f.torn(tail):
		if err = file.Truncate(end); err != nil {
			return err
		}
	case len(tail) > 0:
		record = slices.Concat([]byte{'\n'}, record)
	}
	_, err = file.Write(record)
	return err
}

// The offset after the last newline and the unterminated bytes after it
func (File[T]) lastLine(file *os.File) (int64, []byte, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, nil, err
	}
	var tail []byte
	for pos := info.Size(); pos > 0; {
		n := min(chunk, pos)
		pos -= n
		buf := make([]byte, n)
		if _, err := file.ReadAt(buf, pos); err != nil {
			return 0, nil, err
		}
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			return pos + int64(i) + 1, slices.Concat(buf[i+1:], tail), nil
		}
		tail = slices.Concat(buf, tail)
	}
	return 0, tail, nil
}

// File order
// A missing file reads as empty only while its directory exists
// A directory moved or removed after Open is a store failure and never an empty store
// A line that fails to decode or a check is left out and named by its number in ErrCorrupt
func (f File[T]) All(checks ...func(T) error) ([]T, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, f.missing()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.name(), err)
	}
	var all []T
	var bad []string
	line := 0
	for raw := range bytes.Lines(bytes.TrimPrefix(data, byteOrderMark)) {
		line++
		v, ok, err := f.decode(raw, checks)
		if err != nil {
			bad = append(bad, fmt.Sprintf("line %d: %v", line, err))
		}
		if ok {
			all = append(all, v)
		}
	}
	return all, f.corrupt(bad)
}

// Newest first, read from the end of the file
// 1. a record that fails keep is skipped and limit zero means all
// 2. the read ends at the first record stop holds for, without returning it, so a reader of recent records leaves the old part of the file unread
// 3. a line that fails to decode is left out and named by its byte offset in ErrCorrupt
// A nil stop reads to the start of the file
func (f File[T]) Newest(keep, stop func(T) bool, limit int) ([]T, error) {
	file, err := os.Open(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, f.missing()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.name(), err)
	}
	defer func() { _ = file.Close() }()
	lines, err := newBackward(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.name(), err)
	}
	var out []T
	var bad []string
	for {
		raw, offset, err := lines.next()
		if err != nil {
			return out, errors.Join(fmt.Errorf("%s: %w", f.name(), err), f.corrupt(bad))
		}
		if raw == nil {
			break
		}
		if offset == 0 {
			raw = bytes.TrimPrefix(raw, byteOrderMark)
		}
		v, ok, err := f.decode(raw, nil)
		if err != nil {
			bad = append(bad, fmt.Sprintf("byte %d: %v", offset, err))
		}
		if !ok {
			continue
		}
		if stop != nil && stop(v) {
			break
		}
		if !keep(v) {
			continue
		}
		out = append(out, v)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, f.corrupt(bad)
}

// Moves the lines that fail to decode to the file of the same name with .corrupt added and returns how many
// 1. it holds the append lock and rewrites the file in place so a writer waiting on the lock appends to the repaired file
// 2. a reader takes no lock and may read the file short while it is rewritten, so it runs while nothing reads
// 3. an unterminated last line stays for the next append to cut or keep
func (f File[T]) Repair() (int, error) {
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_RDWR, perms)
	if errors.Is(err, os.ErrNotExist) {
		return 0, f.missing()
	}
	if err != nil {
		return 0, fmt.Errorf("%s: %w", f.name(), err)
	}
	moved, err := f.repair(file)
	if err = errors.Join(err, file.Close()); err != nil {
		return 0, fmt.Errorf("%s: %w", f.name(), err)
	}
	return moved, nil
}

func (f File[T]) repair(file *os.File) (int, error) {
	if err := lock(file); err != nil {
		return 0, err
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		return 0, err
	}
	var kept, bad []byte
	for raw := range bytes.Lines(data) {
		if _, _, err := f.decode(bytes.TrimPrefix(raw, byteOrderMark), nil); err != nil {
			bad = append(bad, raw...)
			continue
		}
		kept = append(kept, raw...)
	}
	if len(bad) == 0 {
		return 0, nil
	}
	aside, err := os.OpenFile(f.path+".corrupt", os.O_CREATE|os.O_APPEND|os.O_WRONLY, perms)
	if err != nil {
		return 0, err
	}
	if _, err = aside.Write(bad); err != nil {
		return 0, errors.Join(err, aside.Close())
	}
	if err = aside.Close(); err != nil {
		return 0, err
	}
	if err = file.Truncate(0); err != nil {
		return 0, err
	}
	if _, err = file.Write(kept); err != nil {
		return 0, err
	}
	return bytes.Count(bad, []byte{'\n'}), nil
}

// The record of one line and whether there was one
// 1. a blank line holds none
// 2. an unterminated line that fails to decode is a write in flight and holds none without an error
func (f File[T]) decode(raw []byte, checks []func(T) error) (T, bool, error) {
	var v T
	if !bytes.HasSuffix(raw, []byte{'\n'}) && f.torn(raw) {
		return v, false, nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return v, false, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, false, err
	}
	for _, check := range checks {
		if err := check(v); err != nil {
			return v, false, err
		}
	}
	return v, true, nil
}

func (f File[T]) corrupt(bad []string) error {
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s %w: %s", f.name(), ErrCorrupt, strings.Join(bad, "; "))
}

// A file not written yet reads as empty while its directory exists
func (f File[T]) missing() error {
	if _, err := os.Stat(filepath.Dir(f.path)); err != nil {
		return fmt.Errorf("%s: %w", f.name(), err)
	}
	return nil
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
