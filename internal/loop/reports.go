package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
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
	traces   trace.Traces
	runs     Runs
	items    knowledge.Set
	outcomes feedback.Outcomes
	rollups  Rollups
	// The start of the window the runs were read in
	since time.Time
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

func (s Snapshot) Critics() CriticReport {
	return CriticReport{Critics: s.runs.Critics(s.items, s.traces), Reactions: s.runs.Reactions(s.traces)}
}

func (s Snapshot) Effect() []EffectRow {
	return s.runs.Effect(s.items, s.outcomes, s.rollups, s.since)
}

// The start of the window the runs were read in and zero for every run
func (s Snapshot) Since() time.Time {
	return s.since
}

// The stores every report reads from
type Stores struct {
	Traces   TraceStore
	Verdicts FeedbackStore
	Outcomes OutcomeStore
	Items    KnowledgeStore
	// The cutoff of the retention window and the runs before it are left out
	// The zero time reads every run
	Since time.Time
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
		return []trace.Name{trace.NameRun, trace.NameRollup}
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
	if name != ReportEffect {
		return s.snapshot(ctx, name.traces(), s.Since)
	}
	return s.cycled(ctx, name.traces())
}

// The snapshot of the names with every cycle whole as report effect and report curation read it
// 1. the runs before the first cycle are read in the window as every report reads them
// 2. a cycle is read whole from its start since no prune removes a run of a cycle that has no rollup
// 3. a closed cycle with a rollup reads from the rollup and the runs a prune left in it count nothing
// So the counts of a closed cycle are the same before and after the prune of its runs
func (s Stores) cycled(ctx context.Context, names []trace.Name) (Snapshot, error) {
	marks, err := s.list(ctx, trace.NameKnob, s.Since)
	if err != nil {
		return Snapshot{}, err
	}
	since := s.Since
	if first, ok := marks.FirstCycle(); ok && first.Start.Before(since) {
		since = first.Start
	}
	snap, err := s.snapshot(ctx, names, since)
	if err != nil {
		return Snapshot{}, err
	}
	outcomes, err := s.Outcomes.List(ctx, "")
	snap.outcomes, snap.rollups = feedback.RestoreOutcomes(outcomes), NewRollups(snap.traces)
	return snap, err
}

// The traces of the name in the newest first order of the store
// Runs and classify traces are read from since on since a prune removes both and a count over all time would mix what survived
func (s Stores) list(ctx context.Context, name trace.Name, since time.Time) (trace.Traces, error) {
	f := record.TraceFilter{Name: string(name)}
	if name == trace.NameRun || name == trace.NameClassify {
		f.Since = since
	}
	found, err := s.Traces.List(ctx, f)
	return trace.RestoreAll(found), err
}

// Reads the traces of each name in turn so no report reads a trace it never looks at
func (s Stores) snapshot(ctx context.Context, names []trace.Name, since time.Time) (Snapshot, error) {
	var all trace.Traces
	for _, name := range names {
		found, err := s.list(ctx, name, since)
		if err != nil {
			return Snapshot{}, err
		}
		all = append(all, found...)
	}
	records, err := s.Verdicts.List(ctx, record.FeedbackFilter{})
	if err != nil {
		return Snapshot{}, err
	}
	set, err := s.Items.All(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{traces: all, runs: NewRuns(all, feedback.RestoreAll(records)), items: set, since: since}, nil
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
		h, err := Load(ctx, s.Traces, s.Verdicts, s.Outcomes, s.Items, s.Since)
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
