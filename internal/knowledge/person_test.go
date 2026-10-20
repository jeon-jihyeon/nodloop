package knowledge_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestCheckApprover(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"a person's name passes", "ann", nil},
		{"an empty name is refused", "", knowledge.ErrApproverRequired},
		{"the reserved name is refused", knowledge.ApproverNodloop, knowledge.ErrApproverReserved},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, knowledge.CheckApprover(tc.args), tc.want)
		})
	}
}

// Every operation only a person decides refuses the reserved name and writes nothing
func TestLedgerRefusesReservedApprover(t *testing.T) {
	seeds := newCompactionSeeds()
	candidate := knowledge.Knowledge{
		ID: "k1", Kind: knowledge.KindMeaning, Content: "one", Run: repoRun(),
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}, Author: "author",
	}
	const reserved = knowledge.ApproverNodloop
	tcs := []struct {
		name string
		args func(ctx context.Context, l *knowledge.Ledger, compaction string) error
	}{
		{"approve", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.Approve(ctx, "k1", 1, reserved))
		}},
		{"approve in labels", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.ApproveIn(ctx, "k1", 1, trace.Labels{}, reserved))
		}},
		{"retire", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.Retire(ctx, "a", 1, reserved))
		}},
		{"reaffirm", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.Reaffirm(ctx, "a", 1, reserved))
		}},
		{"approve a compaction", func(ctx context.Context, l *knowledge.Ledger, compaction string) error {
			return testkit.Err(approveCompaction(ctx, l, compaction, reserved))
		}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), seeds.at)
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			_, _, err := l.Propose(ctx, candidate)
			require.NoError(t, err)
			proposed, err := l.ProposeCompaction(ctx, "j", seeds.drafts())
			require.NoError(t, err)
			before, err := l.All(ctx)
			require.NoError(t, err)

			err = tc.args(ctx, l, proposed.ID)

			assert.ErrorIs(t, err, knowledge.ErrApproverReserved)
			after, err := l.All(ctx)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

// A record of one version of the place repo=nodloop newest first in a set
func personRecord(id string, version int, status knowledge.Status, approver string, approvedAt time.Time) knowledge.Knowledge {
	return knowledge.Knowledge{
		ID: id, Version: version, Kind: knowledge.KindMeaning, Content: id, Run: repoRun(), Status: status, Approver: approver,
		ApprovedAt: approvedAt, Time: approvedAt,
	}
}

func TestSetPersonApproved(t *testing.T) {
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	approved, superseded := knowledge.StatusApproved, knowledge.StatusSuperseded
	reaffirmed := personRecord("b-reaffirmed", 1, approved, "ann", at)
	reaffirmed.ReviewedAt = at.Add(time.Hour)
	tcs := []struct {
		name string
		args knowledge.Set
		want []knowledge.Ref
	}{
		{
			"a version a person approved is in and one nodloop approved is not",
			knowledge.Set{personRecord("a-person", 1, approved, "ann", at), personRecord("c-nodloop", 1, approved, knowledge.ApproverNodloop, at)},
			[]knowledge.Ref{{ID: "a-person", Version: 1}},
		},
		{
			"a person's reaffirm of a version nodloop approved takes it in",
			knowledge.Set{reaffirmed, personRecord("b-reaffirmed", 1, approved, knowledge.ApproverNodloop, at)},
			[]knowledge.Ref{{ID: "b-reaffirmed", Version: 1}},
		},
		{
			"a newer version replaces the one it superseded",
			knowledge.Set{
				personRecord("d-next", 1, superseded, "ann", at), personRecord("d-next", 2, approved, "ann", at),
				personRecord("d-next", 1, approved, "ann", at),
			},
			[]knowledge.Ref{{ID: "d-next", Version: 2}},
		},
		{
			"a candidate and a retired version are out",
			knowledge.Set{personRecord("e-candidate", 1, knowledge.StatusCandidate, "", at), personRecord("f-retired", 1, knowledge.StatusRetired, "ann", at)},
			[]knowledge.Ref{},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.PersonApproved().Refs())
		})
	}
}

func TestSetApprovedBy(t *testing.T) {
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	approved := knowledge.StatusApproved
	elsewhere := personRecord("e-elsewhere", 1, approved, knowledge.ApproverNodloop, at)
	elsewhere.Run = &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}}
	reaffirmed := personRecord("r-reaffirmed", 1, approved, "ann", at)
	all := knowledge.Set{
		reaffirmed, personRecord("r-reaffirmed", 1, approved, knowledge.ApproverNodloop, at),
		personRecord("a-new", 1, approved, knowledge.ApproverNodloop, at), personRecord("b-old", 1, approved, knowledge.ApproverNodloop, at.Add(-time.Hour)),
		personRecord("p-person", 1, approved, "ann", at), elsewhere,
	}
	type args struct {
		approver string
		since    time.Time
		labels   trace.Labels
	}
	tcs := []struct {
		name string
		args args
		want []knowledge.Ref
	}{
		{"nodloop since a time in the place", args{knowledge.ApproverNodloop, at, trace.Labels{"repo": {"nodloop"}}}, []knowledge.Ref{{ID: "a-new", Version: 1}}},
		{
			"an earlier time takes in the older approval",
			args{knowledge.ApproverNodloop, at.Add(-time.Hour), trace.Labels{"repo": {"nodloop"}}},
			[]knowledge.Ref{{ID: "a-new", Version: 1}, {ID: "b-old", Version: 1}},
		},
		{"another place", args{knowledge.ApproverNodloop, at, trace.Labels{"repo": {"acme"}}}, []knowledge.Ref{{ID: "e-elsewhere", Version: 1}}},
		{
			"a person's name gives what the person stood behind last",
			args{"ann", at, trace.Labels{"repo": {"nodloop"}}},
			[]knowledge.Ref{{ID: "p-person", Version: 1}, {ID: "r-reaffirmed", Version: 1}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, all.ApprovedBy(tc.args.approver, tc.args.since, "session", tc.args.labels).Refs())
		})
	}
}

// One version a person approved reaches every run of the scope or none does
func TestSetPersonReaches(t *testing.T) {
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	approved := knowledge.StatusApproved
	scoped := func(id, approver string, run knowledge.RunScope) knowledge.Knowledge {
		k := personRecord(id, 1, approved, approver, at)
		k.Run = &run
		return k
	}
	all := knowledge.Set{
		scoped("a-acme", "ann", knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}}),
		scoped("b-svc", "ann", knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"svc"}}}),
		scoped("c-nodloop", "ann", knowledge.RunScope{
			Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}, Except: trace.Labels{"dir": {"tmp"}},
		}),
		scoped("d-everywhere", knowledge.ApproverNodloop, knowledge.RunScope{Producer: "session"}),
	}
	tcs := []struct {
		name string
		args knowledge.RunScope
		want bool
	}{
		{"the scope of a version a person approved", knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}}, true},
		{
			"a narrower scope that keeps the exception",
			knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}, Except: trace.Labels{"dir": {"tmp", "vendor"}}},
			true,
		},
		{
			"a scope whose required labels leave the excepted value out",
			knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}, "dir": {"cmd"}}},
			true,
		},
		{"a scope that lifts the exception", knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}, false},
		{"two places two versions reach only together", knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme", "svc"}}}, false},
		{"every run of the producer which only nodloop reaches", knowledge.RunScope{Producer: "session"}, false},
		{"another producer", knowledge.RunScope{Producer: "tep", Labels: trace.Labels{"repo": {"acme"}}}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, all.PersonReaches(tc.args))
		})
	}
}
