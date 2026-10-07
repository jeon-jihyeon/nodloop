package loop

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A report a reader asks for by name
type ReportName string

const (
	ReportLoop    ReportName = "loop"    // totals, scope and drafts lines and a row per approved item
	ReportExtract ReportName = "extract" // extractions per plugin version and path
	ReportCritic  ReportName = "critic"  // how each critic agreed with people
	ReportEffect  ReportName = "effect"  // the runs that applied items against the holdout
	ReportHealth  ReportName = "health"  // verdicts and outcomes per knowledge version
)

func ReportNames() []ReportName {
	return []ReportName{ReportLoop, ReportExtract, ReportCritic, ReportEffect, ReportHealth}
}

func (n ReportName) Valid() bool {
	return slices.Contains(ReportNames(), n)
}

// The loop report as one value
type LoopReport struct {
	Totals Totals     `json:"totals"`
	Scopes []ScopeRow `json:"scopes"`
	Drafts []DraftRow `json:"drafts"`
	Items  []RunItem  `json:"items"`
}

// Every trace, verdict and knowledge record the reports read, joined once
type Evidence struct {
	traces trace.Traces
	runs   Runs
	items  knowledge.Set
}

func LoadEvidence(ctx context.Context, traces TraceStore, verdicts FeedbackStore, items KnowledgeStore) (Evidence, error) {
	all, err := traces.List(ctx, trace.Filter{})
	if err != nil {
		return Evidence{}, err
	}
	records, err := verdicts.List(ctx, feedback.Filter{})
	if err != nil {
		return Evidence{}, err
	}
	set, err := items.All(ctx)
	if err != nil {
		return Evidence{}, err
	}
	return Evidence{traces: all, runs: NewRuns(all, records), items: set}, nil
}

func (e Evidence) Loop() LoopReport {
	return LoopReport{
		Totals: e.runs.Totals(e.items),
		Scopes: e.runs.Scopes(e.items, e.traces),
		Drafts: e.runs.Drafts(e.items, e.traces),
		Items:  e.runs.Report(e.items),
	}
}

func (e Evidence) Extractions() []ExtractRow {
	return e.runs.Extractions(e.traces)
}

func (e Evidence) Critics() []CriticRow {
	return e.runs.Critics(e.items, e.traces)
}

func (e Evidence) Effect() []EffectRow {
	return e.runs.Effect(e.items)
}

// The stores every report reads from
type Stores struct {
	Traces   TraceStore
	Verdicts FeedbackStore
	Outcomes OutcomeStore
	Items    KnowledgeStore
}

// The report of the name as a value that encodes to JSON, for a reader that picks it at run time
func (s Stores) Report(ctx context.Context, name ReportName, now time.Time) (any, error) {
	if !name.Valid() {
		return nil, fmt.Errorf("%w: %q. Ask for one of %v", ErrReportUnknown, name, ReportNames())
	}
	if name == ReportHealth {
		h, err := Load(ctx, s.Traces, s.Verdicts, s.Outcomes, s.Items)
		if err != nil {
			return nil, err
		}
		return h.Health(now), nil
	}
	e, err := LoadEvidence(ctx, s.Traces, s.Verdicts, s.Items)
	if err != nil {
		return nil, err
	}
	switch name {
	case ReportExtract:
		return e.Extractions(), nil
	case ReportCritic:
		return e.Critics(), nil
	case ReportEffect:
		return e.Effect(), nil
	}
	return e.Loop(), nil
}
