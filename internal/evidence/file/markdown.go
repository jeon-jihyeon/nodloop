package file

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Opens and closes the front matter of a procedure
const frontMatterFence = "---"

// The scope from the front matter and the paragraphs of the rest
// The front matter is stripped before the split so a file with and without it has the same paragraph ids
func parseProcedure(fileName, content string) (evidence.Procedure, error) {
	scope, body, err := frontMatter(content)
	if err != nil {
		return evidence.Procedure{}, fmt.Errorf("%s: %w", fileName, err)
	}
	return evidence.Procedure{
		Slug: strings.TrimSuffix(fileName, ".md"), File: fileName, Scope: scope, Paragraphs: splitParagraphs(fileName, body),
	}, nil
}

// The lines between a first line `---` and the next `---`
// 1. a file that does not open with the fence has no front matter and an empty scope
// 2. only change_contexts and metrics are known and every change context must be one the evidence layer knows
// 3. a fence after the first line is text
func frontMatter(content string) (evidence.Scope, string, error) {
	first, rest, _ := strings.Cut(content, "\n")
	if strings.TrimSpace(first) != frontMatterFence {
		return evidence.Scope{}, content, nil
	}
	end := 0
	for line := range strings.SplitAfterSeq(rest, "\n") {
		if strings.TrimSpace(line) == frontMatterFence {
			return decodeScope(rest[:end], rest[end+len(line):])
		}
		end += len(line)
	}
	return evidence.Scope{}, "", fmt.Errorf("%w: front matter is not closed", evidence.ErrMalformed)
}

func decodeScope(head, body string) (evidence.Scope, string, error) {
	var scope evidence.Scope
	dec := yaml.NewDecoder(strings.NewReader(head))
	dec.KnownFields(true)
	if err := dec.Decode(&scope); err != nil && !errors.Is(err, io.EOF) {
		return evidence.Scope{}, "", fmt.Errorf("%w: front matter: %w", evidence.ErrMalformed, err)
	}
	for _, c := range scope.ChangeContexts {
		if !c.Valid() {
			return evidence.Scope{}, "", fmt.Errorf("%w: %q", evidence.ErrUnknownContext, c)
		}
	}
	return scope, body, nil
}

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

// Position inside one procedure while its lines are read in order
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
