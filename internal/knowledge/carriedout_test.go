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

// One version of the fixture of the writes of nodloop in the place repo=nodloop unless run names another
func carried(id string, version, base int, status knowledge.Status, approver string, at time.Time) knowledge.Knowledge {
	k := knowledge.Knowledge{
		ID: id, Version: version, Base: base, Kind: knowledge.KindMeaning, Content: id + " lesson", Run: repoRun(),
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}, Basis: knowledge.BasisStated, Status: status,
		Approver: approver, Author: "claude", Time: at,
	}
	if status == knowledge.StatusApproved {
		k.ApprovedAt = at
	}
	return k
}

// The records of the place a person and nodloop approved in with the candidates nodloop may and may not carry out
// 1. p-person is a person's with a next version built on it and q-legacy a person's with a next version of no base
// 2. n-nodloop is nodloop's with a next version and r-reaffirmed nodloop's that a person reaffirmed
// 3. c-add waits in the place, c-acme in a place no person approved and c-veto carries a veto
func carriedSeeds(at time.Time) []knowledge.Knowledge {
	approved, candidate := knowledge.StatusApproved, knowledge.StatusCandidate
	acme := carried("c-acme", 1, 0, candidate, "", at)
	acme.Run = &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}}
	vetoed := carried("c-veto", 1, 0, candidate, "", at)
	vetoed.Kind = knowledge.KindJudgment
	vetoed.Veto = &knowledge.Veto{
		Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`}}, Example: map[string]any{"command": "sed -i s/a/b/ f"},
	}
	reaffirmed := carried("r-reaffirmed", 1, 0, approved, "ann", at.Add(time.Hour))
	reaffirmed.ApprovedAt, reaffirmed.ReviewedAt = at, at.Add(time.Hour)
	compacted := carried("c-compacted", 1, 0, candidate, "", at)
	compacted.Compaction, compacted.CompactionSize = "c-1", 1
	return []knowledge.Knowledge{
		compacted,
		carried("p-person", 1, 0, approved, "ann", at), carried("p-person", 2, 1, candidate, "", at),
		carried("q-legacy", 1, 0, approved, "ann", at), carried("q-legacy", 2, 0, candidate, "", at),
		carried("n-nodloop", 1, 0, approved, knowledge.ApproverNodloop, at), carried("n-nodloop", 2, 1, candidate, "", at),
		carried("r-reaffirmed", 1, 0, approved, knowledge.ApproverNodloop, at), reaffirmed,
		carried("c-add", 1, 0, candidate, "", at), acme, vetoed,
	}
}

// A ledger over the seeds and the count of its records before the write
func seededLedger(t *testing.T, ctx context.Context, seeds []knowledge.Knowledge, at time.Time) (*knowledge.Ledger, int) {
	t.Helper()
	l, _ := newTestLedger(t, t.TempDir(), at)
	require.NoError(t, testkit.Err(l.Import(ctx, seeds)))
	all, err := l.All(ctx)
	require.NoError(t, err)
	return l, len(all)
}

// The records the write added as "id vN status approver reason decision" in sorted order
func written(t *testing.T, ctx context.Context, l *knowledge.Ledger, before int) []string {
	t.Helper()
	all, err := l.All(ctx)
	require.NoError(t, err)
	out := []string{}
	for _, k := range all[:len(all)-before] {
		out = append(out, fmt.Sprintf("%s v%d %s %s %s %s", k.ID, k.Version, k.Status, k.Approver, k.Reason, k.Decision))
	}
	slices.Sort(out)
	return out
}

// nodloop approves only what the person's approvals already reach and names the decide on every record
func TestLedgerApply(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	type args struct {
		id       string
		version  int
		decision string
	}
	type want struct {
		written []string
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a new lesson in a place a person approved", args{"c-add", 1, "d1"}, want{[]string{"c-add v1 approved nodloop  d1"}, nil}},
		{
			"the next version of a lesson nodloop approved supersedes it",
			args{"n-nodloop", 2, "d1"},
			want{[]string{"n-nodloop v1 superseded nodloop  d1", "n-nodloop v2 approved nodloop  d1"}, nil},
		},
		{"a candidate with a veto", args{"c-veto", 1, "d1"}, want{[]string{}, knowledge.ErrPersonDecides}},
		{"a version built on one a person approved", args{"p-person", 2, "d1"}, want{[]string{}, knowledge.ErrPersonDecides}},
		{"a version that would replace one a person approved", args{"q-legacy", 2, "d1"}, want{[]string{}, knowledge.ErrPersonDecides}},
		{"a scope no lesson a person approved reaches", args{"c-acme", 1, "d1"}, want{[]string{}, knowledge.ErrPersonDecides}},
		{"no decide behind it", args{"c-add", 1, ""}, want{[]string{}, knowledge.ErrDecisionRequired}},
		{"an unknown id", args{"k-unknown", 1, "d1"}, want{[]string{}, knowledge.ErrNotFound}},
		{"a candidate of a compaction outside its coverage check", args{"c-compacted", 1, "d1"}, want{[]string{}, knowledge.ErrCompactionInvalid}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, before := seededLedger(t, ctx, carriedSeeds(at), at.Add(time.Hour))

			_, err := l.Apply(ctx, tc.args.id, tc.args.version, tc.args.decision)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.written, written(t, ctx, l, before))
		})
	}
}

// nodloop retires only what it approved itself and under a reason of its own
func TestLedgerWithdraw(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	type args struct {
		id       string
		reason   knowledge.Reason
		decision string
	}
	type want struct {
		written []string
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a lesson nodloop approved", args{"n-nodloop", knowledge.ReasonConflict, "d2"},
			want{[]string{"n-nodloop v1 retired nodloop conflict d2"}, nil},
		},
		{"a lesson a person approved", args{"p-person", knowledge.ReasonConflict, "d2"}, want{[]string{}, knowledge.ErrPersonDecides}},
		{"a lesson a person reaffirmed", args{"r-reaffirmed", knowledge.ReasonWords, "d2"}, want{[]string{}, knowledge.ErrPersonDecides}},
		{"a candidate", args{"c-add", knowledge.ReasonConflict, "d2"}, want{[]string{}, knowledge.ErrVersionUnapproved}},
		{"the reason of a person", args{"n-nodloop", knowledge.ReasonPerson, "d2"}, want{[]string{}, knowledge.ErrReasonUnknown}},
		{"no reason", args{"n-nodloop", "", "d2"}, want{[]string{}, knowledge.ErrReasonUnknown}},
		{"no decide behind it", args{"n-nodloop", knowledge.ReasonConflict, ""}, want{[]string{}, knowledge.ErrDecisionRequired}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, before := seededLedger(t, ctx, carriedSeeds(at), at.Add(time.Hour))

			_, err := l.Withdraw(ctx, tc.args.id, 1, tc.args.reason, tc.args.decision)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.written, written(t, ctx, l, before))
		})
	}
}

// nodloop retires the newest candidate of an id under the reason of its rule
func TestLedgerDrop(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	type args struct {
		id       string
		version  int
		reason   knowledge.Reason
		decision string
	}
	type want struct {
		written []string
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a waiting candidate", args{"c-add", 1, knowledge.ReasonDuplicate, "d3"}, want{[]string{"c-add v1 retired nodloop duplicate d3"}, nil}},
		{
			"a candidate beside a version a person approved", args{"p-person", 2, knowledge.ReasonTempScope, "d3"},
			want{[]string{"p-person v2 retired nodloop temp_scope d3"}, nil},
		},
		{"an approved version", args{"p-person", 1, knowledge.ReasonDuplicate, "d3"}, want{[]string{}, knowledge.ErrNotCandidate}},
		{"the reason of a compaction", args{"c-add", 1, knowledge.ReasonCompaction, "d3"}, want{[]string{}, knowledge.ErrReasonUnknown}},
		{"no decide behind it", args{"c-add", 1, knowledge.ReasonNoEvidence, ""}, want{[]string{}, knowledge.ErrDecisionRequired}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, before := seededLedger(t, ctx, carriedSeeds(at), at.Add(time.Hour))

			_, err := l.Drop(ctx, tc.args.id, tc.args.version, tc.args.reason, tc.args.decision)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.written, written(t, ctx, l, before))
		})
	}
}

// nodloop approves a compaction only of the lessons it approved itself and never one that drafts a veto
func TestLedgerApplyCompaction(t *testing.T) {
	seeds := newCompactionSeeds()
	type args struct {
		// The approver of a and b
		approver string
		// j joins with its veto and the draft that keeps it
		veto     bool
		decision string
		// The coverage names another compaction
		stale bool
		// The call names a compaction nobody proposed
		unknown bool
	}
	type want struct {
		written []string
		err     error
	}
	merged := []string{
		"a v1 retired nodloop compaction d4", "b v1 retired nodloop compaction d4", "k-1 v1 approved nodloop  d4",
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a compaction of lessons nodloop approved", args{approver: knowledge.ApproverNodloop, decision: "d4"}, want{merged, nil}},
		{"a compaction that merges a lesson a person approved", args{approver: "ann", decision: "d4"}, want{[]string{}, knowledge.ErrPersonDecides}},
		{"a compaction that drafts a veto", args{approver: knowledge.ApproverNodloop, veto: true, decision: "d4"}, want{[]string{}, knowledge.ErrPersonDecides}},
		{"no decide behind it", args{approver: knowledge.ApproverNodloop}, want{[]string{}, knowledge.ErrDecisionRequired}},
		{"a coverage of another compaction", args{approver: knowledge.ApproverNodloop, decision: "d4", stale: true}, want{[]string{}, knowledge.ErrCoverageNotPassed}},
		{"a compaction nobody proposed", args{approver: knowledge.ApproverNodloop, decision: "d4", unknown: true}, want{[]string{}, knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, b, j := seeds.a, seeds.b, seeds.j
			a.Approver, b.Approver, j.Approver = tc.args.approver, tc.args.approver, knowledge.ApproverNodloop
			// Only a compaction anchored at j covers it since a veto acts through the guard and joins no folder
			old, drafts, anchor := []knowledge.Knowledge{a, b, seeds.far}, seeds.drafts()[:1], "a"
			if tc.args.veto {
				old, drafts, anchor = append(old, j), seeds.drafts(), "j"
			}
			l, _ := newTestLedger(t, t.TempDir(), seeds.at.Add(time.Hour))
			require.NoError(t, testkit.Err(l.Import(ctx, old)))
			proposed, err := l.ProposeCompaction(ctx, anchor, drafts)
			require.NoError(t, err)
			all, err := l.All(ctx)
			require.NoError(t, err)
			check, id := coverage(proposed), proposed.ID
			if tc.args.stale {
				check.Compaction = "c-other"
			}
			if tc.args.unknown {
				id = "c-unknown"
			}

			_, err = l.ApplyCompaction(ctx, id, tc.args.decision, check)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.written, written(t, ctx, l, len(all)))
		})
	}
}

// A write of nodloop that lands and fails to export says so with ErrExport since the status changed
func TestLedgerCarriedOutExportFails(t *testing.T) {
	seeds := newCompactionSeeds()
	a, b := seeds.a, seeds.b
	a.Approver, b.Approver = knowledge.ApproverNodloop, knowledge.ApproverNodloop
	tcs := []struct {
		name string
		args func(ctx context.Context, l *knowledge.Ledger, compaction string) error
		want []string
	}{
		{"apply", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.Apply(ctx, "c-add", 1, "d5"))
		}, []string{"c-add v1 approved nodloop  d5"}},
		{"withdraw", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.Withdraw(ctx, "a", 1, knowledge.ReasonWords, "d5"))
		}, []string{"a v1 retired nodloop words d5"}},
		{"apply a compaction", func(ctx context.Context, l *knowledge.Ledger, compaction string) error {
			c, err := l.Compaction(ctx, compaction)
			if err != nil {
				return err
			}
			return testkit.Err(l.ApplyCompaction(ctx, compaction, "d5", coverage(c)))
		}, []string{"a v1 retired nodloop compaction d5", "b v1 retired nodloop compaction d5", "k-1 v1 approved nodloop  d5"}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			seeder, _ := newTestLedger(t, dir, seeds.at.Add(time.Hour))
			// The new lesson waits in the repo of far, which a person approved apart from the folder of a and b
			add := carried("c-add", 1, 0, knowledge.StatusCandidate, "", seeds.at)
			add.Run = seeds.far.Run
			require.NoError(t, testkit.Err(seeder.Import(ctx, []knowledge.Knowledge{a, b, seeds.far, add})))
			proposed, err := seeder.ProposeCompaction(ctx, "a", seeds.drafts()[:1])
			require.NoError(t, err)
			all, err := seeder.All(ctx)
			require.NoError(t, err)
			// A file where the veto directory belongs so every export fails
			blockedHome := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(blockedHome, ".claude"), nil, 0o600))
			store, err := file.New(dir)
			require.NoError(t, err)
			blocked := knowledge.NewLedger(store, vetofile.NewApprovedFile(blockedHome, "records"), func() time.Time { return seeds.at.Add(time.Hour) },
				func(prefix string) string { return prefix + "new" })

			err = tc.args(ctx, blocked, proposed.ID)

			assert.ErrorIs(t, err, knowledge.ErrExport)
			assert.Equal(t, tc.want, written(t, ctx, blocked, len(all)))
		})
	}
}

// The items that each reach every run of a scope, a record that names a decide and when a version was proposed
func TestSetCarriedOutReads(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	wide := carried("w-wide", 1, 0, knowledge.StatusApproved, "ann", at)
	wide.Run = &knowledge.RunScope{Producer: "session"}
	corroborated := carried("c-add", 1, 0, knowledge.StatusCandidate, "", at.Add(time.Hour))
	dropped := carried("c-old", 1, 0, knowledge.StatusRetired, knowledge.ApproverNodloop, at)
	dropped.Decision = "d1"
	all := knowledge.Set{corroborated, carried("c-add", 1, 0, knowledge.StatusCandidate, "", at), wide, dropped}
	type want struct {
		reaching            []knowledge.Ref
		named, unnamed, nul bool
		proposed, missing   time.Time
	}
	tcs := []struct {
		name string
		args knowledge.RunScope
		want want
	}{
		{
			"a scope of one repo is reached by every item of its runs", *repoRun(),
			want{[]knowledge.Ref{{ID: "c-add", Version: 1}, {ID: "c-add", Version: 1}, {ID: "w-wide", Version: 1}, {ID: "c-old", Version: 1}}, true, false, false, at, time.Time{}},
		},
		{
			"every run of the producer is reached by the item of every run alone", knowledge.RunScope{Producer: "session"},
			want{[]knowledge.Ref{{ID: "w-wide", Version: 1}}, true, false, false, at, time.Time{}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := want{
				reaching: all.Reaching(tc.args).Refs(), named: all.HasDecision("d1"), unnamed: all.HasDecision("d2"), nul: all.HasDecision(""),
				proposed: all.ProposedAt(knowledge.Ref{ID: "c-add", Version: 1}), missing: all.ProposedAt(knowledge.Ref{ID: "c-none", Version: 1}),
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
