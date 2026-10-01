package loop_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
)

func TestHistoryHealth(t *testing.T) {
	k1 := item("k", 1, knowledge.StatusApproved)
	later := monday.Add(time.Hour)
	uses := []diagnose.AppliedKnowledge{applied("k", 1), applied("k", 1)}
	type args struct {
		reviews  []review
		verdicts feedback.Records
		outcomes feedback.Outcomes
		items    knowledge.Set
		now      time.Time
	}
	tcs := []struct {
		name string
		args args
		want loop.Health
	}{
		{
			"an unapplied version has zero counts",
			args{items: knowledge.Set{k1}, now: monday},
			loop.Health{ID: "k", Version: 1, Status: knowledge.StatusApproved, LastReviewed: monday},
		},
		{
			"a review counts once even when it lists the version twice",
			args{reviews: []review{{id: "r", knowledge: uses}}, items: knowledge.Set{k1}, now: monday},
			loop.Health{ID: "k", Version: 1, Status: knowledge.StatusApproved, Applied: 1, LastReviewed: monday},
		},
		{
			"an item that never reached the model is not applied",
			args{
				reviews: []review{{id: "r", knowledge: []diagnose.AppliedKnowledge{{ID: "k", Version: 1}}}},
				items:   knowledge.Set{k1}, now: monday,
			},
			loop.Health{ID: "k", Version: 1, Status: knowledge.StatusApproved, LastReviewed: monday},
		},
		{
			"another version of the id is not counted",
			args{reviews: []review{{id: "r", knowledge: []diagnose.AppliedKnowledge{applied("k", 2)}}}, items: knowledge.Set{k1}, now: monday},
			loop.Health{ID: "k", Version: 1, Status: knowledge.StatusApproved, LastReviewed: monday},
		},
		{
			"the latest verdict and outcome count and a refuted one flags a retire candidate",
			args{
				reviews: []review{{id: "r", knowledge: uses}},
				verdicts: feedback.Records{
					verdict("r", feedback.VerdictReject, later), verdict("r", feedback.VerdictApprove, monday),
				},
				outcomes: feedback.Outcomes{
					outcome("r", feedback.ResultRefuted, later), outcome("r", feedback.ResultConfirmed, monday),
				},
				items: knowledge.Set{k1}, now: monday,
			},
			loop.Health{
				ID: "k", Version: 1, Status: knowledge.StatusApproved, Applied: 1, Rejected: 1, Refuted: 1,
				RetireCandidate: true, LastReviewed: monday,
			},
		},
		{
			"a tie of confirmed and refuted still flags",
			args{
				reviews:  []review{{id: "r1", knowledge: uses}, {id: "r2", knowledge: uses}},
				outcomes: feedback.Outcomes{outcome("r1", feedback.ResultRefuted, monday), outcome("r2", feedback.ResultConfirmed, monday)},
				items:    knowledge.Set{k1}, now: monday,
			},
			loop.Health{
				ID: "k", Version: 1, Status: knowledge.StatusApproved, Applied: 2, Confirmed: 1, Refuted: 1,
				RetireCandidate: true, LastReviewed: monday,
			},
		},
		{
			"more confirmed than refuted does not flag",
			args{
				reviews: []review{{id: "r1", knowledge: uses}, {id: "r2", knowledge: uses}, {id: "r3", knowledge: uses}},
				outcomes: feedback.Outcomes{
					outcome("r1", feedback.ResultRefuted, monday), outcome("r2", feedback.ResultConfirmed, monday),
					outcome("r3", feedback.ResultConfirmed, monday),
				},
				items: knowledge.Set{k1}, now: monday,
			},
			loop.Health{ID: "k", Version: 1, Status: knowledge.StatusApproved, Applied: 3, Confirmed: 2, Refuted: 1, LastReviewed: monday},
		},
		{
			"a session outcome and a batch review of an eval session are left out",
			args{
				reviews: []review{{id: "r1", knowledge: uses}, {id: "r2", knowledge: uses, batch: true, session: "s1"}},
				outcomes: feedback.Outcomes{{
					TraceID: "r1", Result: feedback.ResultRefuted, Time: monday, Reviewer: feedback.ReviewerSession,
				}},
				items: knowledge.Set{k1}, now: monday,
			},
			loop.Health{ID: "k", Version: 1, Status: knowledge.StatusApproved, Applied: 1, LastReviewed: monday},
		},
		{
			"an approved version past the deadline is stale",
			args{items: knowledge.Set{k1}, now: monday.AddDate(0, 0, knowledge.ReviewDays)},
			loop.Health{ID: "k", Version: 1, Status: knowledge.StatusApproved, LastReviewed: monday, Stale: true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := loop.New(traces(t, tc.args.reviews...), tc.args.verdicts, tc.args.outcomes, tc.args.items)
			require.NoError(t, err)
			assert.Equal(t, []loop.Health{tc.want}, h.Health(tc.args.now))
		})
	}
}

func TestHistoryHealthOrder(t *testing.T) {
	type row struct {
		id      string
		version int
	}
	tcs := []struct {
		name string
		args knowledge.Set
		want []row
	}{
		{
			"rows sort by id then version",
			knowledge.Set{item("b", 1, knowledge.StatusApproved), item("a", 2, knowledge.StatusCandidate), item("a", 1, knowledge.StatusApproved)},
			[]row{{"a", 1}, {"a", 2}, {"b", 1}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := loop.New(nil, nil, nil, tc.args)
			require.NoError(t, err)
			var got []row
			for _, health := range h.Health(monday) {
				got = append(got, row{health.ID, health.Version})
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestHistoryBrokenReferences(t *testing.T) {
	procedures := evidence.Procedures{{Slug: "p", Paragraphs: []evidence.Paragraph{{ID: "p#1"}}}}
	withFeedback := review{id: "with-feedback", batch: true}
	withOutcome := review{id: "with-outcome"}
	type args struct {
		evidence   knowledge.Evidence
		scope      knowledge.Scope
		exceptions []evidence.Context
	}
	tcs := []struct {
		name string
		args args
		want []loop.Issue
	}{
		{
			"every reference resolves",
			args{
				evidence: knowledge.Evidence{
					FeedbackTraceIDs: []string{"with-feedback"}, OutcomeTraceIDs: []string{"with-outcome"},
					ParagraphIDs: []string{"p#1"}, Knowledge: []knowledge.Ref{{ID: "k", Version: 1}},
				},
				scope:      knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextUnknown}, Metrics: []string{"clicks"}}},
				exceptions: []evidence.Context{evidence.ContextPlannedChange},
			},
			[]loop.Issue{},
		},
		{
			"a feedback trace without feedback",
			args{evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"with-outcome", "with-outcome"}}},
			[]loop.Issue{{ID: "k", Version: 2, Field: "feedback_trace_ids", Reference: "with-outcome"}},
		},
		{
			"an outcome trace without an outcome",
			args{evidence: knowledge.Evidence{OutcomeTraceIDs: []string{"with-feedback"}}},
			[]loop.Issue{{ID: "k", Version: 2, Field: "outcome_trace_ids", Reference: "with-feedback"}},
		},
		{
			"a paragraph outside the procedures",
			args{evidence: knowledge.Evidence{ParagraphIDs: []string{"p#9"}}},
			[]loop.Issue{{ID: "k", Version: 2, Field: "paragraph_ids", Reference: "p#9"}},
		},
		{
			"a knowledge ref never recorded",
			args{evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}, Knowledge: []knowledge.Ref{{ID: "gone", Version: 3}}}},
			[]loop.Issue{{ID: "k", Version: 2, Field: "knowledge", Reference: "gone v3"}},
		},
		{
			"an unknown change context and exception and an unobserved metric",
			args{
				evidence:   knowledge.Evidence{ParagraphIDs: []string{"p#1"}},
				scope:      knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{"old"}, Metrics: []string{"views"}}},
				exceptions: []evidence.Context{"older"},
			},
			[]loop.Issue{
				{ID: "k", Version: 2, Field: "change_contexts", Reference: "old"},
				{ID: "k", Version: 2, Field: "exceptions", Reference: "older"},
				{ID: "k", Version: 2, Field: "metrics", Reference: "views"},
			},
		},
		{
			"a dim key and a dim value no event carries",
			args{
				evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}},
				scope:    knowledge.Scope{Dims: map[string]string{"source": "source_a", "platform": "ios"}},
			},
			[]loop.Issue{
				{ID: "k", Version: 2, Field: "dims", Reference: "platform=ios"},
				{ID: "k", Version: 2, Field: "dims", Reference: "source=source_a"},
			},
		},
		{
			"exceptions that cover every scoped change context",
			args{
				evidence:   knowledge.Evidence{ParagraphIDs: []string{"p#1"}},
				scope:      knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}}},
				exceptions: []evidence.Context{evidence.ContextNoKnownChange},
			},
			[]loop.Issue{{ID: "k", Version: 2, Field: "exceptions", Reference: "[no_known_change]"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			audited := item("k", 2, knowledge.StatusApproved)
			audited.Evidence, audited.Scope, audited.Exceptions = tc.args.evidence, tc.args.scope, tc.args.exceptions
			superseded := item("k", 1, knowledge.StatusSuperseded)
			h, err := loop.New(
				traces(t, withFeedback, withOutcome),
				feedback.Records{verdict("with-feedback", feedback.VerdictApprove, monday)},
				feedback.Outcomes{outcome("with-outcome", feedback.ResultConfirmed, monday)},
				knowledge.Set{audited, superseded},
			)
			require.NoError(t, err)
			assert.Equal(t, tc.want, h.BrokenReferences(procedures, []string{"clicks"}, knowledge.Dims{"source": {"source-a": {}}}))
		})
	}
}

func TestHistoryRefuted(t *testing.T) {
	uses := []diagnose.AppliedKnowledge{applied("k", 1)}
	type args struct {
		reviews []review
		refuted []string
	}
	type want struct {
		contexts []evidence.Context
		traceIDs []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no refuted review gives nothing", args{reviews: []review{{id: "r1", knowledge: uses}}}, want{}},
		{
			"refuted reviews give their contexts once and their ids sorted",
			args{
				reviews: []review{
					{id: "r3", knowledge: uses, context: evidence.ContextUnknown},
					{id: "r2", knowledge: uses, context: evidence.ContextPlannedChange},
					{id: "r1", knowledge: uses, context: evidence.ContextUnknown},
					{id: "r4", context: evidence.ContextNoKnownChange},
				},
				refuted: []string{"r1", "r2", "r3", "r4"},
			},
			want{
				contexts: []evidence.Context{evidence.ContextPlannedChange, evidence.ContextUnknown},
				traceIDs: []string{"r1", "r2", "r3"},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var outcomes feedback.Outcomes
			for _, id := range tc.args.refuted {
				outcomes = append(outcomes, outcome(id, feedback.ResultRefuted, monday))
			}
			h, err := loop.New(traces(t, tc.args.reviews...), nil, outcomes, knowledge.Set{item("k", 1, knowledge.StatusApproved)})
			require.NoError(t, err)
			assert.Equal(t, tc.want.contexts, h.RefutedContexts("k", 1))
			assert.Equal(t, tc.want.traceIDs, h.RefutedTraces("k", 1))
		})
	}
}

// Outcomes of reviews that applied versions a compaction merged pass to the version it made
func TestHistoryCarriedOutcomes(t *testing.T) {
	quiet := knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}}}
	merged := func(id string, version int) knowledge.Knowledge {
		k := item(id, version, knowledge.StatusSuperseded)
		if id == "y" {
			k.Status = knowledge.StatusRetired
		}
		return k
	}
	x1, y1 := merged("x", 1), merged("y", 1)
	// x v2 compacts x v1 and y v1 into no_known_change
	compacted := func(edit func(*knowledge.Knowledge)) knowledge.Knowledge {
		k := item("x", 2, knowledge.StatusApproved)
		k.Scope, k.Compaction, k.Supersedes = quiet, "c-1", 1
		k.Evidence.Knowledge = []knowledge.Ref{{ID: "x", Version: 1}, {ID: "y", Version: 1}}
		edit(&k)
		return k
	}
	keep := func(*knowledge.Knowledge) {}
	onX1 := []diagnose.AppliedKnowledge{applied("x", 1)}
	onBoth := []diagnose.AppliedKnowledge{applied("x", 1), applied("y", 1)}
	quietReview := func(id string, uses []diagnose.AppliedKnowledge) review {
		return review{id: id, knowledge: uses, context: evidence.ContextNoKnownChange}
	}
	type args struct {
		items    knowledge.Set
		reviews  []review
		outcomes feedback.Outcomes
	}
	type want struct {
		confirmed, refuted int
		retire             bool
		contexts           []evidence.Context
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a refuted review of a merged version flags the compacted version and names its change context",
			args{
				knowledge.Set{x1, y1, compacted(keep)}, []review{quietReview("r1", onX1)},
				feedback.Outcomes{outcome("r1", feedback.ResultRefuted, monday)},
			},
			want{refuted: 1, retire: true, contexts: []evidence.Context{evidence.ContextNoKnownChange}},
		},
		{
			"a review that applied both merged versions counts once",
			args{
				knowledge.Set{x1, y1, compacted(keep)}, []review{quietReview("r1", onBoth)},
				feedback.Outcomes{outcome("r1", feedback.ResultRefuted, monday)},
			},
			want{refuted: 1, retire: true, contexts: []evidence.Context{evidence.ContextNoKnownChange}},
		},
		{
			"carried confirmed reviews outweigh a carried refuted one",
			args{
				knowledge.Set{x1, y1, compacted(keep)},
				[]review{quietReview("r1", onX1), quietReview("r2", onX1), quietReview("r3", onBoth)},
				feedback.Outcomes{
					outcome("r1", feedback.ResultRefuted, monday), outcome("r2", feedback.ResultConfirmed, monday),
					outcome("r3", feedback.ResultConfirmed, monday),
				},
			},
			want{confirmed: 2, refuted: 1, contexts: []evidence.Context{evidence.ContextNoKnownChange}},
		},
		{
			"a review the version cites as outcome evidence is answered and not carried",
			args{
				knowledge.Set{x1, y1, compacted(func(k *knowledge.Knowledge) { k.Evidence.OutcomeTraceIDs = []string{"r1"} })},
				[]review{quietReview("r1", onX1)}, feedback.Outcomes{outcome("r1", feedback.ResultRefuted, monday)},
			},
			want{},
		},
		{
			"a review of a change context the version no longer reaches is not carried",
			args{
				knowledge.Set{x1, y1, compacted(keep)},
				[]review{{id: "r1", knowledge: onX1, context: evidence.ContextPlannedChange}},
				feedback.Outcomes{outcome("r1", feedback.ResultRefuted, monday)},
			},
			want{},
		},
		{
			"a version that cites another item without a compaction carries nothing",
			args{
				knowledge.Set{x1, y1, compacted(func(k *knowledge.Knowledge) { k.Compaction = "" })},
				[]review{quietReview("r1", onX1)}, feedback.Outcomes{outcome("r1", feedback.ResultRefuted, monday)},
			},
			want{},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := loop.New(traces(t, tc.args.reviews...), nil, tc.args.outcomes, tc.args.items)
			require.NoError(t, err)
			rows := h.Health(monday)
			i := slices.IndexFunc(rows, func(row loop.Health) bool { return row.ID == "x" && row.Version == 2 })
			require.GreaterOrEqual(t, i, 0)

			got := want{rows[i].CarriedConfirmed, rows[i].CarriedRefuted, rows[i].RetireCandidate, h.RefutedContexts("x", 2)}
			assert.Equal(t, tc.want, got)
		})
	}
}
