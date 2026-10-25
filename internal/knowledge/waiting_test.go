package knowledge_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestSetWaiting(t *testing.T) {
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	repo := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	other := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"other"}}}
	// A record of the id and version at the minute with the teaching runs
	record := func(id string, version int, status knowledge.Status, run *knowledge.RunScope, minute int, runs ...string) knowledge.Knowledge {
		k := runItem(id, run)
		k.Version, k.Status, k.Time = version, status, at.Add(time.Duration(minute)*time.Minute)
		if len(runs) > 0 {
			k.Evidence.FeedbackTraceIDs = runs
			for _, id := range runs {
				k.Evidence = k.Evidence.Join(id, (trace.Trace{ID: id}).EvidenceUnit())
			}
		}
		if status != knowledge.StatusCandidate {
			k.Approver = "ann"
		}
		return k
	}
	compacted := record("compacted", 1, knowledge.StatusCandidate, repo, 0)
	compacted.Compaction = "c1"
	older := record("old", 1, knowledge.StatusCandidate, repo, 1)
	tcs := []struct {
		name string
		// Newest first as the store lists them
		args knowledge.Set
		want []string
	}{
		{
			"a new id and the next version of an approved one wait while retired and compacted ones do not",
			knowledge.Set{
				record("retired", 1, knowledge.StatusRetired, repo, 5), record("retired", 1, knowledge.StatusCandidate, repo, 4),
				record("next", 2, knowledge.StatusCandidate, repo, 3), record("next", 1, knowledge.StatusApproved, repo, 2),
				record("new", 1, knowledge.StatusCandidate, repo, 1), record("elsewhere", 1, knowledge.StatusCandidate, other, 1), compacted,
			},
			[]string{"next v2", "new v1"},
		},
		{
			"two candidate versions of one id wait as the newest",
			knowledge.Set{record("old", 2, knowledge.StatusCandidate, repo, 2), older},
			[]string{"old v2"},
		},
		{
			"an older candidate waits once its newer version is retired",
			knowledge.Set{record("old", 2, knowledge.StatusRetired, repo, 3), record("old", 2, knowledge.StatusCandidate, repo, 2), older},
			[]string{"old v1"},
		},
		{
			"more teaching runs wait first even when older",
			knowledge.Set{record("fresh", 1, knowledge.StatusCandidate, repo, 9), record("taught", 1, knowledge.StatusCandidate, repo, 1, "r1", "r2")},
			[]string{"taught v1", "fresh v1"},
		},
		{
			"among equal runs the later propose waits first",
			knowledge.Set{record("b", 1, knowledge.StatusCandidate, repo, 9), record("a", 1, knowledge.StatusCandidate, repo, 1)},
			[]string{"b v1", "a v1"},
		},
		{
			"a corroborate makes a candidate recent again",
			knowledge.Set{
				record("a", 1, knowledge.StatusCandidate, repo, 10, "r1"), record("b", 1, knowledge.StatusCandidate, repo, 2),
				record("a", 1, knowledge.StatusCandidate, repo, 1, "r1"),
			},
			[]string{"a v1", "b v1"},
		},
		{
			"equal runs and times wait in id order",
			knowledge.Set{record("b", 1, knowledge.StatusCandidate, repo, 1), record("a", 1, knowledge.StatusCandidate, repo, 1)},
			[]string{"a v1", "b v1"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, k := range tc.args.Waiting("session", trace.Labels{"repo": {"nodloop"}}) {
				got = append(got, fmt.Sprintf("%s v%d", k.ID, k.Version))
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

// The retired versions a run reaches of the ids it reaches no current version of
func TestSetRetired(t *testing.T) {
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	repo := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	other := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"other"}}}
	// A record of the id and version at the minute
	record := func(id string, version int, status knowledge.Status, run *knowledge.RunScope, minute int) knowledge.Knowledge {
		k := runItem(id, run)
		k.Version, k.Status, k.Time = version, status, at.Add(time.Duration(minute)*time.Minute)
		if status != knowledge.StatusCandidate {
			k.Approver = "ann"
		}
		return k
	}
	compacted := record("merged", 1, knowledge.StatusRetired, repo, 3)
	compacted.Compaction, compacted.Reason = "c1", knowledge.ReasonCompaction
	tcs := []struct {
		name string
		// Newest first as the store lists them
		args knowledge.Set
		want []string
	}{
		{
			"a dropped candidate and a withdrawn item are listed newest retire first",
			knowledge.Set{
				record("dropped", 1, knowledge.StatusRetired, repo, 4), record("withdrawn", 1, knowledge.StatusRetired, repo, 3),
				record("dropped", 1, knowledge.StatusCandidate, repo, 2), record("withdrawn", 1, knowledge.StatusApproved, repo, 1),
			},
			[]string{"dropped v1", "withdrawn v1"},
		},
		{
			"an id with a waiting candidate or an approved version lists none of its retired versions",
			knowledge.Set{
				record("next", 2, knowledge.StatusCandidate, repo, 3), record("next", 1, knowledge.StatusRetired, repo, 2),
				record("kept", 2, knowledge.StatusApproved, repo, 2), record("kept", 1, knowledge.StatusRetired, repo, 1),
			},
			nil,
		},
		{
			"the highest retired version stands for its id",
			knowledge.Set{record("old", 1, knowledge.StatusRetired, repo, 3), record("old", 2, knowledge.StatusRetired, repo, 2)},
			[]string{"old v2"},
		},
		{
			"a superseded version, a version of another place, a compaction and a record without a run are left out",
			knowledge.Set{
				record("moved", 2, knowledge.StatusRetired, other, 5), record("moved", 1, knowledge.StatusSuperseded, repo, 4), compacted,
				record("legacy", 1, knowledge.StatusRetired, nil, 1),
			},
			nil,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, k := range tc.args.Retired("session", trace.Labels{"repo": {"nodloop"}}) {
				got = append(got, fmt.Sprintf("%s v%d", k.ID, k.Version))
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

// Every waiting candidate of any producer and labels in the Waiting order
func TestSetAllWaiting(t *testing.T) {
	candidate := func(id string, run *knowledge.RunScope, runs ...string) knowledge.Knowledge {
		k := runItem(id, run)
		k.Version, k.Status, k.Evidence.FeedbackTraceIDs = 1, knowledge.StatusCandidate, runs
		for _, id := range runs {
			k.Evidence = k.Evidence.Join(id, (trace.Trace{ID: id}).EvidenceUnit())
		}
		return k
	}
	compacted := candidate("compacted", repoRun(), "r1")
	compacted.Compaction = "c1"
	approved := runItem("approved", repoRun())
	approved.Version, approved.Status, approved.Approver = 1, knowledge.StatusApproved, "ann"
	bot := candidate("bot", &knowledge.RunScope{Producer: "bot"}, "r1")
	other := candidate("other", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"other"}}}, "r1", "r2")
	tcs := []struct {
		name string
		args knowledge.Set
		want []string
	}{
		{"no records wait for nothing", knowledge.Set{}, nil},
		{"candidates of every producer and labels wait with more teaching runs first", knowledge.Set{bot, other}, []string{"other", "bot"}},
		{
			"compaction candidates, approved items and items without a run never wait",
			knowledge.Set{bot, other, candidate("legacy", nil, "r1", "r2", "r3"), compacted, approved},
			[]string{"other", "bot"},
		},
		{"an approved item alone leaves nothing waiting", knowledge.Set{approved}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, k := range tc.args.AllWaiting() {
				got = append(got, k.ID)
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSetClusters(t *testing.T) {
	scope := func(producer string, labels trace.Labels) *knowledge.RunScope {
		return &knowledge.RunScope{Producer: producer, Labels: labels}
	}
	item := runItem
	tcs := []struct {
		name string
		args knowledge.Set
		want [][]string
	}{
		{"no items give no cluster", knowledge.Set{}, [][]string{}},
		{
			"items one run may carry join and disjoint scopes stay apart",
			knowledge.Set{
				item("a", scope("session", trace.Labels{"repo": {"nodloop"}})), item("b", scope("session", trace.Labels{"repo": {"other"}})),
				item("c", scope("session", trace.Labels{"repo": {"nodloop"}, "dir": {"."}})),
			},
			[][]string{{"a", "c"}, {"b"}},
		},
		{
			"two producers never join",
			knowledge.Set{item("a", scope("session", nil)), item("b", scope("bot", nil))},
			[][]string{{"a"}, {"b"}},
		},
		{
			"an item that joins two clusters merges them in the order of the set",
			knowledge.Set{
				item("a", scope("session", trace.Labels{"repo": {"x"}})), item("b", scope("session", trace.Labels{"repo": {"y"}})),
				item("c", scope("session", trace.Labels{"repo": {"z"}})), item("d", scope("session", trace.Labels{"repo": {"x", "z"}})),
			},
			[][]string{{"a", "c", "d"}, {"b"}},
		},
		{"an item without a run scope joins none", knowledge.Set{item("a", nil), item("b", scope("session", nil))}, [][]string{{"a"}, {"b"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := [][]string{}
			for _, c := range tc.args.Clusters() {
				var ids []string
				for _, k := range c {
					ids = append(ids, k.ID)
				}
				got = append(got, ids)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// A ledger with the corrected run run-2 beside the candidate k1 taught by run-1
func waitingLedger(t *testing.T, at time.Time, seeds ...knowledge.Knowledge) *knowledge.Ledger {
	t.Helper()
	l, _ := newTestLedger(t, t.TempDir(), at)
	require.NoError(t, testkit.Err(l.Import(context.Background(), seeds)))
	return l
}

func TestLedgerCorroborate(t *testing.T) {
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	candidate := runItem("k1", repoRun())
	candidate.Version, candidate.Status, candidate.Time, candidate.Basis = 1, knowledge.StatusCandidate, at.Add(-time.Hour), knowledge.BasisStated
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "ann", candidate.Time
	newer := candidate
	newer.Version, newer.Base = 2, 0
	run := trace.Trace{ID: "run-2", Name: trace.NameRun, Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	reject := feedback.Records{{TraceID: "run-2", Verdict: feedback.VerdictReject, Reviewer: "ann", Time: at}}
	type args struct {
		seeds    []knowledge.Knowledge
		version  int
		run      trace.Trace
		verdicts feedback.Records
	}
	type want struct {
		runs []string
		// Records of k1 after the call
		records int
		// The returned candidate state and empty on a refusal
		status  knowledge.Status
		content string
		scope   *knowledge.RunScope
		err     error
	}
	waits := func(runs []string, records int) want {
		return want{runs: runs, records: records, status: knowledge.StatusCandidate, content: candidate.Content, scope: candidate.Run}
	}
	elsewhere := run
	elsewhere.Labels = trace.Labels{"repo": {"other"}}
	failed := run
	failed.Error = "timeout"
	cited := run
	cited.ID = "run-1"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a corrected run joins the teaching runs", args{[]knowledge.Knowledge{candidate}, 1, run, reject}, waits([]string{"run-1", "run-2"}, 2)},
		{
			"a run the candidate already cites appends nothing",
			args{[]knowledge.Knowledge{candidate}, 1, cited, feedback.Records{{TraceID: "run-1", Verdict: feedback.VerdictReject, Reviewer: "ann", Time: at}}},
			waits([]string{"run-1"}, 1),
		},
		{"an older candidate version is refused", args{[]knowledge.Knowledge{candidate, newer}, 1, run, reject}, want{records: 2, err: knowledge.ErrNotCandidate}},
		{"an approved version is refused", args{[]knowledge.Knowledge{candidate, approved}, 1, run, reject}, want{records: 2, err: knowledge.ErrNotCandidate}},
		{"an unknown version is refused", args{[]knowledge.Knowledge{candidate}, 9, run, reject}, want{records: 1, err: knowledge.ErrNotCandidate}},
		{"a failed run is refused", args{[]knowledge.Knowledge{candidate}, 1, failed, reject}, want{records: 1, err: trace.ErrFailedRun}},
		{
			"a run whose latest verdict approves is refused",
			args{[]knowledge.Knowledge{candidate}, 1, run, feedback.Records{{TraceID: "run-2", Verdict: feedback.VerdictApprove, Reviewer: "ann", Time: at}}},
			want{records: 1, err: knowledge.ErrNotCorrected},
		},
		{"a run without a verdict is refused", args{[]knowledge.Knowledge{candidate}, 1, run, nil}, want{records: 1, err: knowledge.ErrNotCorrected}},
		{"a run the scope does not admit is refused", args{[]knowledge.Knowledge{candidate}, 1, elsewhere, reject}, want{records: 1, err: knowledge.ErrScopeWidened}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := waitingLedger(t, at, tc.args.seeds...)

			got, err := l.Corroborate(ctx, "k1", tc.args.version, tc.args.run, tc.args.verdicts)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.runs, got.Evidence.FeedbackTraceIDs)
			history, err := l.History(ctx, "k1")
			require.NoError(t, err)
			assert.Len(t, history, tc.want.records)
			assert.Equal(t, tc.want.content, got.Content, "the text a person approves never changes")
			assert.Equal(t, tc.want.status, got.Status)
			assert.Equal(t, tc.want.scope, got.Run)
		})
	}
}
