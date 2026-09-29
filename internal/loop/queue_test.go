package loop_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestHistoryRank(t *testing.T) {
	stated := item("stated", 1, knowledge.StatusApproved)
	verified := item("verified", 1, knowledge.StatusApproved)
	verified.Basis = knowledge.BasisVerified
	planned := item("planned", 1, knowledge.StatusApproved)
	planned.Scope = knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}}}
	covered := knowledge.Set{planned, stated, verified}
	later := monday.Add(time.Hour)
	earlier := monday.Add(-time.Hour)
	type args struct {
		history   []review
		extra     trace.Traces
		verdicts  feedback.Records
		items     knowledge.Set
		candidate review
	}
	type want struct {
		score   int
		reasons []loop.Reason
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a plain review with nothing applied",
			args{items: covered, candidate: review{id: "c"}},
			want{2, []loop.Reason{loop.ReasonNoKnowledge, loop.ReasonFewCitations}},
		},
		{
			"a forced hold without citations",
			args{items: covered, candidate: review{id: "c", status: evidence.StatusHold, cites: []string{}, tags: []string{diagnose.TagGateHold}}},
			want{6, []loop.Reason{loop.ReasonForcedHold, loop.ReasonNoKnowledge, loop.ReasonNoCitations}},
		},
		{
			"a review sent back once",
			args{items: covered, extra: trace.Traces{revise(t, "ctx-c", evidence.StatusHold)}, candidate: review{id: "c"}},
			want{5, []loop.Reason{loop.ReasonRevised, loop.ReasonNoKnowledge, loop.ReasonFewCitations}},
		},
		{
			"half of the earlier verdicts in the context corrected",
			args{
				history:   []review{{id: "h1", at: earlier}, {id: "h2", at: earlier}, {id: "h3", at: earlier, context: evidence.ContextUnknown}},
				verdicts:  feedback.Records{verdict("h1", feedback.VerdictEdit, earlier), verdict("h2", feedback.VerdictApprove, earlier), verdict("h3", feedback.VerdictReject, earlier)},
				items:     covered,
				candidate: review{id: "c", at: monday},
			},
			want{4, []loop.Reason{loop.ReasonPastCorrections, loop.ReasonNoKnowledge, loop.ReasonFewCitations}},
		},
		{
			"a verdict given after the review does not count",
			args{
				history:   []review{{id: "h1", at: earlier}},
				verdicts:  feedback.Records{verdict("h1", feedback.VerdictEdit, later)},
				items:     covered,
				candidate: review{id: "c", at: monday},
			},
			want{2, []loop.Reason{loop.ReasonNoKnowledge, loop.ReasonFewCitations}},
		},
		{
			"the first review of a stated item",
			args{items: covered, candidate: review{id: "c", knowledge: []diagnose.AppliedKnowledge{applied("stated", 1)}, cites: []string{"p#1", "p#2"}}},
			want{4, []loop.Reason{loop.ReasonNewKnowledge, loop.ReasonStatedOnly}},
		},
		{
			"a later review of a verified item",
			args{
				history:   []review{{id: "h1", at: earlier, knowledge: []diagnose.AppliedKnowledge{applied("verified", 1)}}},
				verdicts:  feedback.Records{verdict("h1", feedback.VerdictApprove, earlier)},
				items:     covered,
				candidate: review{id: "c", knowledge: []diagnose.AppliedKnowledge{applied("verified", 1)}, cites: []string{"p#1", "p#2"}},
			},
			want{0, []loop.Reason{}},
		},
		{
			"no approved item covers the context",
			args{items: knowledge.Set{planned}, candidate: review{id: "c", context: evidence.ContextNoKnownChange, cites: []string{"p#1", "p#2"}}},
			want{3, []loop.Reason{loop.ReasonNoApprovedContext, loop.ReasonNoKnowledge}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			all := append(traces(t, tc.args.history...), tc.args.extra...)
			h, err := loop.New(all, tc.args.verdicts, nil, tc.args.items)
			require.NoError(t, err)
			got, err := h.Rank(traces(t, tc.args.candidate))
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want.score, got[0].Score)
			assert.Equal(t, tc.want.reasons, got[0].Reasons)
		})
	}
}

func TestHistoryRankOrder(t *testing.T) {
	covered := knowledge.Set{item("k", 1, knowledge.StatusApproved)}
	broken := review{id: "broken"}.trace(t)
	broken.Output = []byte(`{}`)
	type want struct {
		ids []string
		err error
	}
	tcs := []struct {
		name string
		args trace.Traces
		want want
	}{
		{
			"score first then older first then trace id",
			traces(t,
				review{id: "b", at: monday},
				review{id: "a", at: monday},
				review{id: "old", at: monday.Add(-time.Hour)},
				review{id: "hold", status: evidence.StatusHold, tags: []string{diagnose.TagGateHold}, at: monday.Add(time.Hour)},
			),
			want{ids: []string{"hold", "old", "a", "b"}},
		},
		{"a candidate that does not read fails", trace.Traces{broken}, want{err: diagnose.ErrMalformed}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := loop.New(nil, nil, nil, covered)
			require.NoError(t, err)
			got, err := h.Rank(tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			var ids []string
			for _, item := range got {
				ids = append(ids, item.TraceID)
			}
			assert.Equal(t, tc.want.ids, ids)
		})
	}
}

// Five plain reviews a to e and one forced hold named top
func TestHistoryQueue(t *testing.T) {
	var pending []review
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		pending = append(pending, review{id: id})
	}
	pending = append(pending, review{id: "top", tags: []string{diagnose.TagGateHold}, status: evidence.StatusHold})
	type want struct {
		ids    []string
		audits []bool
		err    error
	}
	tcs := []struct {
		name string
		args loop.QueueOptions
		want want
	}{
		{
			"no limit lists every review without audit",
			loop.QueueOptions{},
			want{ids: []string{"top", "a", "b", "c", "d", "e"}, audits: []bool{false, false, false, false, false, false}},
		},
		{"a limit without audit keeps the top", loop.QueueOptions{Limit: 2}, want{ids: []string{"top", "a"}, audits: []bool{false, false}}},
		{
			"an audit share rounds up",
			loop.QueueOptions{Limit: 3, AuditRate: 0.2, Seed: 7},
			want{ids: []string{"top", "a", "e"}, audits: []bool{false, false, true}},
		},
		{
			"a full audit share draws every slot",
			loop.QueueOptions{Limit: 2, AuditRate: 1, Seed: 7},
			want{ids: []string{"c", "b"}, audits: []bool{true, true}},
		},
		{
			"a limit above the reviews keeps them all",
			loop.QueueOptions{Limit: 10, AuditRate: 0.5, Seed: 1},
			want{ids: []string{"top", "a", "b", "c", "d", "e"}, audits: []bool{false, false, false, false, false, true}},
		},
		{
			"a short order fits the priority slots",
			loop.QueueOptions{Limit: 10, AuditRate: 0.2, Seed: 1},
			want{ids: []string{"top", "a", "b", "c", "d", "e"}, audits: []bool{false, false, false, false, false, false}},
		},
		{"a negative limit is refused", loop.QueueOptions{Limit: -1}, want{err: loop.ErrQueueOptions}},
		{"a rate above one is refused", loop.QueueOptions{Limit: 1, AuditRate: 1.5}, want{err: loop.ErrQueueOptions}},
		{"a rate that is not a number is refused", loop.QueueOptions{Limit: 1, AuditRate: math.NaN()}, want{err: loop.ErrQueueOptions}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := loop.New(traces(t, pending...), nil, nil, nil)
			require.NoError(t, err)
			got, err := h.Queue(tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			var ids []string
			var audits []bool
			for _, item := range got {
				ids, audits = append(ids, item.TraceID), append(audits, item.Audit)
			}
			assert.Equal(t, tc.want.ids, ids)
			assert.Equal(t, tc.want.audits, audits)
			again, err := h.Queue(tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, got, again, "the same seed draws the same samples")
		})
	}
}
