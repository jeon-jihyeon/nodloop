package knowledge_test

import (
	"cmp"
	"context"
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
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestLedgerApplyDelegated(t *testing.T) {
	scope := knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}}
	narrow := knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}, "task": {"review"}}}
	other := knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"o/svc"}}}
	except := knowledge.RunScope{Producer: "session", Except: trace.Labels{"task": {"publish"}}}
	global := knowledge.RunScope{Producer: "session"}
	f := newRestoreFixture()
	prior := carried("k-new", 1, 0, knowledge.StatusApproved, "ann", f.at)
	prior.Run = &scope
	type args struct {
		run        *knowledge.RunScope
		scope      knowledge.RunScope
		version    int
		base       int
		decision   string
		target     string
		veto       *knowledge.Veto
		prior      []knowledge.Knowledge
		compaction string
		content    string
	}
	type want struct {
		err      error
		status   knowledge.Status
		approver string
		decision string
		added    int
	}
	ok := want{status: knowledge.StatusApproved, approver: knowledge.ApproverNodloop, decision: "d1", added: 1}
	person := want{err: knowledge.ErrPersonDecides, status: knowledge.StatusCandidate}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a new candidate within the delegation", args{run: &scope, scope: scope, decision: "d1"}, ok},
		{"a narrower candidate", args{run: &narrow, scope: scope, decision: "d1"}, ok},
		{"an explicit global delegation", args{run: &scope, scope: global, decision: "d1"}, ok},
		{"another repository", args{run: &scope, scope: other, decision: "d1"}, person},
		{"a dropped required label", args{run: &global, scope: scope, decision: "d1"}, person},
		{"a lifted exception", args{run: &scope, scope: except, decision: "d1"}, person},
		{"an update", args{run: &scope, scope: scope, version: 2, base: 1, decision: "d1"}, person},
		{"an earlier human decision", args{run: &scope, scope: scope, decision: "d1", prior: []knowledge.Knowledge{prior}}, person},
		{"a legacy candidate without scope", args{scope: scope, decision: "d1"}, person},
		{"a veto", args{run: &scope, scope: scope, decision: "d1", veto: &knowledge.Veto{Tool: "Bash"}}, person},
		{
			"an invalid delegation", args{run: &scope, decision: "d1"},
			want{err: knowledge.ErrScopeInvalid, status: knowledge.StatusCandidate},
		},
		{
			"no decide", args{run: &scope, scope: scope},
			want{err: knowledge.ErrDecisionRequired, status: knowledge.StatusCandidate},
		},
		{
			"an unknown candidate", args{run: &scope, scope: scope, decision: "d1", target: "missing"},
			want{err: knowledge.ErrNotFound, status: knowledge.StatusCandidate},
		},
		{
			"a compaction candidate", args{run: &scope, scope: scope, decision: "d1", compaction: "c1"},
			want{err: knowledge.ErrCompactionInvalid, status: knowledge.StatusCandidate},
		},
		{
			"the ordinary run budget", args{run: &scope, scope: scope, decision: "d1", content: strings.Repeat("a", knowledge.RunChars)},
			want{err: knowledge.ErrFolderFull, status: knowledge.StatusCandidate},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			version := cmp.Or(tc.args.version, 1)
			k := carried("k-new", version, tc.args.base, knowledge.StatusCandidate, "", f.at)
			k.Run, k.Veto = tc.args.run, tc.args.veto
			k.Compaction, k.Content = tc.args.compaction, cmp.Or(tc.args.content, "Answer concisely")
			l := f.ledger(t, slices.Concat(tc.args.prior, []knowledge.Knowledge{k})...)
			before, err := l.All(ctx)
			require.NoError(t, err)

			_, err = l.ApplyDelegated(ctx, cmp.Or(tc.args.target, k.ID), version, tc.args.decision, tc.args.scope)

			assert.ErrorIs(t, err, tc.want.err)
			after, err := l.All(ctx)
			require.NoError(t, err)
			current, err := after.Version(k.ID, version)
			require.NoError(t, err)
			assert.Equal(t, tc.want.status, current.Status)
			assert.Equal(t, tc.want.approver, current.Approver)
			assert.Equal(t, tc.want.decision, current.Decision)
			assert.Equal(t, tc.want.added, len(after)-len(before))
		})
	}
}

func TestLedgerApplyDelegatedWriteFailure(t *testing.T) {
	type args struct {
		appendErr error
		exportErr bool
		decision  string
	}
	type want struct {
		err    error
		status knowledge.Status
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a failed append leaves the candidate", args{appendErr: assert.AnError, decision: "d1"},
			want{assert.AnError, knowledge.StatusCandidate}},
		{"a failed export keeps the approval", args{exportErr: true, decision: "d1"},
			want{knowledge.ErrExport, knowledge.StatusApproved}},
		{"a failed repair does not hide the refusal", args{exportErr: true},
			want{knowledge.ErrDecisionRequired, knowledge.StatusCandidate}},
	}
	f := newRestoreFixture()
	ctx := context.Background()
	scope := knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			k := carried("k-new", 1, 0, knowledge.StatusCandidate, "", f.at)
			k.Run = &scope
			require.NoError(t, store.Append(ctx, k.Record()))
			home := t.TempDir()
			if tc.args.exportErr {
				require.NoError(t, os.WriteFile(filepath.Join(home, ".claude"), nil, 0o600))
			}
			flaky := &testkit.FlakyKnowledge{Store: store, Appends: testkit.Reads{Err: tc.args.appendErr}}
			l := knowledge.NewLedger(flaky, vetofile.NewApprovedFile(home, "records"),
				func() time.Time { return f.at }, func(prefix string) string { return prefix + "new" })

			_, err = l.ApplyDelegated(ctx, k.ID, k.Version, tc.args.decision, scope)

			assert.ErrorIs(t, err, tc.want.err)
			all, err := l.All(ctx)
			require.NoError(t, err)
			current, err := all.Version(k.ID, k.Version)
			require.NoError(t, err)
			assert.Equal(t, tc.want.status, current.Status)
			assert.Empty(t, all.PersonApproved())
		})
	}
}
