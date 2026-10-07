package loop

import (
	"context"
	"encoding/json"
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
	ReportReplay  ReportName = "replay"  // the newest replay of each version
)

func ReportNames() []ReportName {
	return []ReportName{ReportLoop, ReportExtract, ReportCritic, ReportEffect, ReportHealth, ReportReplay}
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

// The traces a report reads with every verdict and knowledge record joined once
type Snapshot struct {
	traces trace.Traces
	runs   Runs
	items  knowledge.Set
}

func (s Snapshot) Loop() LoopReport {
	return LoopReport{
		Totals: s.runs.Totals(s.items),
		Scopes: s.runs.Scopes(s.items, s.traces),
		Drafts: s.runs.Drafts(s.items, s.traces),
		Items:  s.runs.Report(s.items),
	}
}

func (s Snapshot) Extractions() []ExtractRow {
	return s.runs.Extractions(s.traces)
}

func (s Snapshot) Critics() []CriticRow {
	return s.runs.Critics(s.items, s.traces)
}

func (s Snapshot) Effect() []EffectRow {
	return s.runs.Effect(s.items)
}

// The stores every report reads from
type Stores struct {
	Traces   TraceStore
	Verdicts FeedbackStore
	Outcomes OutcomeStore
	Items    KnowledgeStore
}

// The trace names the report of the name reads
// Health loads its own history so it reads none here
func (n ReportName) traces() []trace.Name {
	switch n {
	case ReportLoop, ReportExtract:
		return []trace.Name{trace.NameRun, trace.NameExtract}
	case ReportCritic:
		return []trace.Name{trace.NameExtract, trace.NameClassify}
	case ReportEffect:
		return []trace.Name{trace.NameRun}
	case ReportReplay:
		return []trace.Name{trace.NameReplay}
	}
	return nil
}

// The snapshot holding the traces the report of the name reads
func (s Stores) Snapshot(ctx context.Context, name ReportName) (Snapshot, error) {
	if !name.Valid() {
		return Snapshot{}, fmt.Errorf("%w: %q. Ask for one of %v", ErrReportUnknown, name, ReportNames())
	}
	return s.snapshot(ctx, name.traces())
}

// Reads the traces of each name in turn so no report reads a trace it never looks at
// Each name keeps the newest first order of the store
func (s Stores) snapshot(ctx context.Context, names []trace.Name) (Snapshot, error) {
	var all trace.Traces
	for _, name := range names {
		found, err := s.Traces.List(ctx, trace.Filter{Name: name})
		if err != nil {
			return Snapshot{}, err
		}
		all = append(all, found...)
	}
	records, err := s.Verdicts.List(ctx, feedback.Filter{})
	if err != nil {
		return Snapshot{}, err
	}
	set, err := s.Items.All(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{traces: all, runs: NewRuns(all, records), items: set}, nil
}

// The report of the name as JSON for a reader that picks it at run time
// Every reader only encodes a report so it leaves here encoded and its shape stays with the report types
func (s Stores) Report(ctx context.Context, name ReportName, now time.Time) (json.RawMessage, error) {
	report, err := s.report(ctx, name, now)
	if err != nil {
		return nil, err
	}
	return json.Marshal(report)
}

func (s Stores) report(ctx context.Context, name ReportName, now time.Time) (any, error) {
	if name == ReportHealth {
		h, err := Load(ctx, s.Traces, s.Verdicts, s.Outcomes, s.Items)
		if err != nil {
			return nil, err
		}
		return h.Health(now), nil
	}
	snap, err := s.Snapshot(ctx, name)
	if err != nil {
		return nil, err
	}
	switch name {
	case ReportExtract:
		return snap.Extractions(), nil
	case ReportCritic:
		return snap.Critics(), nil
	case ReportEffect:
		return snap.Effect(), nil
	case ReportReplay:
		return snap.Replays(), nil
	}
	return snap.Loop(), nil
}
