package file

import (
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Split by heading and blank line
// 1. a heading replaces the path at its level and drops deeper levels
// 2. blank lines separate paragraphs inside a section
// 3. fenced code stays in one paragraph
// 4. the index restarts at 1 for every heading
func splitParagraphs(fileName, content string) []evidence.Paragraph {
	s := splitter{file: fileName, slug: strings.TrimSuffix(fileName, ".md")}
	for line := range strings.SplitSeq(content, "\n") {
		s.read(line)
	}
	s.flush()
	return s.out
}

// Position inside one runbook while its lines are read in order
type splitter struct {
	file string
	slug string
	path []string
	buf  []string
	// Paragraphs under the current heading so far
	index  int
	fenced bool
	out    []evidence.Paragraph
}

func (s *splitter) read(line string) {
	trimmed := strings.TrimSpace(line)
	fence := strings.HasPrefix(trimmed, "```")
	if fence {
		s.fenced = !s.fenced
	}
	if fence || s.fenced {
		s.buf = append(s.buf, line)
		return
	}
	if level, title, ok := heading(trimmed); ok {
		s.enter(level, title)
		return
	}
	if trimmed == "" {
		s.flush()
		return
	}
	s.buf = append(s.buf, line)
}

func (s *splitter) enter(level int, title string) {
	s.flush()
	if level <= len(s.path) {
		s.path = s.path[:level-1]
	}
	for len(s.path) < level-1 {
		s.path = append(s.path, "")
	}
	s.path = append(s.path, title)
	s.index = 0
}

func (s *splitter) flush() {
	text := strings.TrimSpace(strings.Join(s.buf, "\n"))
	s.buf = s.buf[:0]
	if text == "" {
		return
	}
	s.index++
	s.out = append(s.out, evidence.Paragraph{
		ID:   evidence.NewParagraphID(s.slug, s.path, s.index),
		File: s.file,
		Path: slices.Clone(s.path),
		Text: text,
	})
}

func heading(line string) (level int, title string, ok bool) {
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(line[level:]), true
}
