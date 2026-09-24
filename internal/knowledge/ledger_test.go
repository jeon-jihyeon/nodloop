package knowledge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
)

func TestLedgerApproved(t *testing.T) {
	at := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "jed", at
	other := candidate
	other.ID = "k2"
	type args struct {
		id      string
		version int
	}
	type want struct {
		knowledge knowledge.Knowledge
		err       error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"approved version is returned", args{"k1", 1}, want{knowledge: approved}},
		{"candidate is not approved", args{"k2", 1}, want{err: knowledge.ErrVersionUnapproved}},
		{"unknown version is not found", args{"k1", 2}, want{err: knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	l := knowledge.NewLedger(store, func() time.Time { return at }, func() string { return "k-new" })
	require.NoError(t, l.Import(ctx, []knowledge.Knowledge{candidate, approved, other}))
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := l.Approved(ctx, tc.args.id, tc.args.version)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.knowledge, got)
		})
	}
}

func TestLedgerHistory(t *testing.T) {
	at := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "jed", at
	other := candidate
	other.ID = "k2"
	type want struct {
		history knowledge.Set
		err     error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"records of the id come newest first", "k1", want{history: knowledge.Set{approved, candidate}}},
		{"id without a record is not found", "k3", want{err: knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	l := knowledge.NewLedger(store, func() time.Time { return at }, func() string { return "k-new" })
	require.NoError(t, l.Import(ctx, []knowledge.Knowledge{candidate, other, approved}))
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := l.History(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.history, got)
		})
	}
}

func TestLedgerOverlaps(t *testing.T) {
	at := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	k1 := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	k2 := k1
	k2.ID = "k2"
	retired := k1
	retired.ID, retired.Status, retired.Approver = "k3", knowledge.StatusRetired, "jed"
	type want struct {
		overlaps knowledge.Set
		err      error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"current items of the same kind and scope are listed", "k1", want{overlaps: knowledge.Set{k2}}},
		{"id without a current record is not found", "k3", want{err: knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	l := knowledge.NewLedger(store, func() time.Time { return at }, func() string { return "k-new" })
	require.NoError(t, l.Import(ctx, []knowledge.Knowledge{k1, k2, retired}))
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := l.Overlaps(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.overlaps, got)
		})
	}
}

func TestLedgerPropose(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	draft := knowledge.Knowledge{
		ID: "k1", Kind: knowledge.KindMeaning, Content: "clicks and conversions use different time bases",
		Scope:    knowledge.Scope{Metrics: []string{"conversion_count"}},
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Author: "author",
	}
	proposed := draft
	proposed.Version, proposed.Status, proposed.Basis = 1, knowledge.StatusCandidate, knowledge.BasisStated
	proposed.Time = now
	filled := draft
	filled.Version, filled.Status = 7, knowledge.StatusApproved
	signed := draft
	signed.Approver, signed.ApprovedAt, signed.Supersedes = "jed", at, 2
	verified := draft
	verified.Basis = knowledge.BasisVerified
	proposedVerified := proposed
	proposedVerified.Basis = knowledge.BasisVerified
	unnamed := draft
	unnamed.ID = ""
	generated := proposed
	generated.ID = "k-new"
	v1 := proposed
	v1.Time = at
	v3 := v1
	v3.Version = 3
	proposedV4 := proposed
	proposedV4.Version = 4
	neighbour := v1
	neighbour.ID = "k2"
	empty := draft
	empty.Content = ""
	none, just := knowledge.Set{}, knowledge.Set{proposed}
	type args struct {
		seeds []knowledge.Knowledge
		draft knowledge.Knowledge
	}
	type want struct {
		knowledge knowledge.Knowledge
		overlaps  knowledge.Set
		all       knowledge.Set
		err       error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"new id becomes the first stated candidate", args{draft: draft}, want{proposed, none, just, nil}},
		{
			"draft without an id takes a generated one",
			args{draft: unnamed},
			want{generated, none, knowledge.Set{generated}, nil},
		},
		{"version and status of the draft are replaced", args{draft: filled}, want{proposed, none, just, nil}},
		{"approval fields of the draft are dropped", args{draft: signed}, want{proposed, none, just, nil}},
		{
			"verified basis is kept",
			args{draft: verified},
			want{proposedVerified, none, knowledge.Set{proposedVerified}, nil},
		},
		{
			"next version follows the highest version of the id",
			args{[]knowledge.Knowledge{v1, v3}, draft},
			want{proposedV4, none, knowledge.Set{proposedV4, v3, v1}, nil},
		},
		{
			"current item with an intersecting scope is listed",
			args{[]knowledge.Knowledge{neighbour}, draft},
			want{proposed, knowledge.Set{neighbour}, knowledge.Set{proposed, neighbour}, nil},
		},
		{"invalid draft is not appended", args{draft: empty}, want{err: knowledge.ErrContentRequired}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(store, func() time.Time { return now }, func() string { return "k-new" })
			require.NoError(t, l.Import(ctx, tc.args.seeds))
			got, overlaps, err := l.Propose(ctx, tc.args.draft)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.knowledge, got)
			assert.Equal(t, tc.want.overlaps, overlaps)
			all, err := l.All(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.want.all, all)
		})
	}
}

func TestLedgerApprove(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "ann", at
	retired := candidate
	retired.Status, retired.Approver = knowledge.StatusRetired, "ann"
	superseded := approved
	superseded.Status = knowledge.StatusSuperseded
	second := candidate
	second.Version = 2
	approvedNow := candidate
	approvedNow.Status, approvedNow.Approver, approvedNow.ApprovedAt = knowledge.StatusApproved, "jed", now
	approvedNow.Time = now
	secondApproved := second
	secondApproved.Status, secondApproved.Approver, secondApproved.ApprovedAt = knowledge.StatusApproved, "jed", now
	secondApproved.Time, secondApproved.Supersedes = now, 1
	supersededNow := approved
	supersededNow.Status, supersededNow.Approver, supersededNow.Time = knowledge.StatusSuperseded, "jed", now
	secondApprovedEarlier := second
	secondApprovedEarlier.Status, secondApprovedEarlier.Approver = knowledge.StatusApproved, "ann"
	secondApprovedEarlier.ApprovedAt = at
	type args struct {
		seeds    []knowledge.Knowledge
		version  int
		approver string
	}
	type want struct {
		knowledge knowledge.Knowledge
		history   knowledge.Set
		err       error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"candidate is approved under the approver's name",
			args{[]knowledge.Knowledge{candidate}, 1, "jed"},
			want{approvedNow, knowledge.Set{approvedNow, candidate}, nil},
		},
		{
			"approving a second version appends it before superseding the first",
			args{[]knowledge.Knowledge{candidate, approved, second}, 2, "jed"},
			want{secondApproved, knowledge.Set{supersededNow, secondApproved, second, approved, candidate}, nil},
		},
		{
			"older candidate cannot replace a newer approved version",
			args{[]knowledge.Knowledge{candidate, second, secondApprovedEarlier}, 1, "jed"},
			want{history: knowledge.Set{secondApprovedEarlier, second, candidate}, err: knowledge.ErrTransitionInvalid},
		},
		{
			"missing approver fails",
			args{[]knowledge.Knowledge{candidate}, 1, ""},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrApproverRequired},
		},
		{
			"unknown version is not found",
			args{[]knowledge.Knowledge{candidate}, 9, "jed"},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrNotFound},
		},
		{
			"unknown version without an approver is not found",
			args{[]knowledge.Knowledge{candidate}, 9, ""},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrNotFound},
		},
		{
			"approved version cannot be approved again",
			args{[]knowledge.Knowledge{candidate, approved}, 1, "jed"},
			want{history: knowledge.Set{approved, candidate}, err: knowledge.ErrTransitionInvalid},
		},
		{
			"retired version cannot be approved",
			args{[]knowledge.Knowledge{candidate, retired}, 1, "jed"},
			want{history: knowledge.Set{retired, candidate}, err: knowledge.ErrTransitionInvalid},
		},
		{
			"superseded version cannot be approved",
			args{[]knowledge.Knowledge{candidate, approved, superseded}, 1, "jed"},
			want{history: knowledge.Set{superseded, approved, candidate}, err: knowledge.ErrTransitionInvalid},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(store, func() time.Time { return now }, func() string { return "k-new" })
			require.NoError(t, l.Import(ctx, tc.args.seeds))
			got, err := l.Approve(ctx, "k1", tc.args.version, tc.args.approver)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.knowledge, got)
			history, err := l.History(ctx, "k1")
			require.NoError(t, err)
			assert.Equal(t, tc.want.history, history)
		})
	}
}

func TestLedgerRetire(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "ann", at
	retired := candidate
	retired.Status, retired.Approver = knowledge.StatusRetired, "ann"
	superseded := approved
	superseded.Status = knowledge.StatusSuperseded
	candidateRetired := candidate
	candidateRetired.Status, candidateRetired.Approver, candidateRetired.Time = knowledge.StatusRetired, "jed", now
	approvedRetired := approved
	approvedRetired.Status, approvedRetired.Approver, approvedRetired.Time = knowledge.StatusRetired, "jed", now
	type args struct {
		seeds    []knowledge.Knowledge
		version  int
		approver string
	}
	type want struct {
		knowledge knowledge.Knowledge
		history   knowledge.Set
		err       error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"candidate is retired under the approver's name",
			args{[]knowledge.Knowledge{candidate}, 1, "jed"},
			want{candidateRetired, knowledge.Set{candidateRetired, candidate}, nil},
		},
		{
			"approved version is retired",
			args{[]knowledge.Knowledge{candidate, approved}, 1, "jed"},
			want{approvedRetired, knowledge.Set{approvedRetired, approved, candidate}, nil},
		},
		{
			"missing approver fails",
			args{[]knowledge.Knowledge{candidate}, 1, ""},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrApproverRequired},
		},
		{
			"unknown version is not found",
			args{[]knowledge.Knowledge{candidate}, 9, "jed"},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrNotFound},
		},
		{
			"unknown version without an approver is not found",
			args{[]knowledge.Knowledge{candidate}, 9, ""},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrNotFound},
		},
		{
			"retired version cannot be retired again",
			args{[]knowledge.Knowledge{candidate, retired}, 1, "jed"},
			want{history: knowledge.Set{retired, candidate}, err: knowledge.ErrTransitionInvalid},
		},
		{
			"superseded version cannot be retired",
			args{[]knowledge.Knowledge{candidate, approved, superseded}, 1, "jed"},
			want{history: knowledge.Set{superseded, approved, candidate}, err: knowledge.ErrTransitionInvalid},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(store, func() time.Time { return now }, func() string { return "k-new" })
			require.NoError(t, l.Import(ctx, tc.args.seeds))
			got, err := l.Retire(ctx, "k1", tc.args.version, tc.args.approver)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.knowledge, got)
			history, err := l.History(ctx, "k1")
			require.NoError(t, err)
			assert.Equal(t, tc.want.history, history)
		})
	}
}

func TestLedgerImport(t *testing.T) {
	at := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "jed", at
	empty := candidate
	empty.Content = ""
	type want struct {
		history knowledge.Set
		err     error
	}
	tcs := []struct {
		name string
		args []knowledge.Knowledge
		want want
	}{
		{
			"valid records are appended as they are",
			[]knowledge.Knowledge{candidate, approved},
			want{knowledge.Set{approved, candidate}, nil},
		},
		{
			"invalid record stops the import after the valid ones and is named by position",
			[]knowledge.Knowledge{candidate, empty, approved},
			want{knowledge.Set{candidate}, fmt.Errorf("record 2: %w", knowledge.ErrContentRequired)},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(store, func() time.Time { return at }, func() string { return "k-new" })
			assert.Equal(t, tc.want.err, l.Import(ctx, tc.args))
			history, err := l.History(ctx, "k1")
			require.NoError(t, err)
			assert.Equal(t, tc.want.history, history)
		})
	}
}

func TestLedgerFailsOnBrokenStore(t *testing.T) {
	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) }
	newID := func() string { return "k-new" }
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author",
	}
	unreadableDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(unreadableDir, "knowledge.jsonl"), 0o700))
	unreadableStore, err := file.New(unreadableDir)
	require.NoError(t, err)
	unreadable := knowledge.NewLedger(unreadableStore, now, newID)
	readOnlyDir := t.TempDir()
	readOnlyStore, err := file.New(readOnlyDir)
	require.NoError(t, err)
	readOnly := knowledge.NewLedger(readOnlyStore, now, newID)
	require.NoError(t, readOnly.Import(ctx, []knowledge.Knowledge{candidate}))
	require.NoError(t, os.Chmod(filepath.Join(readOnlyDir, "knowledge.jsonl"), 0o400))
	type args struct {
		ledger *knowledge.Ledger
		call   func(context.Context, *knowledge.Ledger) (any, error)
	}
	type want struct {
		value any
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"all fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.All(ctx) }},
			want{knowledge.Set(nil), file.ErrRead},
		},
		{
			"history fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.History(ctx, "k1")
			}},
			want{knowledge.Set(nil), file.ErrRead},
		},
		{
			"overlaps fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Overlaps(ctx, "k1")
			}},
			want{knowledge.Set(nil), file.ErrRead},
		},
		{
			"approved fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Approved(ctx, "k1", 1)
			}},
			want{knowledge.Knowledge{}, file.ErrRead},
		},
		{
			"propose fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				k, overlaps, err := l.Propose(ctx, candidate)
				return []any{k, overlaps}, err
			}},
			want{[]any{knowledge.Knowledge{}, knowledge.Set(nil)}, file.ErrRead},
		},
		{
			"approve fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Approve(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrRead},
		},
		{
			"retire fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Retire(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrRead},
		},
		{
			"import fails to append",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return nil, l.Import(ctx, []knowledge.Knowledge{candidate})
			}},
			want{nil, file.ErrAppend},
		},
		{
			"propose fails to append",
			args{readOnly, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				k, overlaps, err := l.Propose(ctx, candidate)
				return []any{k, overlaps}, err
			}},
			want{[]any{knowledge.Knowledge{}, knowledge.Set(nil)}, file.ErrAppend},
		},
		{
			"approve fails to append",
			args{readOnly, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Approve(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrAppend},
		},
		{
			"retire fails to append",
			args{readOnly, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Retire(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrAppend},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.args.call(ctx, tc.args.ledger)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.value, got)
		})
	}
}
