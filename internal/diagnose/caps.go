package diagnose

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// How much a review context carries
// Every size counts runes so a cut never splits a character and a count means the same on any language
const (
	// A safety cap derived from the smallest supported model context and shared with the knowledge folder check
	knowledgeChars = knowledge.ReviewChars
	// A quality value tuned on live runs so every correction arrives whole while long original reviews give way
	// It leaves room for the leads that say what each correction changed
	exampleChars = 7000
	// Approval never grows a folder past it so only an imported ledger or one approved before the item cap overflows it
	knowledgeItems = knowledge.ReviewItems
	// One list Claude Code can show on one screen
	// No approval bounds corrections so the closest and newest come first
	exampleItems = 10
)

// Runes of the knowledge and examples sections as sent with heading and cut marks and notice
// Also the runes left out summed over the items of each section
type sectionChars struct {
	Knowledge int `json:"knowledge"`
	Examples  int `json:"examples"`
}

// Runes of every section of a review prompt as sent
// Procedures counts the paragraph section and not the observations
type promptChars struct {
	Procedures int `json:"procedures"`
	sectionChars
}

// Renders the chosen items within the caps
// Returns the select trace input that names every item with its size and cut and the text that reached the model
// known holds the paragraph ids the review may cite so an example names the ones it cannot
func fit(selector Selector, items []chosenKnowledge, examples []chosenExample, known citable) (selectInput, Selection) {
	selected := selectInput{Selector: selector, Knowledge: []AppliedKnowledge{}, Examples: []appliedExample{}}
	knowledgeTexts := make([]block, len(items))
	for i, k := range items {
		knowledgeTexts[i] = block{body: k.Text()}
	}
	knowledgeText, knowledgeSizes := budget(knowledgeChars).section(knowledgeHeading, knowledgeNotice, knowledgeTexts)
	for i, k := range items {
		given := knowledgeSizes[i]
		selected.Knowledge = append(selected.Knowledge, AppliedKnowledge{
			ID: k.ID, Version: k.Version, Reason: k.Reason, Chars: given.chars, OmittedChars: given.omitted, Cut: given.cut(),
		})
	}
	exampleTexts := make([]block, len(examples))
	for i, e := range examples {
		exampleTexts[i] = e.render(i+1, known)
	}
	exampleText, exampleSizes := budget(exampleChars).section(examplesHeading, examplesNotice, exampleTexts)
	for i, e := range examples {
		given := exampleSizes[i]
		selected.Examples = append(selected.Examples, appliedExample{
			TraceID: e.TraceID, Reason: e.Why, Chars: given.chars, OmittedChars: given.omitted, Cut: given.cut(),
		})
	}
	selected.Omitted = knowledgeSizes.cut() || exampleSizes.cut()
	selected.Chars = sectionChars{Knowledge: utf8.RuneCountInString(knowledgeText), Examples: utf8.RuneCountInString(exampleText)}
	selected.OmittedChars = sectionChars{Knowledge: knowledgeSizes.omitted(), Examples: exampleSizes.omitted()}
	return selected, Selection{Applied: selected.givenKnowledge(), Omitted: selected.Omitted, Text: knowledgeText + exampleText}
}

const (
	knowledgeHeading = "\n## Approved knowledge\n\nVerified working knowledge for events like this one. It supplements the procedure and never overrides an observation\n"
	knowledgeNotice  = "\nSome approved items were cut to fit the knowledge cap. A cut item ends with the count of characters left out\n"
	examplesHeading  = "\n## Examples\n\nEarlier reviews and how the reviewer judged them. Each gives the verdict, the reason, what the correction changed and the corrected review first and the original review last\n"
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

// Runes one section of the selected text may carry
// Every selected text gets its share so none is dropped whole while another reaches the model uncut
type budget int

// The heading and every text within its share and the notice when a text was cut
// Nothing without texts
// Returns the section and the runes given and left out per text
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
		out[i] = size{chars: utf8.RuneCountInString(given), omitted: omitted}
	}
	if out.cut() {
		w.WriteString(notice)
	}
	return w.String(), out
}

// Runes each text may carry
// 1. every lead and body fits whole first when together they fit the budget
// 2. the tails then share what is left
// 3. when the leads and bodies alone outgrow the budget they share it and every tail is left out
// 4. the shares never add up to more than the budget
func (b budget) shares(texts []block) []share {
	cores := make([]int, len(texts))
	tails := make([]int, len(texts))
	used := 0
	for i, t := range texts {
		cores[i], tails[i] = utf8.RuneCountInString(t.lead)+utf8.RuneCountInString(t.body), utf8.RuneCountInString(t.tail)
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

// Runes each need may take
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

// Runes one text may carry
type share int

// The text within the share and how many of its runes were left out
// 1. a text cut partway ends with the cut mark and the mark counts against the share
// 2. a share that holds the lead and body keeps both whole and cuts the tail
// 3. a tail whose room cannot hold the mark is left out whole without a mark
// 4. a smaller share cuts the body and leaves out the tail
// 5. a share too small for the lead and the mark gives nothing and the whole text counts as left out
// 6. the mark is sized for the most that can be left out so the share is never exceeded
func (s share) cut(text block) (string, int) {
	lead, body, tail := utf8.RuneCountInString(text.lead), utf8.RuneCountInString(text.body), utf8.RuneCountInString(text.tail)
	total := lead + body + tail
	if total <= int(s) {
		return text.lead + text.body + text.tail, 0
	}
	if core := lead + body; int(s) >= core {
		room := int(s) - core - utf8.RuneCountInString(fmt.Sprintf(cutMark, tail))
		if room <= 0 {
			return text.lead + text.body, tail
		}
		return share(room).trim(text.lead+text.body, text.tail, total)
	}
	room := int(s) - lead - utf8.RuneCountInString(fmt.Sprintf(cutMark, total-lead))
	if room < 0 {
		return "", total
	}
	return share(room).trim(text.lead, text.body, total)
}

// The kept text and the first runes of the open part within this share and the cut mark
// Nothing when neither the kept text nor the cut part has a rune
func (s share) trim(kept, open string, total int) (string, int) {
	end := len(open)
	n := 0
	for i := range open {
		if n == int(s) {
			end = i
			break
		}
		n++
	}
	given := kept + open[:end]
	if given == "" {
		return "", total
	}
	omitted := total - utf8.RuneCountInString(given)
	return given + fmt.Sprintf(cutMark, omitted), omitted
}

// Runes of one text that reached the model and that were left out
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

// Runes left out over the texts
func (ss sizes) omitted() int {
	n := 0
	for _, s := range ss {
		n += s.omitted
	}
	return n
}
