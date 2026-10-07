package jsonl

import (
	"bytes"
	"os"
	"slices"
)

// The lines of a file from the last to the first
// Each line keeps its newline when it has one
type backward struct {
	file *os.File
	// Where the bytes not read yet end
	pos int64
	// Bytes read and not returned yet
	// They start at pos
	buf []byte
}

func newBackward(file *os.File) (*backward, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return &backward{file: file, pos: info.Size()}, nil
}

// The last line not returned yet and the offset it starts at
// nil once the first line was returned
func (b *backward) next() ([]byte, int64, error) {
	for {
		if len(b.buf) == 0 && b.pos == 0 {
			return nil, 0, nil
		}
		body := bytes.TrimSuffix(b.buf, []byte{'\n'})
		if i := bytes.LastIndexByte(body, '\n'); i >= 0 {
			line := b.buf[i+1:]
			b.buf = b.buf[:i+1]
			return line, b.pos + int64(i) + 1, nil
		}
		if b.pos == 0 {
			line := b.buf
			b.buf = nil
			return line, 0, nil
		}
		if err := b.read(); err != nil {
			return nil, 0, err
		}
	}
}

// Puts the bytes before those read so far in front of them
// 1. it reads chunk after chunk until one holds a newline or the file starts
// 2. the chunks are joined once so a line longer than a chunk is copied once and not once per chunk
func (b *backward) read() error {
	parts := chunks{b.buf}
	for b.pos > 0 {
		n := min(chunk, b.pos)
		b.pos -= n
		buf := make([]byte, n)
		if _, err := b.file.ReadAt(buf, b.pos); err != nil {
			return err
		}
		parts = append(parts, buf)
		if bytes.IndexByte(buf, '\n') >= 0 {
			break
		}
	}
	b.buf = parts.join()
	return nil
}

// Pieces of a file read from its end with the last piece first
type chunks [][]byte

// The pieces in file order as one slice
func (c chunks) join() []byte {
	ordered := slices.Clone(c)
	slices.Reverse(ordered)
	return bytes.Join(ordered, nil)
}
