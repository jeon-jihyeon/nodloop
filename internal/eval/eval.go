// Package eval turns traces and labels and feedback into the numbers README reports
package eval

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Conditions are trace tags so the strings stay while the names say what each injects
type Condition string

const (
	ConditionSeed         Condition = "seed"          // first review of a seed event with nothing injected
	ConditionBaseline     Condition = "feedback:off"  // holdout with nothing injected
	ConditionExamples     Condition = "feedback:on"   // seed corrections as examples
	ConditionKnowledge    Condition = "knowledge:on"  // approved knowledge selected by scope
	ConditionKnowledgeAll Condition = "knowledge:all" // every approved item regardless of scope as the memory file baseline
)

// Holdout conditions in report order
// A fresh slice per call so no caller can change what another sees
func holdoutConditions() []Condition {
	return []Condition{ConditionBaseline, ConditionExamples, ConditionKnowledge, ConditionKnowledgeAll}
}

// Every condition in report order
func allConditions() []Condition {
	return append([]Condition{ConditionSeed}, holdoutConditions()...)
}

func (c Condition) valid() bool {
	return slices.Contains(allConditions(), c)
}

// What the condition injects into a review
func (c Condition) options(examples int) diagnose.BatchOptions {
	switch c {
	case ConditionExamples:
		return diagnose.BatchOptions{Examples: examples, Knowledge: diagnose.KnowledgeNone}
	case ConditionKnowledge:
		return diagnose.BatchOptions{Knowledge: diagnose.KnowledgeSelected}
	case ConditionKnowledgeAll:
		return diagnose.BatchOptions{Knowledge: diagnose.KnowledgeAll}
	default:
		return diagnose.BatchOptions{Knowledge: diagnose.KnowledgeNone}
	}
}

// A score of -1 means the metric does not apply to the event
const notApplicable = -1

type Score struct {
	EventID           string             `json:"event_id"`
	Condition         Condition          `json:"condition"`
	Type              evidence.EventType `json:"type"`
	ExpectedStatus    diagnose.Status    `json:"expected_status"`
	Status            diagnose.Status    `json:"status"`
	StatusOK          bool               `json:"status_ok"`
	ForcedHold        bool               `json:"forced_hold"`
	Failed            bool               `json:"failed"`
	CitationPrecision float64            `json:"citation_precision"`
	CitationRecall    float64            `json:"citation_recall"`
	RequiredChecks    float64            `json:"required_checks"`
	// 1 when the first check cites the first required check of the label and 0 otherwise
	// The runbook orders its steps and the label keeps that order
	FirstCheck float64 `json:"first_check"`
	// Whether record sent the review back once before this one
	Revised bool `json:"revised"`
	// Share of the Knowledge ids of the label the review used
	// Not applicable when the label expects none
	KnowledgeHit float64 `json:"knowledge_hit"`
	// Knowledge ids used that the label does not expect
	Misapplications int              `json:"misapplied"`
	KnowledgeUsed   []string         `json:"knowledge_used,omitempty"`
	Verdict         feedback.Verdict `json:"verdict,omitempty"`
	EditWidth       int              `json:"edit_width"`
	CostUSD         float64          `json:"cost_usd"`
	InputTokens     int              `json:"input_tokens"`
	OutputTokens    int              `json:"output_tokens"`
	DurationMS      int64            `json:"duration_ms"`
}

// Orders scores by event id
func (s Score) compare(other Score) int {
	return strings.Compare(s.EventID, other.EventID)
}

type Summary struct {
	Condition      Condition `json:"condition"`
	Events         int       `json:"events"`
	StatusAccuracy float64   `json:"status_accuracy"`
	// Over the events whose label holds
	HoldRecall float64 `json:"hold_accuracy"`
	// Share of the hold answers whose label holds
	// Not applicable when no review answered hold
	HoldPrecision     float64 `json:"hold_precision"`
	CitationPrecision float64 `json:"citation_precision"`
	CitationRecall    float64 `json:"citation_recall"`
	RequiredChecks    float64 `json:"required_checks"`
	FirstCheck        float64 `json:"first_check"`
	KnowledgeHit      float64 `json:"knowledge_hit"`
	Misapplications   int     `json:"misapplied"`
	Revisions         int     `json:"revised"`
	ForcedHolds       int     `json:"forced_holds"`
	Failures          int     `json:"failures"`
	Annotated         int     `json:"annotated"`
	Approvals         int     `json:"approvals"`
	Edits             int     `json:"edits"`
	Rejections        int     `json:"rejections"`
	EditRate          float64 `json:"edit_rate"`
	// Mean over the edit verdicts
	// Not applicable when nothing was edited
	MeanEditWidth    float64 `json:"mean_edit_width"`
	MeanCostUSD      float64 `json:"mean_cost_usd"`
	MeanInputTokens  float64 `json:"mean_input_tokens"`
	MeanOutputTokens float64 `json:"mean_output_tokens"`
	P50DurationMS    int64   `json:"p50_duration_ms"`
	P95DurationMS    int64   `json:"p95_duration_ms"`
}

// 1. hold when the label holds
// 2. no_action for normal variation and attribution lag
// 3. ready_for_review otherwise
func expectedStatus(hold bool, eventType evidence.EventType) diagnose.Status {
	switch {
	case hold:
		return diagnose.StatusHold
	case eventType == evidence.TypeNormalVariation, eventType == evidence.TypeAttributionLag:
		return diagnose.StatusNoAction
	default:
		return diagnose.StatusReadyForReview
	}
}

// Scores one diagnose trace against its label and the latest verdict on it
// An empty verdict means the trace has no feedback
// 1. a failed trace scores as an empty review so every metric keeps failures in its denominator
// 2. citations compare the union of cause paragraph ids with the label paragraphs
// 3. required checks are the share of the RequiredChecks of the label cited by any check and stay not applicable on a no_action label since that review carries no checks
// 4. edit width counts the top level fields that differ between the output and the edited review
// 5. knowledge hit and misapplied compare the ids the trace input names with the Knowledge of the label
// 6. a metric whose label side is empty stays not applicable
// 7. the verdict counts on a failed trace too
func newScore(condition Condition, l evidence.Label, tr trace.Trace, revised bool, verdict feedback.Verdict, edited json.RawMessage) Score {
	s := Score{
		EventID: l.EventID, Condition: condition, Type: l.Type, ExpectedStatus: expectedStatus(l.IsHold(), l.Type),
		CitationPrecision: notApplicable, CitationRecall: notApplicable, RequiredChecks: notApplicable, FirstCheck: notApplicable, KnowledgeHit: notApplicable,
		ForcedHold: slices.Contains(tr.Tags, diagnose.TagGateHold),
		Revised:    revised,
		CostUSD:    tr.Usage.CostUSD, InputTokens: tr.Usage.PromptTokens(), OutputTokens: tr.Usage.OutputTokens, DurationMS: tr.DurationMS,
	}
	var diag diagnose.Diagnosis
	s.Failed = tr.Error != "" || len(tr.Output) == 0 || json.Unmarshal(tr.Output, &diag) != nil
	used := diagnose.KnowledgeApplied(tr.Input)
	if s.Failed {
		diag, used = diagnose.Diagnosis{}, nil
	}
	s.Status, s.StatusOK = diag.Status, diag.Status == s.ExpectedStatus
	s.scoreKnowledge(used, l.Knowledge)
	s.scoreCitations(diag.Causes, l.Paragraphs)
	var first []string
	if len(diag.Checks) > 0 {
		first = diag.Checks[0].ParagraphIDs
	}
	if s.ExpectedStatus != diagnose.StatusNoAction {
		s.scoreChecks(diag.Checks.Paragraphs(), first, l.RequiredChecks)
	}
	s.scoreVerdict(tr.Output, verdict, edited)
	return s
}

func (s *Score) scoreKnowledge(used, expected []string) {
	s.KnowledgeUsed = used
	hit := 0
	for _, id := range used {
		if !slices.Contains(expected, id) {
			s.Misapplications++
			continue
		}
		hit++
	}
	if len(expected) > 0 {
		s.KnowledgeHit = float64(hit) / float64(len(expected))
	}
}

// Precision over the ids the causes cite and recall over the ids the label expects
func (s *Score) scoreCitations(causes []diagnose.Cause, expected []evidence.ParagraphID) {
	if len(expected) == 0 {
		return
	}
	cited := map[string]struct{}{}
	for _, c := range causes {
		for _, id := range c.ParagraphIDs {
			cited[id] = struct{}{}
		}
	}
	hit := 0
	for id := range cited {
		if slices.Contains(expected, evidence.ParagraphID(id)) {
			hit++
		}
	}
	s.CitationPrecision = 0
	if len(cited) > 0 {
		s.CitationPrecision = float64(hit) / float64(len(cited))
	}
	s.CitationRecall = float64(hit) / float64(len(expected))
}

// Required keeps the order the runbook runs its steps
// first holds the paragraphs of the first check of the review and is held to the first entry of required
func (s *Score) scoreChecks(checked, first []string, required []evidence.ParagraphID) {
	if len(required) == 0 {
		return
	}
	hit := 0
	for _, id := range required {
		if slices.Contains(checked, string(id)) {
			hit++
		}
	}
	s.RequiredChecks = float64(hit) / float64(len(required))
	s.FirstCheck = 0
	if slices.Contains(first, string(required[0])) {
		s.FirstCheck = 1
	}
}

func (s *Score) scoreVerdict(output json.RawMessage, verdict feedback.Verdict, edited json.RawMessage) {
	s.Verdict = verdict
	var original, changed map[string]json.RawMessage
	if verdict != feedback.VerdictEdit || json.Unmarshal(output, &original) != nil || json.Unmarshal(edited, &changed) != nil {
		return
	}
	for k, v := range original {
		if string(v) != string(changed[k]) {
			s.EditWidth++
		}
	}
	for k := range changed {
		if _, ok := original[k]; !ok {
			s.EditWidth++
		}
	}
}

// A mean that leaves not applicable values out
type mean struct {
	sum   float64
	count int
}

func (m *mean) add(v float64) {
	if v == notApplicable {
		return
	}
	m.sum += v
	m.count++
}

func (m mean) value() float64 {
	if m.count == 0 {
		return notApplicable
	}
	return m.sum / float64(m.count)
}

// Running totals over the scores of one condition
type tally struct {
	summary                                                                                Summary
	statusOK, holds, holdOK, answeredHold                                                  int
	citationPrecision, citationRecall, requiredChecks, firstCheck, knowledgeHit, editWidth mean
	cost, inputTokens, outputTokens                                                        float64
	durations                                                                              durations
}

// Review durations in milliseconds
type durations []int64

// Nearest rank over sorted durations: the value at rank ceil of percent times count over 100
// Integer arithmetic so a product such as 95 times 20 never lands just above a whole rank
func (ds durations) percentile(percent int) int64 {
	rank := (percent*len(ds) + 99) / 100
	return ds[rank-1]
}

func (t *tally) add(s Score) {
	t.summary.Condition = s.Condition
	t.summary.Events++
	if s.Failed {
		t.summary.Failures++
	}
	if s.ForcedHold {
		t.summary.ForcedHolds++
	}
	if s.StatusOK {
		t.statusOK++
	}
	t.addHold(s.ExpectedStatus == diagnose.StatusHold, s.Status == diagnose.StatusHold)
	t.citationPrecision.add(s.CitationPrecision)
	t.citationRecall.add(s.CitationRecall)
	t.requiredChecks.add(s.RequiredChecks)
	t.firstCheck.add(s.FirstCheck)
	if s.Revised {
		t.summary.Revisions++
	}
	t.knowledgeHit.add(s.KnowledgeHit)
	t.summary.Misapplications += s.Misapplications
	t.addVerdict(s.Verdict, s.EditWidth)
	t.cost += s.CostUSD
	t.inputTokens += float64(s.InputTokens)
	t.outputTokens += float64(s.OutputTokens)
	t.durations = append(t.durations, s.DurationMS)
}

func (t *tally) addHold(expected, answered bool) {
	if expected {
		t.holds++
	}
	if answered {
		t.answeredHold++
	}
	if expected && answered {
		t.holdOK++
	}
}

func (t *tally) addVerdict(verdict feedback.Verdict, editWidth int) {
	switch verdict {
	case feedback.VerdictApprove:
		t.summary.Approvals++
	case feedback.VerdictEdit:
		t.summary.Edits++
		t.editWidth.add(float64(editWidth))
	case feedback.VerdictReject:
		t.summary.Rejections++
	default:
		return
	}
	t.summary.Annotated++
}

// Only called after at least one score was added
// Sorts a copy of the durations so the tally of the caller keeps its order
func (t tally) result() Summary {
	t.durations = slices.Sorted(slices.Values(t.durations))
	sum := t.summary
	sum.HoldRecall, sum.HoldPrecision, sum.EditRate = notApplicable, notApplicable, notApplicable
	sum.CitationPrecision, sum.CitationRecall = t.citationPrecision.value(), t.citationRecall.value()
	sum.RequiredChecks, sum.FirstCheck, sum.KnowledgeHit, sum.MeanEditWidth = t.requiredChecks.value(), t.firstCheck.value(), t.knowledgeHit.value(), t.editWidth.value()
	n := float64(sum.Events)
	sum.StatusAccuracy = float64(t.statusOK) / n
	if t.holds > 0 {
		sum.HoldRecall = float64(t.holdOK) / float64(t.holds)
	}
	if t.answeredHold > 0 {
		sum.HoldPrecision = float64(t.holdOK) / float64(t.answeredHold)
	}
	if sum.Annotated > 0 {
		sum.EditRate = float64(sum.Edits+sum.Rejections) / float64(sum.Annotated)
	}
	sum.MeanCostUSD = t.cost / n
	sum.MeanInputTokens = t.inputTokens / n
	sum.MeanOutputTokens = t.outputTokens / n
	sum.P50DurationMS = t.durations.percentile(50)
	sum.P95DurationMS = t.durations.percentile(95)
	return sum
}

// Scores of one condition
type scores []Score

// Means over the scores
// Not applicable scores are left out of their average
func (ss scores) summary() Summary {
	var t tally
	for _, s := range ss {
		t.add(s)
	}
	return t.result()
}

// How one condition compares with the baseline on the same events
// Averages hide which events moved and the report names them
type Comparison struct {
	// Status wrong at the baseline and right here
	Fixed []string `json:"fixed"`
	// Status right at the baseline and wrong here
	Regressed []string `json:"regressed"`
	// Status unchanged but required checks or citation recall below the baseline value
	Weakened []string `json:"weakened"`
}

func (ss scores) byEvent() map[string]Score {
	out := map[string]Score{}
	for _, s := range ss {
		out[s.EventID] = s
	}
	return out
}

// Events keep the order of the scores
// Events the baseline did not score are left out
func (ss scores) comparedTo(base map[string]Score) Comparison {
	c := Comparison{Fixed: []string{}, Regressed: []string{}, Weakened: []string{}}
	for _, s := range ss {
		b, ok := base[s.EventID]
		if !ok {
			continue
		}
		switch {
		case s.StatusOK && !b.StatusOK:
			c.Fixed = append(c.Fixed, s.EventID)
		case !s.StatusOK && b.StatusOK:
			c.Regressed = append(c.Regressed, s.EventID)
		case s.RequiredChecks < b.RequiredChecks || s.CitationRecall < b.CitationRecall:
			c.Weakened = append(c.Weakened, s.EventID)
		}
	}
	return c
}
