package jsonl

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"strconv"
	"strings"
)

// Where a read in file order ended: the byte offset after the last line it read and the FNV-1a hash of that line
// The zero position is the start of the file
type position struct {
	offset int64
	sum    uint64
}

// The opaque form a reader passes back and empty at the start of the file
func (p position) String() string {
	if p.offset == 0 {
		return ""
	}
	return strconv.FormatInt(p.offset, 10) + ":" + strconv.FormatUint(p.sum, 16)
}

// A form no read answered names no line and fails with ErrPositionGone
func parsePosition(s string) (position, error) {
	if s == "" {
		return position{}, nil
	}
	at, sum, ok := strings.Cut(s, ":")
	offset, err := strconv.ParseInt(at, 10, 64)
	hash, sumErr := strconv.ParseUint(sum, 16, 64)
	if !ok || err != nil || sumErr != nil || offset <= 0 {
		return position{}, fmt.Errorf("%w: %q", ErrPositionGone, s)
	}
	return position{offset: offset, sum: hash}, nil
}

// The hash a position keeps of a line without its surrounding space
func lineSum(content []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(bytes.TrimSpace(content))
	return h.Sum64()
}

// The records of the lines after the position in file order and the position after the last line read
// 1. the empty position starts at the first line
// 2. a rewrite that moved the line of the position resumes after that line found from the start
// 3. a position whose line no rewrite kept fails with ErrPositionGone
// 4. a record that fails keep is skipped and limit zero means all
// 5. an unterminated last line that fails to decode ends the read before it since it is a write in flight
// 6. a line that fails to decode is left out and named by its byte offset in ErrCorrupt
// A position ends after a line that holds something so a newline an append adds after a kept tail never moves it
func (f File[T]) After(from string, keep func(T) bool, limit int) ([]T, string, error) {
	at, err := parsePosition(from)
	if err != nil {
		return nil, from, fmt.Errorf("%s: %w", f.Name(), err)
	}
	file, err := os.Open(f.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := f.missing(); err != nil {
			return nil, from, err
		}
		if at.offset == 0 {
			return nil, from, nil
		}
		return nil, from, fmt.Errorf("%s: %w: %q", f.Name(), ErrPositionGone, from)
	case err != nil:
		return nil, from, fmt.Errorf("%s: %w", f.Name(), err)
	}
	defer func() { _ = file.Close() }()
	start, err := f.resume(file, at)
	if err != nil {
		return nil, from, err
	}
	return f.forward(file, start, keep, limit)
}

// The position the read goes on from
// 1. the given one while the line that ends at its offset has its hash
// 2. else the end of the first line from the start with that hash
func (f File[T]) resume(file *os.File, at position) (position, error) {
	if at.offset == 0 {
		return at, nil
	}
	raw, offset, err := (&backward{file: file, pos: at.offset}).next()
	if err == nil && raw != nil && lineSum(f.content(raw, offset)) == at.sum {
		return at, nil
	}
	r := bufio.NewReaderSize(io.NewSectionReader(file, 0, 1<<62), chunk)
	var line []byte
	for end := int64(0); ; {
		line, err = readLine(r, line[:0])
		if err != nil && !errors.Is(err, io.EOF) {
			return position{}, fmt.Errorf("%s: %w", f.Name(), err)
		}
		start := end
		end += int64(len(line))
		if len(line) > 0 && lineSum(f.content(line, start)) == at.sum {
			return position{offset: end, sum: at.sum}, nil
		}
		if errors.Is(err, io.EOF) {
			return position{}, fmt.Errorf("%s: %w", f.Name(), ErrPositionGone)
		}
	}
}

// The records from the position on up to limit and the position after the last line that holds something
func (f File[T]) forward(file *os.File, from position, keep func(T) bool, limit int) ([]T, string, error) {
	r := bufio.NewReaderSize(io.NewSectionReader(file, from.offset, 1<<62), chunk)
	end, offset := from, from.offset
	var out []T
	var bad []string
	var line []byte
	for limit <= 0 || len(out) < limit {
		var err error
		line, err = readLine(r, line[:0])
		if err != nil && !errors.Is(err, io.EOF) {
			return out, end.String(), errors.Join(fmt.Errorf("%s: %w", f.Name(), err), f.corrupt(bad))
		}
		raw, start := f.content(line, offset), offset
		offset += int64(len(line))
		switch {
		case len(bytes.TrimSpace(raw)) == 0:
		case !bytes.HasSuffix(line, []byte{'\n'}) && torn[T](raw):
			return out, end.String(), f.corrupt(bad)
		default:
			end = position{offset: offset, sum: lineSum(raw)}
			v, ok, decodeErr := decode[T](raw)
			if decodeErr != nil {
				bad = append(bad, fmt.Sprintf("byte %d: %v", start, decodeErr))
			}
			if ok && keep(v) {
				out = append(out, v)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return out, end.String(), f.corrupt(bad)
}

// The line without a byte order mark when it starts the file as All reads it
func (File[T]) content(line []byte, offset int64) []byte {
	if offset == 0 {
		return bytes.TrimPrefix(line, byteOrderMark)
	}
	return line
}
