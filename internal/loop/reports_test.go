package loop_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// A report with a cutoff reads only the runs since it
func TestStoresReportWindow(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	old, err := trace.NewRun("session", "", nil, []byte(`{"applied":[]}`), []byte("old"), st.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, st.Traces.Append(ctx, old.Record()))
	cutoff := st.Clock.Now()
	young, err := trace.NewRun("session", "", nil, []byte(`{"applied":[]}`), []byte("young"), st.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, st.Traces.Append(ctx, young.Record()))
	tcs := []struct {
		name string
		args time.Time
		want int
	}{
		{"no cutoff counts every run", time.Time{}, 2},
		{"a cutoff counts the runs since it", cutoff, 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stores := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Outcomes: st.Outcomes, Items: st.Ledger, Since: tc.args}
			snap, err := stores.Snapshot(ctx, loop.ReportLoop)
			require.NoError(t, err)
			assert.Equal(t, tc.want, snap.Loop().Totals.Runs)
		})
	}
}

// The critic report counts the classify traces of the window since a prune removes the old ones with their runs
func TestStoresReportCriticWindow(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	classify := func(at time.Time) {
		tr := trace.Trace{
			ID: trace.NewID(at), Name: trace.NameClassify, Subject: "critic", Ref: "run", Time: at, Input: json.RawMessage(`{}`),
			Output: json.RawMessage(`{"members":[{"name":"laya","answers":{"holds":{"yes":0.9}}}]}`),
		}
		require.NoError(t, st.Traces.Append(ctx, tr.Record()))
	}
	classify(st.Clock.Now())
	cutoff := st.Clock.Now().Add(time.Minute)
	classify(cutoff.Add(time.Minute))
	tcs := []struct {
		name string
		args time.Time
		want int
	}{
		{"no cutoff counts every classify trace", time.Time{}, 2},
		{"a cutoff counts the classify traces since it", cutoff, 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stores := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Outcomes: st.Outcomes, Items: st.Ledger, Since: tc.args}
			snap, err := stores.Snapshot(ctx, loop.ReportCritic)
			require.NoError(t, err)
			rows := snap.Critics().Critics
			require.Len(t, rows, 1)
			assert.Equal(t, tc.want, rows[0].Judged)
		})
	}
}

// Every report name answers its report as JSON and an unknown name is refused before any read
func TestStoresReport(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}}, []byte(`{"applied":[]}`), []byte("answer"), st.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, st.Traces.Append(ctx, run.Record()))
	stores := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Outcomes: st.Outcomes, Items: st.Ledger}
	// The JSON of a report as Report writes it
	reportJSON := func(report any) string {
		b, err := json.Marshal(report)
		require.NoError(t, err)
		return string(b)
	}
	type want struct {
		// Empty for a refused name
		report string
		err    error
	}
	tcs := []struct {
		name string
		args loop.ReportName
		want want
	}{
		{"loop holds the totals", loop.ReportLoop, want{report: reportJSON(loop.LoopReport{
			Totals: loop.Totals{Runs: 1}, Scopes: []loop.ScopeRow{}, Drafts: []loop.DraftRow{}, Items: []loop.RunItem{},
		})}},
		{"extract has no row without an extraction", loop.ReportExtract, want{report: reportJSON([]loop.ExtractRow{})}},
		{"critic has no row without a critic", loop.ReportCritic, want{report: reportJSON(loop.CriticReport{
			Critics: []loop.CriticRow{}, Reactions: []loop.ReactionRow{}, Capture: loop.CaptureRow{Critic: "capture"},
		})}},
		{
			"effect has a row per arm without runs that received items",
			loop.ReportEffect,
			want{report: reportJSON([]loop.EffectRow{{Arm: "applied", Warnings: []loop.EffectWarning{}}, {Arm: "withheld", Warnings: []loop.EffectWarning{}}})},
		},
		{"health has no row without knowledge", loop.ReportHealth, want{report: reportJSON([]loop.Health{})}},
		{"replay has no row without a replay", loop.ReportReplay, want{report: reportJSON([]loop.ReplayRow{})}},
		{"an unknown name is refused", "nope", want{err: loop.ErrReportUnknown}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := stores.Report(ctx, tc.args, st.Clock.Now())

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.report, string(got))
		})
	}
}

// A store that fails to read fails every report that reads it
func TestStoresReportLoadFails(t *testing.T) {
	type args struct {
		name loop.ReportName
		// Reads of the knowledge, the feedback and the trace store that succeed before each fails
		items, verdicts, lists int
		// The lines of outcomes.jsonl
		outcomes string
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"loop fails on the knowledge", args{loop.ReportLoop, 0, 1, 9, ""}, assert.AnError},
		{"loop fails on the verdicts", args{loop.ReportLoop, 1, 0, 9, ""}, assert.AnError},
		{"replay fails on the knowledge", args{loop.ReportReplay, 0, 1, 9, ""}, assert.AnError},
		{"health fails on the knowledge", args{loop.ReportHealth, 0, 1, 9, ""}, assert.AnError},
		{"health fails on the verdicts", args{loop.ReportHealth, 1, 0, 9, ""}, assert.AnError},
		{"critic fails on the traces", args{loop.ReportCritic, 9, 9, 0, ""}, assert.AnError},
		{"effect fails on the traces read with the knob traces", args{loop.ReportEffect, 9, 9, 0, ""}, assert.AnError},
		{"effect fails on a corrupt outcome", args{loop.ReportEffect, 9, 9, 9, "not json\n"}, feedbackfile.ErrCorrupt},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			require.NoError(t, os.WriteFile(filepath.Join(st.Dir, "outcomes.jsonl"), []byte(tc.args.outcomes), 0o600))
			dir := t.TempDir()
			items, err := knowledgefile.New(dir)
			require.NoError(t, err)
			ledger := knowledge.NewLedger(
				&testkit.FlakyKnowledge{Store: items, Reads: testkit.Reads{Allowed: tc.args.items, Err: assert.AnError}},
				vetofile.NewApprovedFile(t.TempDir(), dir), st.Clock.Now, func(prefix string) string { return prefix + "generated" },
			)
			verdicts := &testkit.FlakyFeedback{Store: st.Feedback, Reads: testkit.Reads{Allowed: tc.args.verdicts, Err: assert.AnError}}
			traces := &testkit.FlakyTraces{Store: st.Traces, Lists: testkit.Reads{Allowed: tc.args.lists, Err: assert.AnError}}
			stores := loop.Stores{Traces: traces, Verdicts: verdicts, Outcomes: st.Outcomes, Items: ledger}

			got, err := stores.Report(context.Background(), tc.args.name, st.Clock.Now())

			assert.ErrorIs(t, err, tc.want)
			assert.Nil(t, got)
		})
	}
}

func TestSnapshotReplays(t *testing.T) {
	failed := `{"id":"a","version":1,"cases":[{"expect":"breaks"},{"expect":"keeps"},{"expect":"keeps"}],"missed":0,"overreach":1}`
	passed := `{"id":"a","version":1,"cases":[{"expect":"breaks"},{"expect":"keeps"}],"missed":0,"overreach":0}`
	partial := `{"id":"a","version":1,"cases":[{"expect":"breaks"}],"missed":0,"overreach":0}`
	type replayed struct {
		output string
		err    string
	}
	type want struct {
		// Rows without their time which is the time of the newest replay that did not fail
		rows     []loop.ReplayRow
		outcomes []string
	}
	tcs := []struct {
		name string
		// Replay traces oldest first
		// Each comes a clock step after the one before
		args []replayed
		want want
	}{
		{"no replay has no row", nil, want{[]loop.ReplayRow{}, []string{}}},
		{
			"the newest replay of a version wins",
			[]replayed{{output: failed}, {output: passed}},
			want{[]loop.ReplayRow{{ID: "a", Version: 1, Passed: true, Corrected: 1, Approved: 1}}, []string{"passed"}},
		},
		{
			"cases count by what they expect",
			[]replayed{{output: failed}},
			want{[]loop.ReplayRow{{ID: "a", Version: 1, Corrected: 1, Approved: 2, Overreach: 1}}, []string{"failed"}},
		},
		{
			"an axis with no case is partial and never passed",
			[]replayed{{output: partial}},
			want{[]loop.ReplayRow{{ID: "a", Version: 1, Corrected: 1}}, []string{"partial"}},
		},
		{"an output that does not decode is left out", []replayed{{output: `"text"`}}, want{[]loop.ReplayRow{}, []string{}}},
		{"a failed replay is left out", []replayed{{output: failed}, {err: "replay: the judgment is incomplete"}},
			want{[]loop.ReplayRow{
				{ID: "a", Version: 1, Corrected: 1, Approved: 2, Overreach: 1},
			}, []string{"failed"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			ctx := context.Background()
			run, err := trace.NewRun("session", "", nil, []byte(`{"applied":[]}`), []byte("answer"), st.Clock.Now())
			require.NoError(t, err)
			require.NoError(t, st.Traces.Append(ctx, run.Record()))
			var newest time.Time
			for _, r := range tc.args {
				now := st.Clock.Now()
				var output json.RawMessage
				if r.output != "" {
					output = json.RawMessage(r.output)
				}
				tr := trace.Trace{ID: trace.NewID(now), Name: trace.NameReplay, Subject: "a", Time: now, Output: output, Error: r.err}
				require.NoError(t, st.Traces.Append(ctx, tr.Record()))
				if r.err == "" {
					newest = now
				}
			}
			snap, err := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Items: st.Ledger}.Snapshot(ctx, loop.ReportReplay)
			require.NoError(t, err)

			rows := snap.Replays()

			got := want{rows: rows, outcomes: []string{}}
			for _, row := range rows {
				got.outcomes = append(got.outcomes, row.Outcome())
			}
			for i := range tc.want.rows {
				tc.want.rows[i].Time = newest
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
