package knowledge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// The ledger of a person's write fixture
// 1. a v1 approved by ann with the update a v2 waiting
// 2. the candidate k1 v1
// 3. c v1 approved by ann and later retired
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

// The ledger of the fixture with the raw records appended after it as a store holds them
// So a record of an earlier release that Import refuses can be seeded
func (f restoreFixture) ledger(t *testing.T, raw ...knowledge.Knowledge) *knowledge.Ledger {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	l, _ := newTestLedger(t, dir, f.at.Add(time.Hour))
	require.NoError(t, testkit.Err(l.Import(ctx, []knowledge.Knowledge{f.approved, f.update, f.candidate, f.retired})))
	store, err := file.New(dir)
	require.NoError(t, err)
	require.NoError(t, store.AppendDecided(ctx, func([]record.Knowledge) ([]record.Knowledge, error) { return knowledge.Set(raw).Records(), nil }))
	return l
}

// Versions the fixture seeds raw beside its own records
// 1. long is a retired version whose text fills RunChars on its own and big an approved one of the same size
// 2. legacy is a retired version and old a candidate of an earlier release without a run scope
func (f restoreFixture) extras() (long, big, legacy, old knowledge.Knowledge) {
	long = f.retired
	long.ID, long.Content = "long", strings.Repeat("a", knowledge.RunChars)
	big = f.approved
	big.ID, big.Content = "big", strings.Repeat("b", knowledge.RunChars)
	legacy = f.retired
	legacy.ID, legacy.Run = "legacy", nil
	old = f.candidate
	old.ID, old.Run = "old", nil
	return long, big, legacy, old
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

// Every write of a person and its reverse return what runs receive and what waits to where it was
// Both writes stay in the records
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
	_, big, _, old := f.extras()
	type args struct {
		decision, approver string
		// The write of the person under the decision x1
		write func(ctx context.Context, l *knowledge.Ledger) error
		// A write after the one the reverse names
		since func(ctx context.Context, l *knowledge.Ledger) error
	}
	approve := func(ctx context.Context, l *knowledge.Ledger) error {
		return testkit.Err(l.Approve(ctx, "k1", 1, "ann"))
	}
	nothing := func(context.Context, *knowledge.Ledger) error { return nil }
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a decision no record names", args{"x9", "ann", approve, nothing}, knowledge.ErrNotFound},
		{"an empty decision", args{"", "ann", approve, nothing}, knowledge.ErrNotFound},
		{
			"an id another write changed since",
			args{"x1", "ann", approve, func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Retire(ctx, "k1", 1, "ann"))
			}},
			knowledge.ErrReverseStale,
		},
		{"an empty name", args{"x1", "", approve, nothing}, knowledge.ErrApproverRequired},
		{"the reserved name", args{"x1", knowledge.ApproverNodloop, approve, nothing}, knowledge.ErrApproverReserved},
		{
			"an approved version a run has no room for since",
			args{"x1", "ann", func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Retire(ctx, "a", 1, "ann"))
			}, func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Import(ctx, []knowledge.Knowledge{big}))
			}},
			knowledge.ErrFolderFull,
		},
		{
			"a candidate of an earlier release without a run scope",
			args{"x1", "ann", func(ctx context.Context, l *knowledge.Ledger) error {
				return testkit.Err(l.Retire(ctx, "old", 1, "ann"))
			}, nothing},
			knowledge.ErrScopeRequired,
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := f.ledger(t, old)
			require.NoError(t, tc.args.write(ctx, l.Under("x1")))
			require.NoError(t, tc.args.since(ctx, l))
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

// Only the records a reverse of the stamp writes took it back and another write of the same id never does
func TestSetTakenBack(t *testing.T) {
	f := newRestoreFixture()
	type args struct {
		// The write under d1 after the approve of k1 under x1
		then            func(ctx context.Context, l *knowledge.Ledger) error
		stamp, decision string
	}
	reverse := func(ctx context.Context, l *knowledge.Ledger) error { return testkit.Err(l.Reverse(ctx, "x1", "ann")) }
	retire := func(ctx context.Context, l *knowledge.Ledger) error {
		return testkit.Err(l.Retire(ctx, "k1", 1, "ann"))
	}
	more := func(ctx context.Context, l *knowledge.Ledger) error {
		if err := reverse(ctx, l); err != nil {
			return err
		}
		return testkit.Err(l.Retire(ctx, "k1", 2, "ann"))
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"the reverse of the stamp", args{reverse, "x1", "d1"}, true},
		{"a retire of the version the stamp approved", args{retire, "x1", "d1"}, false},
		{"a reverse with another write of the id under the decision", args{more, "x1", "d1"}, false},
		{"a decision that wrote nothing", args{reverse, "x1", "d9"}, false},
		{"an empty decision", args{reverse, "x1", ""}, false},
		{"a stamp no record names", args{reverse, "x9", "d1"}, false},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := f.ledger(t)
			require.NoError(t, testkit.Err(l.Under("x1").Approve(ctx, "k1", 1, "ann")))
			require.NoError(t, tc.args.then(ctx, l.Under("d1")))
			all, err := l.All(ctx)
			require.NoError(t, err)

			got := all.TakenBack(tc.args.stamp, tc.args.decision, "ann")

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLedgerRestore(t *testing.T) {
	f := newRestoreFixture()
	long, _, legacy, _ := f.extras()
	type args struct {
		id       string
		version  int
		approver string
	}
	type want struct {
		// The newest record of the id after the call as version, status and approver
		newest string
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a retired version comes back approved as its next version", args{"c", 1, "ann"}, want{"c v2 approved ann", nil}},
		{"an approved version is refused", args{"a", 1, "ann"}, want{"a v2 candidate ", knowledge.ErrTransitionInvalid}},
		{"an unknown version is refused", args{"c", 9, "ann"}, want{"c v1 retired ann", knowledge.ErrNotFound}},
		{"an empty name is refused", args{"c", 1, ""}, want{"c v1 retired ann", knowledge.ErrApproverRequired}},
		{"the reserved name is refused", args{"c", 1, knowledge.ApproverNodloop}, want{"c v1 retired ann", knowledge.ErrApproverReserved}},
		{"a version a run has no room for is refused", args{"long", 1, "ann"}, want{"long v1 retired ann", knowledge.ErrFolderFull}},
		{
			"a version of an earlier release without a run scope is refused",
			args{"legacy", 1, "ann"},
			want{"legacy v1 retired ann", knowledge.ErrScopeRequired},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := f.ledger(t, long, legacy)

			_, err := l.Under("x1").Restore(ctx, tc.args.id, tc.args.version, tc.args.approver)

			assert.ErrorIs(t, err, tc.want.err)
			history, herr := l.History(ctx, tc.args.id)
			require.NoError(t, herr)
			assert.Equal(t, tc.want.newest, fmt.Sprintf("%s v%d %s %s", history[0].ID, history[0].Version, history[0].Status, history[0].Approver))
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
