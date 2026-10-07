package loop_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// Every report name answers its report as JSON and an unknown name is refused before any read
func TestStoresReport(t *testing.T) {
	st := testkit.Open(t)
	ctx := context.Background()
	run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}}, []byte(`{"applied":[]}`), []byte("answer"), st.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, st.Traces.Append(ctx, run))
	stores := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Outcomes: st.Outcomes, Items: st.Ledger}
	type want struct {
		report any
		err    error
	}
	tcs := []struct {
		name string
		args loop.ReportName
		want want
	}{
		{"loop holds the totals", loop.ReportLoop, want{report: loop.LoopReport{
			Totals: loop.Totals{Runs: 1}, Scopes: []loop.ScopeRow{}, Drafts: []loop.DraftRow{}, Items: []loop.RunItem{},
		}}},
		{"extract has no row without an extraction", loop.ReportExtract, want{report: []loop.ExtractRow{}}},
		{"critic has no row without a critic", loop.ReportCritic, want{report: []loop.CriticRow{}}},
		{"effect has a row per arm without runs that received items", loop.ReportEffect, want{report: []loop.EffectRow{{Arm: "applied"}, {Arm: "withheld"}}}},
		{"health has no row without knowledge", loop.ReportHealth, want{report: []loop.Health{}}},
		{"replay has no row without a replay", loop.ReportReplay, want{report: []loop.ReplayRow{}}},
		{"an unknown name is refused", "nope", want{err: loop.ErrReportUnknown}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := stores.Report(ctx, tc.args, st.Clock.Now())
			if tc.want.err != nil {
				assert.ErrorIs(t, err, tc.want.err)
				return
			}
			require.NoError(t, err)
			want, err := json.Marshal(tc.want.report)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got))
		})
	}
}

// A store that fails to read fails every report that reads it
func TestStoresReportLoadFails(t *testing.T) {
	type args struct {
		name loop.ReportName
		// Reads of the knowledge and the feedback store that succeed before each fails
		items, verdicts int
	}
	tcs := []struct {
		name string
		args args
	}{
		{"loop fails on the knowledge", args{loop.ReportLoop, 0, 1}},
		{"loop fails on the verdicts", args{loop.ReportLoop, 1, 0}},
		{"replay fails on the knowledge", args{loop.ReportReplay, 0, 1}},
		{"health fails on the knowledge", args{loop.ReportHealth, 0, 1}},
		{"health fails on the verdicts", args{loop.ReportHealth, 1, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			dir := t.TempDir()
			items, err := knowledgefile.New(dir)
			require.NoError(t, err)
			ledger := knowledge.NewLedger(
				&testkit.FlakyKnowledge{Store: items, Reads: testkit.Reads{Allowed: tc.args.items, Err: assert.AnError}},
				vetofile.NewApprovedFile(t.TempDir(), dir), st.Clock.Now, func(prefix string) string { return prefix + "generated" },
			)
			verdicts := &testkit.FlakyFeedback{Store: st.Feedback, Reads: testkit.Reads{Allowed: tc.args.verdicts, Err: assert.AnError}}
			stores := loop.Stores{Traces: st.Traces, Verdicts: verdicts, Outcomes: st.Outcomes, Items: ledger}

			got, err := stores.Report(context.Background(), tc.args.name, st.Clock.Now())

			assert.ErrorIs(t, err, assert.AnError)
			assert.Nil(t, got)
		})
	}
}

func TestSnapshotReplays(t *testing.T) {
	failed := `{"id":"a","version":1,"cases":[{"expect":"breaks"},{"expect":"keeps"},{"expect":"keeps"}],"missed":0,"overreach":1}`
	passed := `{"id":"a","version":1,"cases":[{"expect":"breaks"}],"missed":0,"overreach":0}`
	type replayed struct {
		output string
		err    string
	}
	tcs := []struct {
		name string
		// Replay traces oldest first
		// Each comes a clock step after the one before
		args []replayed
		// Rows without their time which is the time of the newest replay that did not fail
		want []loop.ReplayRow
	}{
		{"no replay has no row", nil, []loop.ReplayRow{}},
		{
			"the newest replay of a version wins",
			[]replayed{{output: failed}, {output: passed}},
			[]loop.ReplayRow{{ID: "a", Version: 1, Passed: true, Corrected: 1}},
		},
		{
			"cases count by what they expect",
			[]replayed{{output: failed}},
			[]loop.ReplayRow{{ID: "a", Version: 1, Corrected: 1, Approved: 2, Overreach: 1}},
		},
		{"an output that does not decode is left out", []replayed{{output: `"text"`}}, []loop.ReplayRow{}},
		{"a failed replay is left out", []replayed{{output: failed}, {err: "replay: the judgment is incomplete"}}, []loop.ReplayRow{
			{ID: "a", Version: 1, Corrected: 1, Approved: 2, Overreach: 1},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			ctx := context.Background()
			run, err := trace.NewRun("session", "", nil, []byte(`{"applied":[]}`), []byte("answer"), st.Clock.Now())
			require.NoError(t, err)
			require.NoError(t, st.Traces.Append(ctx, run))
			var newest time.Time
			for _, r := range tc.args {
				now := st.Clock.Now()
				var output json.RawMessage
				if r.output != "" {
					output = json.RawMessage(r.output)
				}
				tr := trace.Trace{ID: trace.NewID(now), Name: trace.NameReplay, Subject: "a", Time: now, Output: output, Error: r.err}
				require.NoError(t, st.Traces.Append(ctx, tr))
				if r.err == "" {
					newest = now
				}
			}
			snap, err := loop.Stores{Traces: st.Traces, Verdicts: st.Feedback, Items: st.Ledger}.Snapshot(ctx, loop.ReportReplay)
			require.NoError(t, err)

			got := snap.Replays()

			for i := range tc.want {
				tc.want[i].Time = newest
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
