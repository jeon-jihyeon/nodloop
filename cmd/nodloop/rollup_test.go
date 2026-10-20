package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// The record directory of three sessions drawn into the arms of the holdout over six weeks and the home of its retention
// Each run is named by the days before pruneNow
// 1. session s1 applied an approved item and session s2 was withheld from it with runs judged by a person, a name and the conversation
// 2. session s3 applied it with an outcome, and one run went through no draw
// 3. the run that taught the item lies before every cycle
type effectRecords struct {
	getenv  func(string) string
	records string
	ids     map[int]string
}

// The cycle date and the weeks of a knob event and no knob event for an empty date
type knobbed struct {
	cycle string
	weeks int
}

func newEffectRecords(t *testing.T, retention string, k knobbed) effectRecords {
	t.Helper()
	ctx := context.Background()
	home, records := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".nodloop"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".nodloop", "config.json"), []byte(`{"retention":`+retention+`}`), 0o600))
	traces, err := tracefile.New(records)
	require.NoError(t, err)
	verdicts, err := feedbackfile.New(records)
	require.NoError(t, err)
	outcomes, err := feedbackfile.NewOutcomeStore(records)
	require.NoError(t, err)
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	drawn := func(unit string, withheld bool) knowledge.RunInput {
		if withheld {
			return knowledge.RunInput{Applied: []knowledge.Ref{}, Withheld: []knowledge.Ref{gitC}, Drawn: true, Share: 0.5, Scheme: 1, Unit: unit}
		}
		return knowledge.RunInput{Applied: []knowledge.Ref{gitC}, Drawn: true, Share: 0.5, Scheme: 1, Unit: unit}
	}
	r := effectRecords{getenv: func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }, records: records, ids: map[int]string{}}
	all := k.traces(t)
	for _, run := range []struct {
		days int
		in   knowledge.RunInput
	}{
		{45, knowledge.RunInput{Applied: []knowledge.Ref{}}}, {40, drawn("u-s1", false)}, {39, drawn("u-s2", true)},
		{38, drawn("u-s1", false)}, {37, knowledge.RunInput{Applied: []knowledge.Ref{gitC}}}, {36, drawn("u-s2", true)},
		{35, drawn("u-s3", false)}, {20, drawn("u-s1", false)}, {11, drawn("u-s2", true)}, {3, drawn("u-s3", false)}, {1, drawn("u-s1", false)},
	} {
		at := pruneNow.Add(-time.Duration(run.days) * 24 * time.Hour)
		input, err := json.Marshal(run.in)
		require.NoError(t, err)
		tr := trace.Trace{ID: trace.NewID(at), Name: trace.NameRun, Producer: sessionProducer, Time: at, Input: input}
		all = append(all, tr)
		r.ids[run.days] = tr.ID
	}
	// In time order as the store keeps them
	slices.SortFunc(all, func(a, b trace.Trace) int { return a.Time.Compare(b.Time) })
	for _, tr := range all {
		require.NoError(t, traces.Append(ctx, tr.Record()))
	}
	form, fact := feedback.ReasonForm, feedback.ReasonFact
	for days, fb := range map[int]feedback.Feedback{
		45: {Verdict: feedback.VerdictReject, ReasonCode: form, Reviewer: "ann", Proof: trace.ProofKey},
		40: {Verdict: feedback.VerdictReject, ReasonCode: form, Reviewer: "ann", Proof: trace.ProofKey},
		36: {Verdict: feedback.VerdictReject, ReasonCode: fact, Reviewer: "ci", Proof: trace.ProofNone},
		1:  {Verdict: feedback.VerdictApprove, Reviewer: feedback.ReviewerSession},
	} {
		fb.TraceID, fb.Time = r.ids[days], pruneNow.Add(-time.Hour)
		require.NoError(t, verdicts.Append(ctx, fb.Record()))
	}
	o, err := feedback.NewOutcome(r.ids[35], feedback.ResultConfirmed, "", "", "ann", pruneNow.Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, outcomes.Append(ctx, o.Record()))
	items, err := knowledgefile.New(records)
	require.NoError(t, err)
	approved := pruneNow.Add(-44 * 24 * time.Hour)
	ledger := knowledge.NewLedger(items, vetofile.NewApprovedFile(home, records), func() time.Time { return pruneNow }, knowledge.NewIDs(time.Now))
	require.NoError(t, errOf(ledger.Import(ctx, []knowledge.Knowledge{{
		ID: "git-c", Version: 1, Kind: knowledge.KindJudgment, Content: "use git -C", Run: &knowledge.RunScope{Producer: sessionProducer},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{r.ids[45]}}, Basis: knowledge.BasisStated, Status: knowledge.StatusApproved,
		Approver: "ann", ApprovedAt: approved, Time: approved, Author: "ann",
	}})))
	return r
}

func errOf[T any](_ T, err error) error {
	return err
}

// The knob trace a pass appended on the morning of the start of the cycle and none without a date
func (k knobbed) traces(t *testing.T) []trace.Trace {
	t.Helper()
	if k.cycle == "" {
		return nil
	}
	start, err := time.Parse(trace.CycleDate, k.cycle)
	require.NoError(t, err)
	event := curate.KnobEvent{CycleID: k.cycle, Start: start, Seed: "5eed", Knobs: curate.Knobs{CycleWeeks: k.weeks}, Cause: curate.KnobStart}
	tr, err := event.Trace("0.10.0", start.Add(9*time.Hour))
	require.NoError(t, err)
	return []trace.Trace{tr}
}

// What a command printed
func (r effectRecords) run(t *testing.T, cmd func([]string, func(string) string, func() time.Time, io.Writer, io.Writer) int, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	require.Equal(t, 0, cmd(args, r.getenv, func() time.Time { return pruneNow }, &stdout, &stderr), stderr.String())
	return stdout.String()
}

// The days before pruneNow of the runs left and the number of rollup traces
func (r effectRecords) left(t *testing.T) ([]int, int) {
	t.Helper()
	traces, err := tracefile.New(r.records)
	require.NoError(t, err)
	all, err := traces.List(context.Background(), record.TraceFilter{})
	require.NoError(t, err)
	days := map[string]int{}
	for d, id := range r.ids {
		days[id] = d
	}
	var runs []int
	rollups := 0
	for _, tr := range all {
		switch tr.Name {
		case string(trace.NameRun):
			runs = append(runs, days[tr.ID])
		case string(trace.NameRollup):
			rollups++
		}
	}
	return runs, rollups
}

// report effect reads the same before and after records prune and a second prune changes nothing more
// 1. a closed cycle the cutoff reaches is rolled up before its unjudged runs go
// 2. the cutoff keeps to the start of the open cycle so its runs stay
// 3. records without a knob event prune by the retention alone and roll up nothing
func TestRecordsPruneKeepsEffect(t *testing.T) {
	type args struct {
		retention string
		knob      knobbed
	}
	type want struct {
		planned string
		left    []int
		rollups int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a closed cycle keeps its counts in its rollup",
			args{"30", knobbed{"2026-08-31", 2}},
			want{"would roll up 1 closed cycles and prune 3 runs, 0 classify traces and 0 turn, instructions and capture traces older than 2026-09-12T12:00:00Z",
				[]int{1, 3, 11, 20, 35, 36, 40, 45}, 1},
		},
		{
			"the open cycle keeps its runs whatever their age",
			args{"7", knobbed{"2026-09-28", 4}},
			want{"would roll up 0 closed cycles and prune 4 runs, 0 classify traces and 0 turn, instructions and capture traces older than 2026-09-28T00:00:00Z",
				[]int{1, 3, 11, 35, 36, 40, 45}, 0},
		},
		{
			"records without a knob event prune by the retention alone",
			args{"30", knobbed{}},
			want{"would roll up 0 closed cycles and prune 3 runs, 0 classify traces and 0 turn, instructions and capture traces older than 2026-09-12T12:00:00Z",
				[]int{1, 3, 11, 20, 35, 36, 40, 45}, 0},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newEffectRecords(t, tc.args.retention, tc.args.knob)
			before, beforeText := r.run(t, runReport, "effect", "--json"), r.run(t, runReport, "effect")

			planned := r.run(t, runRecords, "prune", "--dry-run")
			r.run(t, runRecords, "prune")
			after, afterText := r.run(t, runReport, "effect", "--json"), r.run(t, runReport, "effect")
			r.run(t, runRecords, "prune")
			again := r.run(t, runReport, "effect", "--json")

			left, rollups := r.left(t)
			assert.Contains(t, beforeText, "effect\twithheld\truns ", "the arms hold drawn runs")
			assert.Contains(t, beforeText, "producer session")
			assert.Contains(t, planned, tc.want.planned)
			assert.ElementsMatch(t, tc.want.left, left)
			assert.Equal(t, tc.want.rollups, rollups)
			assert.JSONEq(t, before, after)
			assert.Equal(t, beforeText, afterText)
			assert.JSONEq(t, before, again)
		})
	}
}
