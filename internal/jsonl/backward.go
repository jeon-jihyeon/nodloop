package jsonl

import (
	"bytes"
	"os"
)

// The lines of a file from the last to the first, each with its newline when it has one
type backward struct {
	file *os.File
	// Where the bytes not read yet end
	pos int64
	// Bytes read and not returned yet, starting at pos
	buf []byte
}

func newBackward(file *os.File) (*backward, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return &backward{file: file, pos: info.Size()}, nil
}

// The last line not returned yet and the offset it starts at, or nil once the first line was returned
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

// Puts the chunk before the bytes read so far in front of them
func (b *backward) read() error {
	n := min(chunk, b.pos)
	b.pos -= n
	buf := make([]byte, n, int(n)+len(b.buf))
	if _, err := b.file.ReadAt(buf, b.pos); err != nil {
		return err
	}
	b.buf = append(buf, b.buf...)
	return nil
}
