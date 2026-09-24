package diagnose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// How much approved knowledge the batch path lets into a review
type KnowledgeMode string

const (
	KnowledgeNone     KnowledgeMode = "none"     // no knowledge in the context
	KnowledgeSelected KnowledgeMode = "selected" // applicable items chosen by scope
	KnowledgeAll      KnowledgeMode = "all"      // every approved item regardless of scope as the memory file baseline
)

func (m KnowledgeMode) valid() bool {
	return m == KnowledgeNone || m == KnowledgeSelected || m == KnowledgeAll
}

// Who chose the items a review used
type Selector string

const (
	SelectByCode   Selector = "code"
	SelectByClaude Selector = "claude"
)

// One line per offered item so the model judges relevance without the text
type KnowledgeCandidate struct {
	ID      string          `json:"id"`
	Version int             `json:"version"`
	Kind    knowledge.Kind  `json:"kind"`
	Head    string          `json:"head"`
	Scope   knowledge.Scope `json:"scope"`
}

// First line of a text cut so a candidate list stays one line per item on a terminal
// The limit counts runes so a cut never splits a multibyte character
const headLimit = 120

func headline(text string) string {
	first, _, _ := strings.Cut(text, "\n")
	s := []rune(strings.TrimSpace(first))
	if len(s) > headLimit {
		return string(s[:headLimit-3]) + "..."
	}
	return string(s)
}

type ExampleCandidate struct {
	TraceID string           `json:"trace_id"`
	Verdict feedback.Verdict `json:"verdict"`
	Head    string           `json:"head"`
}

// What the model chose and why
type Choices struct {
	Knowledge []Choice `json:"knowledge"`
	Examples  []Choice `json:"examples"`
}

type Choice struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func (ch Choice) picksKnowledge(k KnowledgeCandidate) bool {
	return k.ID == ch.ID
}

func (ch Choice) picksExample(e ExampleCandidate) bool {
	return e.TraceID == ch.ID
}

// Text of the chosen items and what was given after the caps
type Selection struct {
	Applied []AppliedKnowledge
	Omitted bool
	// The knowledge and examples sections of the context text
	Text string
}

type AppliedKnowledge struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Reason  string `json:"reason,omitempty"`
	// What reached the model with the cut mark
	// Zero means the item did not reach the model
	Chars        int  `json:"chars"`
	OmittedChars int  `json:"omitted_chars"`
	Cut          bool `json:"cut"`
}

func (k AppliedKnowledge) dropped() bool {
	return k.Chars == 0
}

type appliedExample struct {
	TraceID      string `json:"trace_id"`
	Reason       string `json:"reason,omitempty"`
	Chars        int    `json:"chars"`
	OmittedChars int    `json:"omitted_chars"`
	Cut          bool   `json:"cut"`
}

func (e appliedExample) dropped() bool {
	return e.Chars == 0
}

// Stored as the select trace Input
// Holds no float and no map so encoding never fails
type selectInput struct {
	Selector  Selector           `json:"mode"`
	Knowledge []AppliedKnowledge `json:"knowledge"`
	Examples  []appliedExample   `json:"examples"`
	Omitted   bool               `json:"omitted"`
}

// Knowledge that reached the model whole or cut
// Never nil so the record reads as a list
func (s selectInput) givenKnowledge() []AppliedKnowledge {
	return slices.DeleteFunc(append([]AppliedKnowledge{}, s.Knowledge...), AppliedKnowledge.dropped)
}

// An approved item the review will see
type chosenKnowledge struct {
	knowledge.Knowledge
	// Why the chooser took the item
	// Empty in the batch path
	Reason string
}

func (k chosenKnowledge) render() string {
	return fmt.Sprintf("\n[%s v%d %s] %s\nScope: %s\n", k.ID, k.Version, k.Kind, k.Content, k.Scope)
}

// An offered item the ledger loads by id and version
// Select and Run both hand the loader this instead of the candidate list it would search
type knowledgeRef struct {
	id      string
	version int
	reason  string
}

// Why is not called Reason because example already carries the feedback reason
type chosenExample struct {
	example
	// Why the chooser took the example
	// Empty in the batch path
	Why string
}

// Past feedback joined with the review it judged
type example struct {
	TraceID  string
	Original json.RawMessage
	Verdict  feedback.Verdict
	Reason   string
	Edited   json.RawMessage
}

// Records what the model chose and returns the text
// 1. every chosen id must be in the offered lists and a repeated id counts once
// 2. each chosen item gets a share of its cap and a cut is marked in the text and recorded
// 3. a second select for the same context replaces the first because record reads the newest
// 4. a context that already has a diagnose trace is refused
func (d *Diagnoser) Select(ctx context.Context, pendingID string, choices Choices) (Selection, error) {
	c, err := d.pending(ctx, pendingID)
	if err != nil {
		return Selection{}, err
	}
	refs, err := c.offeredKnowledge(choices.Knowledge)
	if err != nil {
		return Selection{}, err
	}
	items, err := d.loadKnowledge(ctx, refs)
	if err != nil {
		return Selection{}, err
	}
	picks, err := c.offeredExamples(choices.Examples)
	if err != nil {
		return Selection{}, err
	}
	examples, err := d.loadExamples(ctx, picks)
	if err != nil {
		return Selection{}, err
	}
	selected, sel := d.limits.fit(SelectByClaude, items, examples)
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.ensureOpen(ctx, pendingID); err != nil {
		return Selection{}, err
	}
	if err := d.recordSelection(ctx, c, selected); err != nil {
		return Selection{}, err
	}
	return sel, nil
}

// Each distinct chosen item with its offered version
func (c Context) offeredKnowledge(choices []Choice) ([]knowledgeRef, error) {
	var refs []knowledgeRef
	seen := map[string]struct{}{}
	for _, choice := range choices {
		i := slices.IndexFunc(c.KnowledgeCandidates, choice.picksKnowledge)
		if i < 0 {
			return nil, fmt.Errorf("%w: knowledge %s", ErrNotOffered, choice.ID)
		}
		if _, ok := seen[choice.ID]; ok {
			continue
		}
		seen[choice.ID] = struct{}{}
		refs = append(refs, knowledgeRef{id: choice.ID, version: c.KnowledgeCandidates[i].Version, reason: choice.Reason})
	}
	return refs, nil
}

// Each distinct chosen example
func (c Context) offeredExamples(choices []Choice) ([]Choice, error) {
	var picks []Choice
	seen := map[string]struct{}{}
	for _, choice := range choices {
		if !slices.ContainsFunc(c.ExampleCandidates, choice.picksExample) {
			return nil, fmt.Errorf("%w: example %s", ErrNotOffered, choice.ID)
		}
		if _, ok := seen[choice.ID]; ok {
			continue
		}
		seen[choice.ID] = struct{}{}
		picks = append(picks, choice)
	}
	return picks, nil
}

// Every offered item in order for the batch path
func (c Context) knowledgeRefs() []knowledgeRef {
	var refs []knowledgeRef
	for _, k := range c.KnowledgeCandidates {
		refs = append(refs, knowledgeRef{id: k.ID, version: k.Version})
	}
	return refs
}

// The first offered examples up to the count for the batch path
// A negative count means none
func (c Context) leadingExamples(count int) []Choice {
	var picks []Choice
	for _, e := range c.ExampleCandidates[:min(max(count, 0), len(c.ExampleCandidates))] {
		picks = append(picks, Choice{ID: e.TraceID})
	}
	return picks
}

// A version retired since the context was built fails here
func (d *Diagnoser) loadKnowledge(ctx context.Context, refs []knowledgeRef) ([]chosenKnowledge, error) {
	var items []chosenKnowledge
	for _, ref := range refs {
		k, err := d.ledger.Approved(ctx, ref.id, ref.version)
		if err != nil {
			return nil, err
		}
		items = append(items, chosenKnowledge{Knowledge: k, Reason: ref.reason})
	}
	return items, nil
}

// Every approved item regardless of scope
func (d *Diagnoser) approvedKnowledge(ctx context.Context) ([]chosenKnowledge, error) {
	all, err := d.ledger.All(ctx)
	if err != nil {
		return nil, err
	}
	var items []chosenKnowledge
	for _, k := range all.Approved() {
		items = append(items, chosenKnowledge{Knowledge: k})
	}
	return items, nil
}

func (d *Diagnoser) loadExamples(ctx context.Context, picks []Choice) ([]chosenExample, error) {
	var examples []chosenExample
	for _, pick := range picks {
		e, err := d.joinExample(ctx, pick.ID)
		if err != nil {
			return nil, err
		}
		examples = append(examples, chosenExample{example: e, Why: pick.Reason})
	}
	return examples, nil
}

func (d *Diagnoser) recordSelection(ctx context.Context, c Context, selected selectInput) error {
	tr := c.newTrace(trace.NameSelect, d.now())
	tr.Input, _ = json.Marshal(selected)
	return d.traces.Append(ctx, tr)
}

// An example exists to carry the correction
// 1. the verdict and reason lead and are never cut
// 2. the corrected review is the body so it fits whole before any original review gets a character
// 3. the original review is the tail and gets only what the corrections leave
func (e example) render(n int) block {
	var lead strings.Builder
	fmt.Fprintf(&lead, "\n### Example %d\n\nVerdict: %s\n", n, e.Verdict)
	if e.Reason != "" {
		fmt.Fprintf(&lead, "Reason: %s\n", e.Reason)
	}
	var body string
	if len(e.Edited) > 0 {
		body = fmt.Sprintf("Corrected: %s\n", e.Edited)
	}
	return block{lead: lead.String(), body: body, tail: fmt.Sprintf("Original review: %s\n", e.Original)}
}

// Newest select trace of a context that has no diagnose trace yet
// Nil when the context has none
func (d *Diagnoser) openSelect(ctx context.Context, pendingID string) (*selectInput, error) {
	if err := d.ensureOpen(ctx, pendingID); err != nil {
		return nil, err
	}
	found, err := d.traces.List(ctx, trace.Filter{Name: trace.NameSelect, Ref: pendingID, Limit: 1})
	if err != nil || len(found) == 0 {
		return nil, err
	}
	var selected selectInput
	if err := json.Unmarshal(found[0].Input, &selected); err != nil {
		return nil, fmt.Errorf("%w: select %s: %w", ErrMalformed, found[0].ID, err)
	}
	return &selected, nil
}

// Joins one feedback candidate with its trace
func (d *Diagnoser) joinExample(ctx context.Context, traceID string) (example, error) {
	tr, err := d.traces.Get(ctx, traceID)
	if err != nil {
		return example{}, err
	}
	records, err := d.feedback.List(ctx, feedback.Filter{TraceID: traceID, Limit: 1})
	if err != nil {
		return example{}, err
	}
	if len(records) == 0 {
		return example{}, fmt.Errorf("%w: %s", ErrNoFeedback, traceID)
	}
	latest := records[0]
	return example{TraceID: traceID, Original: tr.Output, Verdict: latest.Verdict, Reason: latest.Reason, Edited: latest.Edited}, nil
}

// Approved items whose scope fits the event up to the candidate cap and whether the cap cut the list
func (d *Diagnoser) knowledgeCandidates(
	ctx context.Context, changeContext evidence.Context, moved []string, dims map[string]map[string]struct{},
) ([]KnowledgeCandidate, bool, error) {
	all, err := d.ledger.All(ctx)
	if err != nil {
		return nil, false, err
	}
	applicable := all.Applicable(changeContext, moved, dims)
	var out []KnowledgeCandidate
	for _, k := range applicable[:min(len(applicable), d.limits.Candidates)] {
		out = append(out, KnowledgeCandidate{ID: k.ID, Version: k.Version, Kind: k.Kind, Head: headline(k.Content), Scope: k.Scope})
	}
	return out, len(applicable) > d.limits.Candidates, nil
}

// Short list of past corrections that could inform this review
// Text comes later through Select
// The bool reports whether the cap left out one more candidate
// 1. the latest verdict per trace decides so a trace later approved is skipped
// 2. the trace must be a diagnose trace whose recordInput decodes
// A hand edited trace that no longer decodes is skipped so one bad line never blocks every later review
// 3. same change context and at least one shared metric
// 4. never a review of an excluded event so eval cannot leak an answer into its own question
// 5. excluded always holds the event under review
// 6. newest first and at most the candidate cap
// Every feedback record is read and each correction costs one trace read
// A store query by change context would bound this once the records grow
func (d *Diagnoser) exampleCandidates(
	ctx context.Context, excluded []string, changeContext evidence.Context, metrics []string,
) ([]ExampleCandidate, bool, error) {
	all, err := d.feedback.List(ctx, feedback.Filter{})
	if err != nil {
		return nil, false, err
	}
	var out []ExampleCandidate
	for _, verdict := range feedback.Records(all).Latest() {
		if !verdict.Corrects() {
			continue
		}
		tr, err := d.traces.Get(ctx, verdict.TraceID)
		if errors.Is(err, trace.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if tr.Name != trace.NameDiagnose || len(tr.Output) == 0 || slices.Contains(excluded, tr.Subject) {
			continue
		}
		var in recordInput
		if json.Unmarshal(tr.Input, &in) != nil || !in.matches(changeContext, metrics) {
			continue
		}
		if len(out) == d.limits.Candidates {
			return out, true, nil
		}
		out = append(out, ExampleCandidate{TraceID: verdict.TraceID, Verdict: verdict.Verdict, Head: headline(verdict.Reason)})
	}
	return out, false, nil
}
