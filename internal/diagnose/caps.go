package diagnose

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Sized so knowledge and examples together stay under a fifth of a demo context of about 50k characters
// Ten candidates fit one screen when Claude Code lists them for the user
const (
	defaultKnowledgeChars = 4000
	defaultExampleChars   = 6000
	defaultCandidates     = 10
)

// How much a review context carries
// It is the limits section of policy.yaml where zero means the default
// Every selected item gets a share of its cap and a cut is marked in the text and recorded
type Limits struct {
	KnowledgeChars int `yaml:"knowledge_chars"`
	ExampleChars   int `yaml:"example_chars"`
	Candidates     int `yaml:"candidates"`
}

// Reads only the limits section so the analyzers stay with analysis and one file serves both
// A file without the section gives zero limits
func LoadLimits(b []byte) (Limits, error) {
	var file struct {
		Limits Limits `yaml:"limits"`
	}
	if err := yaml.Unmarshal(b, &file); err != nil {
		return Limits{}, fmt.Errorf("%w: %w", ErrBadLimits, err)
	}
	return file.Limits, nil
}

// Defaults fill the limits the policy file leaves out
func (l Limits) withDefaults() Limits {
	if l.KnowledgeChars <= 0 {
		l.KnowledgeChars = defaultKnowledgeChars
	}
	if l.ExampleChars <= 0 {
		l.ExampleChars = defaultExampleChars
	}
	if l.Candidates <= 0 {
		l.Candidates = defaultCandidates
	}
	return l
}

// Renders the chosen items within the caps
// Returns the select trace input that names every item with its size and cut and the text that reached the model
func (l Limits) fit(selector Selector, items []chosenKnowledge, examples []chosenExample) (selectInput, Selection) {
	selected := selectInput{Selector: selector, Knowledge: []AppliedKnowledge{}, Examples: []appliedExample{}}
	knowledgeTexts := make([]block, len(items))
	for i, k := range items {
		knowledgeTexts[i] = block{body: k.render()}
	}
	knowledgeText, knowledgeSizes := budget(l.KnowledgeChars).section(knowledgeHeading, knowledgeNotice, knowledgeTexts)
	for i, k := range items {
		given := knowledgeSizes[i]
		selected.Knowledge = append(selected.Knowledge, AppliedKnowledge{
			ID: k.ID, Version: k.Version, Reason: k.Reason, Chars: given.chars, OmittedChars: given.omitted, Cut: given.cut(),
		})
	}
	exampleTexts := make([]block, len(examples))
	for i, e := range examples {
		exampleTexts[i] = e.render(i + 1)
	}
	exampleText, exampleSizes := budget(l.ExampleChars).section(examplesHeading, examplesNotice, exampleTexts)
	for i, e := range examples {
		given := exampleSizes[i]
		selected.Examples = append(selected.Examples, appliedExample{
			TraceID: e.TraceID, Reason: e.Why, Chars: given.chars, OmittedChars: given.omitted, Cut: given.cut(),
		})
	}
	selected.Omitted = knowledgeSizes.cut() || exampleSizes.cut()
	return selected, Selection{Applied: selected.givenKnowledge(), Omitted: selected.Omitted, Text: knowledgeText + exampleText}
}

const (
	knowledgeHeading = "\n## Approved knowledge\n\nVerified working knowledge for events like this one. It supplements the runbook and never overrides an observation\n"
	knowledgeNotice  = "\nSome approved items were cut to fit the knowledge cap. A cut item ends with the count of characters left out\n"
	examplesHeading  = "\n## Examples\n\nEarlier reviews and how the reviewer judged them. Each gives the verdict, the reason and the corrected review first and the original review last\n"
	examplesNotice   = "\nSome examples were cut to fit the example cap. The original review gives way first so an example may end without one. An example cut partway ends with the count of characters left out\n"
	// Ends a text cut partway
	cutMark = "\n[%d characters omitted at the cap]\n"
)

// One selected text as it reaches the model
type block struct {
	// Never cut so a share too small for it gives nothing
	lead string
	// Cut only when the leads and bodies of all texts outgrow the budget
	body string
	// Gets only what the leads and bodies leave
	// Left out whole without a mark when its room cannot hold the mark
	tail string
}

// Characters one section of the selected text may carry
// Every selected text gets its share so none is dropped whole while another reaches the model uncut
type budget int

// The heading and every text within its share and the notice when a text was cut
// Nothing without texts
// Returns the section and the characters given and left out per text
func (b budget) section(heading, notice string, texts []block) (string, sizes) {
	if len(texts) == 0 {
		return "", nil
	}
	var w strings.Builder
	w.WriteString(heading)
	out := make(sizes, len(texts))
	for i, s := range b.shares(texts) {
		given, omitted := s.cut(texts[i])
		w.WriteString(given)
		out[i] = size{chars: len(given), omitted: omitted}
	}
	if out.cut() {
		w.WriteString(notice)
	}
	return w.String(), out
}

// Characters each text may carry
// 1. every lead and body fits whole first when together they fit the budget
// 2. the tails then share what is left
// 3. when the leads and bodies alone outgrow the budget they share it and every tail is left out
// 4. the shares never add up to more than the budget
func (b budget) shares(texts []block) []share {
	cores := make([]int, len(texts))
	tails := make([]int, len(texts))
	used := 0
	for i, t := range texts {
		cores[i], tails[i] = len(t.lead)+len(t.body), len(t.tail)
		used += cores[i]
	}
	if used > int(b) {
		return b.fair(cores)
	}
	out := budget(int(b) - used).fair(tails)
	for i := range out {
		out[i] += share(cores[i])
	}
	return out
}

// Characters each need may take
// 1. smaller needs take their full size first
// 2. what they leave is split evenly across the larger needs
func (b budget) fair(needs []int) []share {
	order := make([]int, len(needs))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(x, y int) int { return cmp.Compare(needs[x], needs[y]) })
	out := make([]share, len(needs))
	left := int(b)
	for n, i := range order {
		s := min(needs[i], left/(len(order)-n))
		out[i] = share(s)
		left -= s
	}
	return out
}

// Characters one text may carry
type share int

// The text within the share and how many of its characters were left out
// 1. a text cut partway ends with the cut mark and the mark counts against the share
// 2. a share that holds the lead and body keeps both whole and cuts the tail
// 3. a tail whose room cannot hold the mark is left out whole without a mark
// 4. a smaller share cuts the body and leaves out the tail
// 5. a share too small for the lead and the mark gives nothing and the whole text counts as left out
// 6. the mark is sized for the most that can be left out so the share is never exceeded
func (s share) cut(text block) (string, int) {
	total := len(text.lead) + len(text.body) + len(text.tail)
	if total <= int(s) {
		return text.lead + text.body + text.tail, 0
	}
	if core := len(text.lead) + len(text.body); int(s) >= core {
		room := int(s) - core - len(fmt.Sprintf(cutMark, len(text.tail)))
		if room <= 0 {
			return text.lead + text.body, len(text.tail)
		}
		return share(room).trim(text.lead+text.body, text.tail, total)
	}
	room := int(s) - len(text.lead) - len(fmt.Sprintf(cutMark, total-len(text.lead)))
	if room < 0 {
		return "", total
	}
	return share(room).trim(text.lead, text.body, total)
}

// The kept text and the open part cut within this share and the cut mark
// The cut backs off to a rune start so it never splits a multibyte character
// Nothing when neither the kept text nor the cut part has a character
func (s share) trim(kept, open string, total int) (string, int) {
	n := int(s)
	for n > 0 && !utf8.RuneStart(open[n]) {
		n--
	}
	given := kept + open[:n]
	if given == "" {
		return "", total
	}
	omitted := total - len(given)
	return given + fmt.Sprintf(cutMark, omitted), omitted
}

// Characters of one text that reached the model and that were left out
type size struct {
	chars, omitted int
}

func (s size) cut() bool {
	return s.omitted > 0
}

// Sizes of the texts of one section in selection order
type sizes []size

func (ss sizes) cut() bool {
	return slices.ContainsFunc(ss, size.cut)
}
