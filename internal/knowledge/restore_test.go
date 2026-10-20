package knowledge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// The ledger of a person's write fixture
// a v1 approved by ann with the update a v2 waiting, the candidate k1 v1, and c v1 approved by ann and later retired
type restoreFixture struct {
	at                 time.Time
	approved, update   knowledge.Knowledge
	candidate, retired knowledge.Knowledge
}

func newRestoreFixture() restoreFixture {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	base := knowledge.Knowledge{
		Version: 1, Kind: knowledge.KindJudgment, Basis: knowledge.BasisStated, Run: repoRun(), Author: "claude", Time: at,
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}},
	}
	approved := base
	approved.ID, approved.Content, approved.Status, approved.Approver, approved.ApprovedAt = "a", "use git -C", knowledge.StatusApproved, "ann", at
	update := base
	update.ID, update.Version, update.Base, update.Content, update.Status = "a", 2, 1, "use git -C with a path", knowledge.StatusCandidate
	candidate := base
	candidate.ID, candidate.Content, candidate.Status = "k1", "say what changed", knowledge.StatusCandidate
	retired := base
	retired.ID, retired.Content, retired.Status, retired.Approver, retired.Reason = "c", "keep commits small", knowledge.StatusRetired, "ann", knowledge.ReasonPerson
	return restoreFixture{at: at, approved: approved, update: update, candidate: candidate, retired: retired}
}

func (f restoreFixture) ledger(t *testing.T) *knowledge.Ledger {
	t.Helper()
	l, _ := newTestLedger(t, t.TempDir(), f.at.Add(time.Hour))
	require.NoError(t, testkit.Err(l.Import(context.Background(), []knowledge.Knowledge{f.approved, f.update, f.candidate, f.retired})))
	return l
}

// What runs receive and what waits by id, content and scope with every version number left out
func ledgerView(t *testing.T, l *knowledge.Ledger) []string {
	t.Helper()
	all, err := l.All(context.Background())
	require.NoError(t, err)
	out := []string{}
	for _, k := range all.Approved() {
		out = append(out, fmt.Sprintf("approved %s %q %s", k.ID, k.Content, k.Run))
	}
	for _, k := range all.AllWaiting() {
		out = append(out, fmt.Sprintf("waiting %s %q %s", k.ID, k.Content, k.Run))
	}
	slices.Sort(out)
	return out
}

// Every write of a person and its reverse return what runs receive and what waits to where it was, with both writes in the records
func TestLedgerReverse(t *testing.T) {
	f := newRestoreFixture()
	type args struct {
		// The write the person made under the decision x1
		write func(ctx context.Context, l *knowledge.Ledger) error
	}
	type want struct {
		// Whether the write changed what runs receive or what waits
		changes bool
		// The records the reverse wrote as id, version and status
		wrote []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a candidate approved is retired and waits again as its next version",
			args{write: func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Approve(ctx, "k1", 1, "ann"))
			}},
			want{true, []string{"k1 v1 retired", "k1 v2 candidate"}},
		},
		{
			"an update that superseded the approved version comes back to the version before and the update waits again",
			args{write: func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Approve(ctx, "a", 2, "ann"))
			}},
			want{true, []string{"a v2 retired", "a v3 candidate", "a v3 approved", "a v4 candidate"}},
		},
		{
			"an approved version retired comes back as its next version",
			args{write: func(ctx context.Context, l *knowledge.Ledger) error { return testkit.Err(l.Retire(ctx, "a", 1, "ann")) }},
			want{true, []string{"a v3 candidate", "a v3 approved"}},
		},
		{
			"a candidate dropped waits again as its next version",
			args{write: func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Retire(ctx, "k1", 1, "ann"))
			}},
			want{true, []string{"k1 v2 candidate"}},
		},
		{
			"a candidate approved in labels is retired and waits again",
			args{write: func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.ApproveIn(ctx, "k1", 1, trace.Labels{}, "ann"))
			}},
			want{true, []string{"k1 v1 retired", "k1 v2 candidate"}},
		},
		{
			"a reaffirm changed nothing a run receives so its reverse writes nothing",
			args{write: func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Reaffirm(ctx, "a", 1, "ann"))
			}},
			want{false, []string{}},
		},
		{
			"a retired version restored is retired again",
			args{write: func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Restore(ctx, "c", 1, "ann"))
			}},
			want{true, []string{"c v2 retired"}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := f.ledger(t)
			before := ledgerView(t, l)
			require.NoError(t, tc.args.write(ctx, l.Under("x1")))
			require.Equal(t, tc.want.changes, !slices.Equal(before, ledgerView(t, l)), "the write changed what runs receive or what waits")

			wrote, err := l.Under("r1").Reverse(ctx, "x1", "ann")

			require.NoError(t, err)
			got := []string{}
			for _, k := range wrote {
				got = append(got, fmt.Sprintf("%s v%d %s", k.ID, k.Version, k.Status))
			}
			assert.Equal(t, tc.want.wrote, got)
			assert.Equal(t, before, ledgerView(t, l))
			all, err := l.All(ctx)
			require.NoError(t, err)
			assert.Equal(t, len(wrote) > 0, all.HasDecision("r1"), "the reverse names itself on what it wrote")
			assert.True(t, all.HasDecision("x1"), "the write stays in the records")
		})
	}
}

// A compaction approved under a decision and reversed leaves the old items approved and the new ones retired
func TestLedgerReverseCompaction(t *testing.T) {
	seeds := newCompactionSeeds()
	ctx := context.Background()
	l, _ := newTestLedger(t, t.TempDir(), seeds.at)
	require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
	proposed, err := l.ProposeCompaction(ctx, "j", seeds.drafts())
	require.NoError(t, err)
	before := ledgerView(t, l)
	_, err = approveCompaction(ctx, l.Under("x1"), proposed.ID, "ann")
	require.NoError(t, err)
	require.NotEqual(t, before, ledgerView(t, l))

	_, err = l.Under("r1").Reverse(ctx, "x1", "ann")

	require.NoError(t, err)
	assert.Equal(t, before, ledgerView(t, l))
}

func TestLedgerReverseRefused(t *testing.T) {
	f := newRestoreFixture()
	type args struct {
		decision, approver string
		// A write after the one the reverse names
		since func(ctx context.Context, l *knowledge.Ledger) error
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a decision no record names", args{decision: "x9", approver: "ann"}, knowledge.ErrNotFound},
		{"an empty decision", args{decision: "", approver: "ann"}, knowledge.ErrNotFound},
		{
			"an id another write changed since",
			args{decision: "x1", approver: "ann", since: func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Retire(ctx, "k1", 1, "ann"))
			}},
			knowledge.ErrReverseStale,
		},
		{"an empty name", args{decision: "x1", approver: ""}, knowledge.ErrApproverRequired},
		{"the reserved name", args{decision: "x1", approver: knowledge.ApproverNodloop}, knowledge.ErrApproverReserved},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := f.ledger(t)
			require.NoError(t, testkit.Err(l.Under("x1").Approve(ctx, "k1", 1, "ann")))
			if tc.args.since != nil {
				require.NoError(t, tc.args.since(ctx, l))
			}
			before, err := l.All(ctx)
			require.NoError(t, err)

			_, err = l.Reverse(ctx, tc.args.decision, tc.args.approver)

			assert.ErrorIs(t, err, tc.want)
			after, err := l.All(ctx)
			require.NoError(t, err)
			assert.Equal(t, before, after, "a refused reverse writes nothing")
		})
	}
}

func TestLedgerRestore(t *testing.T) {
	f := newRestoreFixture()
	type args struct {
		id       string
		version  int
		approver string
	}
	type want struct {
		// The newest record of the id after the call as version, status and approver
		got string
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a retired version comes back approved as its next version", args{"c", 1, "ann"}, want{"c v2 approved ann", nil}},
		{"an approved version is refused", args{"a", 1, "ann"}, want{"", knowledge.ErrTransitionInvalid}},
		{"an unknown version is refused", args{"c", 9, "ann"}, want{"", knowledge.ErrNotFound}},
		{"the reserved name is refused", args{"c", 1, knowledge.ApproverNodloop}, want{"", knowledge.ErrApproverReserved}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := f.ledger(t)

			k, err := l.Under("x1").Restore(ctx, tc.args.id, tc.args.version, tc.args.approver)

			assert.ErrorIs(t, err, tc.want.err)
			got := ""
			if err == nil {
				got = fmt.Sprintf("%s v%d %s %s", k.ID, k.Version, k.Status, k.Approver)
			}
			assert.Equal(t, tc.want.got, got)
		})
	}
}

// A restored version keeps the text, the scope and the evidence of the retired one and names the decision
func TestLedgerRestoreFields(t *testing.T) {
	f := newRestoreFixture()
	ctx := context.Background()
	l := f.ledger(t)

	_, err := l.Under("x1").Restore(ctx, "c", 1, "ann")

	require.NoError(t, err)
	history, err := l.History(ctx, "c")
	require.NoError(t, err)
	got := history[0]
	assert.Equal(t, []any{f.retired.Content, f.retired.Run.String(), f.retired.Evidence, "x1", knowledge.Reason(""), 2},
		[]any{got.Content, got.Run.String(), got.Evidence, got.Decision, got.Reason, got.Version})
}

// A write of nodloop through a ledger under a person's decision keeps the decide it names
func TestLedgerUnderKeepsADecisionOfItsOwn(t *testing.T) {
	f := newRestoreFixture()
	ctx := context.Background()
	l := f.ledger(t)

	k, err := l.Under("x1").Drop(ctx, "k1", 1, knowledge.ReasonDuplicate, "d1")

	require.NoError(t, err)
	history, err := l.History(ctx, k.ID)
	require.NoError(t, err)
	assert.Equal(t, "d1", history[0].Decision)
}

// A store that refuses the append fails the write and an export that fails rides on ErrExport after the records
func TestLedgerRestoreAndReverseFail(t *testing.T) {
	f := newRestoreFixture()
	ctx := context.Background()
	now := func() time.Time { return f.at.Add(time.Hour) }
	newID := func(prefix string) string { return prefix + "new" }
	// The records seeded with the write x1 in a ledger whose veto file lives under home
	seeded := func(t *testing.T, home string) (*knowledge.Ledger, string) {
		t.Helper()
		dir := t.TempDir()
		store, err := file.New(dir)
		require.NoError(t, err)
		seeder := knowledge.NewLedger(store, vetofile.NewApprovedFile(t.TempDir(), "records"), now, newID)
		require.NoError(t, testkit.Err(seeder.Import(ctx, []knowledge.Knowledge{f.approved, f.update, f.candidate, f.retired})))
		require.NoError(t, testkit.Err(seeder.Under("x1").Approve(ctx, "k1", 1, "ann")))
		return knowledge.NewLedger(store, vetofile.NewApprovedFile(home, "records"), now, newID), dir
	}
	readOnly := func(t *testing.T) *knowledge.Ledger {
		t.Helper()
		l, dir := seeded(t, t.TempDir())
		require.NoError(t, os.Chmod(filepath.Join(dir, "knowledge.jsonl"), 0o400))
		return l
	}
	// A file where the veto directory belongs so every export fails
	blocked := func(t *testing.T) *knowledge.Ledger {
		t.Helper()
		home := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(home, ".claude"), nil, 0o600))
		l, _ := seeded(t, home)
		return l
	}
	restore := func(l *knowledge.Ledger) error { return testkit.Err(l.Restore(ctx, "c", 1, "ann")) }
	reverse := func(l *knowledge.Ledger) error { return testkit.Err(l.Reverse(ctx, "x1", "ann")) }
	type args struct {
		ledger func(t *testing.T) *knowledge.Ledger
		call   func(l *knowledge.Ledger) error
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a restore the store refuses", args{readOnly, restore}, file.ErrAppend},
		{"a reverse the store refuses", args{readOnly, reverse}, file.ErrAppend},
		{"a restore whose export fails", args{blocked, restore}, knowledge.ErrExport},
		{"a reverse whose export fails", args{blocked, reverse}, knowledge.ErrExport},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.args.call(tc.args.ledger(t))

			assert.ErrorIs(t, err, tc.want)
		})
	}
}
