// Package jsonl is one append only file with one JSON record per line
package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Bytes a rewrite reads and writes at a time
const rewriteBuffer = 1 << 20

// Tests stop a rewrite here as a crash between the synced temp file and the rename would
var beforeReplace = func() error { return nil }

// One append only file of records
// 1. every writer holds an exclusive lock on the lock file beside it so writers in any process such as the MCP server and the CLI take turns
// On unix it then locks the data file too since earlier releases locked only that
// 2. Append opens the file only once it holds the lock and writes one record in one syscall with O_APPEND
// 3. readers take no lock and skip an unterminated last line that fails to decode: a write in flight or a crash
// 4. Append cuts such a torn tail before it writes so the torn bytes never end up inside the file
// 5. an unterminated tail that decodes as a record lost only its newline and Append keeps it
// 6. a read skips any other line that fails to decode and returns the records it read with ErrCorrupt naming the lines
// 7. Repair and Remove write the new content to the temp file beside it and rename it over the file
// A reader thus sees the old file or the new one and never a part of either
// 8. Open and every rewrite first recover what a crashed rewrite left
// Records are JSON objects so no torn prefix of one parses
type File[T any] struct {
	path string
}

// The directory must exist
// The file is created on the first append
// A temp file or a backup a crashed rewrite left is recovered before Open returns
func Open[T any](dir, name string) (File[T], error) {
	info, err := os.Stat(dir)
	if err != nil {
		return File[T]{}, fmt.Errorf("%s: %w", name, err)
	}
	if !info.IsDir() {
		return File[T]{}, fmt.Errorf("%s: %s %w", name, dir, ErrNotDirectory)
	}
	f := File[T]{path: filepath.Join(dir, name)}
	if !f.leftover() {
		return f, nil
	}
	if err := f.locked(f.recover); err != nil {
		return File[T]{}, err
	}
	return f, nil
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
	return f.locked(func() error {
		file, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, perms)
		if err != nil {
			return err
		}
		return errors.Join(f.write(file, decide), file.Close())
	})
}

// Runs fn while it holds the lock file of the file and then the data file lock where the platform takes one
// 1. the lock file is never renamed so a writer waiting on it writes to the file a rewrite left
// 2. the data file lock keeps out a process of a release that locked only the data file
// The order is fixed so two writers of this release never wait on each other
func (f File[T]) locked(fn func() error) error {
	held, err := os.OpenFile(f.path+".lock", os.O_CREATE|os.O_RDWR, perms)
	if err != nil {
		return fmt.Errorf("%s: %w", f.Name(), err)
	}
	err = lock(held)
	var data *os.File
	if err == nil {
		data, err = lockData(f.path)
	}
	if err == nil {
		err = fn()
	}
	if data != nil {
		err = errors.Join(err, data.Close())
	}
	if err = errors.Join(err, held.Close()); err != nil {
		return fmt.Errorf("%s: %w", f.Name(), err)
	}
	return nil
}

func (f File[T]) write(file *os.File, decide func() ([]T, error)) error {
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
	case len(tail) > 0 && torn[T](tail):
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
	var tail chunks
	for pos := info.Size(); pos > 0; {
		n := min(chunk, pos)
		pos -= n
		buf := make([]byte, n)
		if _, err := file.ReadAt(buf, pos); err != nil {
			return 0, nil, err
		}
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			return pos + int64(i) + 1, append(tail, buf[i+1:]).join(), nil
		}
		tail = append(tail, buf)
	}
	return 0, tail.join(), nil
}

// File order
// A missing file reads as empty only while its directory exists
// A directory moved or removed after Open is a store failure and never an empty store
// A line that fails to decode is left out and named by its number in ErrCorrupt
func (f File[T]) All() ([]T, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, f.missing()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name(), err)
	}
	all, err := Decode[T](data)
	if err != nil {
		return all, fmt.Errorf("%s %w", f.Name(), err)
	}
	return all, nil
}

func Decode[T any](data []byte) ([]T, error) {
	var all []T
	var bad []string
	line := 0
	for raw := range bytes.Lines(bytes.TrimPrefix(data, byteOrderMark)) {
		line++
		v, ok, err := decode[T](raw)
		if err != nil {
			bad = append(bad, fmt.Sprintf("line %d: %v", line, err))
		}
		if ok {
			all = append(all, v)
		}
	}
	if len(bad) > 0 {
		return all, fmt.Errorf("%w: %s", ErrCorrupt, strings.Join(bad, "; "))
	}
	return all, nil
}

// Newest first and read from the end of the file
// 1. a record that fails keep is skipped and limit zero means all
// 2. the read ends at the first record stop holds for and leaves that record out
// 3. a reader of recent records thus leaves the old part of the file unread
// 4. a line that fails to decode is left out and named by its byte offset in ErrCorrupt
// A nil stop reads to the start of the file
func (f File[T]) Newest(keep, stop func(T) bool, limit int) ([]T, error) {
	file, err := os.Open(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, f.missing()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name(), err)
	}
	defer func() { _ = file.Close() }()
	lines, err := newBackward(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name(), err)
	}
	var out []T
	var bad []string
	for {
		raw, offset, err := lines.next()
		if err != nil {
			return out, errors.Join(fmt.Errorf("%s: %w", f.Name(), err), f.corrupt(bad))
		}
		if raw == nil {
			break
		}
		if offset == 0 {
			raw = bytes.TrimPrefix(raw, byteOrderMark)
		}
		v, ok, err := decode[T](raw)
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
// 1. a line is judged as All judges it so a repaired file reads without ErrCorrupt
// 2. an unterminated last line stays for the next append to cut or keep
// 3. the moved lines are synced before the rename so a crash in between leaves them in both files and a later repair moves them again
func (f File[T]) Repair() (int, error) {
	var bad []byte
	keep := func(raw []byte) bool {
		if _, _, err := decode[T](raw); err != nil {
			bad = append(bad, raw...)
			return false
		}
		return true
	}
	return f.rewrite(func() (func([]byte) bool, error) { return keep, nil }, func() error {
		return writeSynced(f.path+".corrupt", os.O_CREATE|os.O_APPEND|os.O_WRONLY, bad)
	})
}

// Rewrites the file without the records the drop decide returns holds for and returns how many it left out
// 1. decide runs once under the lock right before the file is read so no writer slips in between
// 2. a line that fails to decode and an unterminated last line stay as they are for doctor and the next append
// 3. nothing is written when no record is dropped and a nil drop leaves the file unread
func (f File[T]) Remove(decide func() (func(T) bool, error)) (int, error) {
	return f.rewrite(func() (func([]byte) bool, error) {
		drop, err := decide()
		if err != nil || drop == nil {
			return nil, err
		}
		return func(raw []byte) bool {
			v, ok, err := decode[T](raw)
			return err != nil || !ok || !drop(v)
		}, nil
	}, func() error { return nil })
}

// Streams the lines keep holds for to the temp file under the lock and renames it over the file
// 1. sieve runs once under the lock and returns keep and a nil keep rewrites nothing
// 2. commit runs after the temp file is synced and before the rename
// 3. a missing file has nothing to rewrite
// 4. the directory is synced after the rename so the new name outlives a power loss
func (f File[T]) rewrite(sieve func() (func([]byte) bool, error), commit func() error) (int, error) {
	removed := 0
	err := f.locked(func() error {
		if err := f.recover(); err != nil {
			return err
		}
		keep, err := sieve()
		if err != nil || keep == nil {
			return err
		}
		removed, err = f.sift(keep)
		if err != nil || removed == 0 {
			return err
		}
		if err := commit(); err != nil {
			return errors.Join(err, os.Remove(f.temp()))
		}
		return f.replace()
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// Writes the lines keep holds for to the synced temp file and returns how many it left out
// No temp file stays when it leaves none out
func (f File[T]) sift(keep func([]byte) bool) (int, error) {
	in, err := os.Open(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, f.missing()
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(f.temp(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perms)
	if err != nil {
		return 0, err
	}
	removed, err := copyKept(bufio.NewReaderSize(in, rewriteBuffer), out, keep)
	if err == nil && removed > 0 {
		err = out.Sync()
	}
	err = errors.Join(err, out.Close())
	if err != nil || removed == 0 {
		return 0, errors.Join(err, os.Remove(f.temp()))
	}
	return removed, nil
}

// Copies the lines keep holds for and returns how many it left out
// A byte order mark before the first line is dropped as All drops it
func copyKept(in *bufio.Reader, out io.Writer, keep func([]byte) bool) (int, error) {
	w := bufio.NewWriterSize(out, rewriteBuffer)
	removed := 0
	var line []byte
	for first := true; ; first = false {
		var err error
		line, err = readLine(in, line[:0])
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		raw := line
		if first {
			raw = bytes.TrimPrefix(raw, byteOrderMark)
		}
		switch {
		case len(raw) == 0:
		case keep(raw):
			if _, werr := w.Write(raw); werr != nil {
				return 0, werr
			}
		default:
			removed++
		}
		if errors.Is(err, io.EOF) {
			return removed, w.Flush()
		}
	}
}

// One line with its newline when it has one appended to buf
// A line longer than the buffer of the reader is read whole
func readLine(r *bufio.Reader, buf []byte) ([]byte, error) {
	for {
		part, err := r.ReadSlice('\n')
		buf = append(buf, part...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return buf, err
		}
	}
}

// Renames the synced temp file over the file and syncs the directory
func (f File[T]) replace() error {
	if err := beforeReplace(); err != nil {
		return err
	}
	if err := rename(f.temp(), f.path); err != nil {
		return errors.Join(err, os.Remove(f.temp()))
	}
	return syncDir(filepath.Dir(f.path))
}

// The new content of a rewrite until the rename
// A fixed name so the next lock holder finds what a crash left
func (f File[T]) temp() string {
	return f.path + ".tmp"
}

// The whole file an earlier release kept while it rewrote the file in place
func (f File[T]) backup() string {
	return f.path + ".bak"
}

// Whether a crashed rewrite left a temp file or a backup
func (f File[T]) leftover() bool {
	for _, path := range []string{f.temp(), f.backup()} {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return false
}

// Recovers what a crashed rewrite left while the lock is held
// 1. a temp file never reached the rename so the file holds every record and the temp file goes
// 2. a backup of an earlier release holds the file as it was before a rewrite in place that may have stopped halfway
// The file becomes the backup followed by the lines appended since so no record is lost
// A record the rewrite was to remove comes back and the next prune removes it again
func (f File[T]) recover() error {
	if err := os.Remove(f.temp()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	saved, err := os.ReadFile(f.backup())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := os.ReadFile(f.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeSynced(f.temp(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.merge(saved, current)); err != nil {
		return err
	}
	if err := f.replace(); err != nil {
		return err
	}
	if err := os.Remove(f.backup()); err != nil {
		return err
	}
	return syncDir(filepath.Dir(f.path))
}

// The lines of the backup and then the lines of the current file the backup lacks
// 1. lines compare without their newline and each backup line accounts for one equal current line
// 2. a torn unterminated last line of either is left out
// A torn backup means the file was not touched yet and a torn file is a part of a backup line
func (f File[T]) merge(saved, current []byte) []byte {
	seen := map[string]int{}
	var out []byte
	for _, raw := range f.whole(saved) {
		seen[string(raw)]++
		out = append(append(out, raw...), '\n')
	}
	for _, raw := range f.whole(current) {
		if seen[string(raw)] > 0 {
			seen[string(raw)]--
			continue
		}
		out = append(append(out, raw...), '\n')
	}
	return out
}

// The lines of the content without their newline and without a torn unterminated last line
// A byte order mark is dropped as All drops it so a first line written with one and without one compare equal
func (f File[T]) whole(content []byte) [][]byte {
	var out [][]byte
	for raw := range bytes.Lines(bytes.TrimPrefix(content, byteOrderMark)) {
		if !bytes.HasSuffix(raw, []byte{'\n'}) && torn[T](raw) {
			break
		}
		out = append(out, bytes.TrimSuffix(raw, []byte{'\n'}))
	}
	return out
}

// Writes the bytes to the path opened with the flags and syncs them before closing
func writeSynced(path string, flags int, data []byte) error {
	out, err := os.OpenFile(path, flags, perms)
	if err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		return errors.Join(err, out.Close())
	}
	return errors.Join(out.Sync(), out.Close())
}

// The record of one line and whether there was one
// 1. a blank line holds none
// 2. an unterminated line that fails to decode is a write in flight and holds none without an error
func decode[T any](raw []byte) (T, bool, error) {
	var v T
	if !bytes.HasSuffix(raw, []byte{'\n'}) && torn[T](raw) {
		return v, false, nil
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return v, false, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, false, err
	}
	return v, true, nil
}

func (f File[T]) corrupt(bad []string) error {
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%s %w: %s", f.Name(), ErrCorrupt, strings.Join(bad, "; "))
}

// A file not written yet reads as empty while its directory exists
func (f File[T]) missing() error {
	if _, err := os.Stat(filepath.Dir(f.path)); err != nil {
		return fmt.Errorf("%s: %w", f.Name(), err)
	}
	return nil
}

// Whether an unterminated last line fails to decode as a record
// A file holding one record after a byte order mark keeps it
func torn[T any](tail []byte) bool {
	var v T
	return json.Unmarshal(bytes.TrimPrefix(tail, byteOrderMark), &v) != nil
}

func (f File[T]) Name() string {
	return filepath.Base(f.path)
}

// How many records the file holds and the error naming its corrupt lines
func (f File[T]) Check() (int, error) {
	all, err := f.All()
	return len(all), err
}
