package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The reviews the triage order puts first
const triageTop = 5

// Wrong statuses among the reviews of one band of the triage order
type ErrorBand struct {
	Events int `json:"events"`
	Errors int `json:"errors"`
	// Not applicable on an empty band
	ErrorRate float64 `json:"error_rate"`
}

func (b *ErrorBand) add(wrong bool) {
	b.Events++
	if wrong {
		b.Errors++
	}
	b.ErrorRate = float64(b.Errors) / float64(b.Events)
}

// How well the queue order of one condition finds its wrong statuses
type Triage struct {
	Condition Condition `json:"condition"`
	Events    int       `json:"events"`
	// Wrong statuses including failed reviews
	Errors int `json:"errors"`
	// Failed reviews have no status to rank and stay in the denominator of the recall
	Unranked int `json:"unranked"`
	K        int `json:"k"`
	// Share of the wrong statuses that the top k hold
	// Not applicable without a wrong status
	RecallAtK float64   `json:"recall_at_k"`
	Top       ErrorBand `json:"top"`
	Rest      ErrorBand `json:"rest"`
}

// Ranks the newest holdout reviews of the first repeat per condition the way the queue ranks the conversation
// The history is every trace and verdict and knowledge record so the order reads what a person would have seen
func (r *Runner) Triage(ctx context.Context, sessionID string) ([]Triage, error) {
	rep, err := r.Report(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	traces, err := r.traces.List(ctx, trace.Filter{})
	if err != nil {
		return nil, err
	}
	verdicts, err := r.feedback.List(ctx, feedback.Filter{})
	if err != nil {
		return nil, err
	}
	items, err := r.ledger.All(ctx)
	if err != nil {
		return nil, err
	}
	history, err := loop.New(traces, verdicts, nil, items)
	if err != nil {
		return nil, err
	}
	newest := reviews(traces).session(sessionID).firstRepeat().newest()
	var out []Triage
	for _, cond := range holdoutConditions() {
		row, err := newTriage(cond, scores(rep.Scores).of(cond), newest[cond], history)
		if err != nil {
			return nil, err
		}
		if row.Events > 0 {
			out = append(out, row)
		}
	}
	return out, nil
}

// The diagnose traces of one session in their order
func (rs reviews) session(sessionID string) reviews {
	filter := trace.Filter{Name: trace.NameDiagnose, SessionID: sessionID}
	var out reviews
	for _, tr := range rs {
		if filter.Matches(tr) {
			out = append(out, tr)
		}
	}
	return out
}

// The scores and the newest reviews of one condition join on the event
func newTriage(cond Condition, ss scores, newest []trace.Trace, history *loop.History) (Triage, error) {
	row := Triage{
		Condition: cond, K: triageTop, RecallAtK: notApplicable,
		Top: ErrorBand{ErrorRate: notApplicable}, Rest: ErrorBand{ErrorRate: notApplicable},
	}
	byEvent := map[string]trace.Trace{}
	for _, tr := range newest {
		byEvent[tr.Subject] = tr
	}
	wrong := map[string]bool{}
	var candidates trace.Traces
	for _, s := range ss {
		row.Events++
		if !s.StatusOK {
			row.Errors++
		}
		tr, ok := byEvent[s.EventID]
		if s.Failed || !ok {
			row.Unranked++
			continue
		}
		wrong[tr.ID] = !s.StatusOK
		candidates = append(candidates, tr)
	}
	ranked, err := history.Rank(candidates)
	if err != nil {
		return Triage{}, err
	}
	for i, item := range ranked {
		band := &row.Rest
		if i < row.K {
			band = &row.Top
		}
		band.add(wrong[item.TraceID])
	}
	if row.Errors > 0 {
		row.RecallAtK = float64(row.Top.Errors) / float64(row.Errors)
	}
	return row, nil
}

// Triage rows in condition order
type triages []Triage

func (ts triages) table() string {
	if len(ts) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nTriage order: the top %d of the queue order against the rest. Failed reviews stay in the recall denominator\n\n", triageTop)
	b.WriteString("| condition | events | errors | unranked | recall at k | top error rate | rest error rate |\n|---|---|---|---|---|---|---|\n")
	for _, row := range ts {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s | %s | %s |\n", row.Condition, row.Events, row.Errors, row.Unranked,
			ratio(row.RecallAtK), ratio(row.Top.ErrorRate), ratio(row.Rest.ErrorRate))
	}
	return b.String()
}
