package file

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	"go.yaml.in/yaml/v3"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Opens and closes the front matter of a procedure
const frontMatterFence = "---"

// The scope from the front matter and the paragraphs of the rest
// 1. the front matter is stripped before the split so a file with and without it has the same paragraph ids
// 2. a byte order mark is dropped because it would hide the opening fence and the first heading
func parseProcedure(fileName, content string) (evidence.Procedure, error) {
	scope, body, err := frontMatter(strings.TrimPrefix(content, byteOrderMark))
	if err != nil {
		return evidence.Procedure{}, fmt.Errorf("%s: %w", fileName, err)
	}
	return evidence.Procedure{
		Slug:  strings.TrimSuffix(fileName, procedureExt),
		File:  fileName,
		Scope: scope, Paragraphs: splitParagraphs(fileName, body),
	}, nil
}

// The lines between a first line `---` and the next `---`
// 1. a file that does not open with the fence has no front matter and an empty scope
// 2. only change_contexts and metrics are known and the source checks the change contexts against the declared ones
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
	return scope, body, nil
}

// Split the raw lines by the blocks a CommonMark parser finds
// 1. only a heading at the top level replaces the path at its level and drops deeper levels
// 2. a blank line inside fenced code or an HTML block stays and any other blank line ends a paragraph
// 3. the index restarts at 1 for every heading and counts on under a repeated heading path so no id names two paragraphs
func splitParagraphs(fileName, content string) []evidence.Paragraph {
	src := newSource(content)
	doc := src.parse()
	s := splitter{
		file: fileName, slug: strings.TrimSuffix(fileName, procedureExt),
		headings: src.headings(doc), kept: src.kept(doc), skip: -1,
		ids: map[evidence.ParagraphID]struct{}{},
	}
	for i, line := range strings.Split(content, "\n") {
		s.read(i, line)
	}
	s.flush()
	return s.out
}

// The procedure body with the offset where each of its lines starts
type source struct {
	text   []byte
	starts []int
}

func newSource(content string) source {
	starts := []int{0}
	for i := range len(content) {
		if content[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return source{text: []byte(content), starts: starts}
}

// A parser per call because the parser does not document that it is safe for concurrent use
// Tables are parsed so a rule right under a table is a break and never a setext underline
func (s source) parse() ast.Node {
	return goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(s.text))
}

// Index of the line that holds the byte at offset
func (s source) line(offset int) int {
	return sort.SearchInts(s.starts, offset+1) - 1
}

// A heading that moves the path keyed by the line it starts on
// A heading nested in a list or a quote and an ATX heading without a title stay text
func (s source) headings(doc ast.Node) map[int]heading {
	out := map[int]heading{}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		h, ok := n.(*ast.Heading)
		if !ok || h.Lines().Len() == 0 {
			continue
		}
		parts := make([]string, h.Lines().Len())
		for i := range parts {
			segment := h.Lines().At(i)
			parts[i] = string(segment.Value(s.text))
		}
		title := strings.TrimSpace(strings.Join(parts, " "))
		last := s.line(h.Lines().At(h.Lines().Len() - 1).Start)
		// A setext heading starts where its text starts while an ATX heading starts at its hashes
		if h.Lines().At(0).Start == h.Pos() {
			title = strings.Join(strings.Fields(title), " ")
			last++
		}
		out[s.line(h.Pos())] = heading{level: h.Level, title: title, last: last}
	}
	return out
}

// Lines of fenced code and HTML blocks where a blank line does not end the paragraph
// Indented code is left out so a blank line inside it still splits as it always did
func (s source) kept(doc ast.Node) map[int]bool {
	out := map[int]bool{}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || (n.Kind() != ast.KindFencedCodeBlock && n.Kind() != ast.KindHTMLBlock) {
			return ast.WalkContinue, nil
		}
		for i := range n.Lines().Len() {
			out[s.line(n.Lines().At(i).Start)] = true
		}
		return ast.WalkSkipChildren, nil
	})
	return out
}

type heading struct {
	level int
	title string
	// Last line the heading takes
	// A setext heading also takes its underline
	last int
}

// Position inside one procedure while its lines are read in order
type splitter struct {
	file     string
	slug     string
	headings map[int]heading
	kept     map[int]bool
	// Last line of the heading being passed over
	skip int
	path []string
	buf  []string
	// Paragraphs under the current heading so far
	index int
	ids   map[evidence.ParagraphID]struct{}
	out   []evidence.Paragraph
}

func (s *splitter) read(i int, line string) {
	if h, ok := s.headings[i]; ok {
		s.enter(h.level, h.title)
		s.skip = h.last
		return
	}
	if i <= s.skip {
		return
	}
	if strings.TrimSpace(line) == "" && !s.kept[i] {
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
	var id evidence.ParagraphID
	for {
		s.index++
		id = evidence.NewParagraphID(s.slug, s.path, s.index)
		if _, taken := s.ids[id]; !taken {
			break
		}
	}
	s.ids[id] = struct{}{}
	s.out = append(s.out, evidence.Paragraph{ID: id, File: s.file, Path: slices.Clone(s.path), Text: text})
}
