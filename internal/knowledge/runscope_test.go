package knowledge_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func runLedger(t *testing.T) *knowledge.Ledger {
	t.Helper()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	return knowledge.NewLedger(store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(), func() time.Time {
		now = now.Add(time.Minute)
		return now
	}, func(prefix string) string { return prefix + "new" })
}

func runItem(id string, run *knowledge.RunScope) knowledge.Knowledge {
	return knowledge.Knowledge{
		ID: id, Kind: knowledge.KindJudgment, Content: "use git -C instead of cd", Run: run,
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"run-1"}}, Author: "author",
	}
}

// Only approved run items whose labels the run carries and whose exceptions it does not reach a run
// An item of the data review never reaches a run whatever its scope
func TestSetFor(t *testing.T) {
	approved := func(k knowledge.Knowledge) knowledge.Knowledge {
		k.Version, k.Status, k.Approver = 1, knowledge.StatusApproved, "ann"
		return k
	}
	repo := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	commit := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}, "task": {"commit", "push"}}}
	notDocs := &knowledge.RunScope{Producer: "session", Except: trace.Labels{"task": {"docs"}}}
	other := &knowledge.RunScope{Producer: "ci", Labels: trace.Labels{"repo": {"nodloop"}}}
	candidate := runItem("candidate", repo)
	candidate.Version, candidate.Status = 1, knowledge.StatusCandidate
	data := approved(knowledge.Knowledge{ID: "data", Kind: knowledge.KindMeaning, Content: "lag", Author: "author"})
	set := knowledge.Set{
		approved(runItem("repo", repo)), approved(runItem("commit", commit)), approved(runItem("not-docs", notDocs)),
		approved(runItem("other", other)), candidate, data,
	}
	type args struct {
		producer string
		labels   trace.Labels
	}
	tcs := []struct {
		name string
		args args
		want []string
	}{
		{"a commit in the repo gets every item it carries", args{"session", trace.Labels{"repo": {"nodloop"}, "task": {"commit"}}}, []string{"commit", "not-docs", "repo"}},
		{"docs work is excepted", args{"session", trace.Labels{"repo": {"nodloop"}, "task": {"docs"}}}, []string{"repo"}},
		{"another repo gets only the unscoped item", args{"session", trace.Labels{"repo": {"other"}}}, []string{"not-docs"}},
		{"another producer gets its own items", args{"ci", trace.Labels{"repo": {"nodloop"}}}, []string{"other"}},
		{"an unknown producer gets nothing", args{"bot", trace.Labels{"repo": {"nodloop"}}}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, k := range set.For(tc.args.producer, tc.args.labels) {
				got = append(got, k.ID)
			}
			assert.Equal(t, tc.want, got)
		})
	}
	assert.Empty(t, set.Applicable(evidence.ContextNoKnownChange, knowledge.Moved{}, nil).Matching(knowledge.Filter{Kinds: []knowledge.Kind{knowledge.KindJudgment}}),
		"a run item reaches no data review")
	assert.False(t, knowledge.Set{approved(runItem("repo", repo))}.Covers(evidence.ContextNoKnownChange))
}

func TestLedgerProposeRunScope(t *testing.T) {
	tcs := []struct {
		name string
		args knowledge.Knowledge
		want error
	}{
		{"a run scope is proposed", runItem("k1", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"b", "a", "b"}}}), nil},
		{"a run scope without a producer is refused", runItem("k1", &knowledge.RunScope{Labels: trace.Labels{"repo": {"a"}}}), knowledge.ErrScopeInvalid},
		{"an empty label value is refused", runItem("k1", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {""}}}), knowledge.ErrScopeInvalid},
		{
			"a run scope beside a change context is refused",
			func() knowledge.Knowledge {
				k := runItem("k1", &knowledge.RunScope{Producer: "session"})
				k.Scope.ChangeContexts = []evidence.Context{evidence.ContextNoKnownChange}
				return k
			}(),
			knowledge.ErrScopeMixed,
		},
		{
			"a run scope beside an exception is refused",
			func() knowledge.Knowledge {
				k := runItem("k1", &knowledge.RunScope{Producer: "session"})
				k.Exceptions = []evidence.Context{evidence.ContextNoKnownChange}
				return k
			}(),
			knowledge.ErrScopeMixed,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := runLedger(t)

			got, _, err := l.Propose(context.Background(), tc.args)

			require.ErrorIs(t, err, tc.want)
			if tc.want == nil {
				assert.Equal(t, trace.Labels{"repo": {"a", "b"}}, got.Run.Labels)
			}
		})
	}
}

// Overlaps list run items of one producer and kind that one run could carry together and never an item of the data review
func TestLedgerRunOverlaps(t *testing.T) {
	ctx := context.Background()
	l := runLedger(t)
	for _, k := range []knowledge.Knowledge{
		runItem("nodloop", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}),
		runItem("other-repo", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"other"}}}),
		runItem("any-repo", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"task": {"commit"}}}),
		{ID: "data", Kind: knowledge.KindJudgment, Content: "lag", Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Author: "author"},
	} {
		_, _, err := l.Propose(ctx, k)
		require.NoError(t, err)
	}

	got, err := l.Overlaps(ctx, "nodloop")

	require.NoError(t, err)
	var ids []string
	for _, k := range got {
		ids = append(ids, k.ID)
	}
	assert.Equal(t, []string{"any-repo"}, ids)
}

func TestLedgerApproveRunScope(t *testing.T) {
	repo := trace.Labels{"repo": {"nodloop"}}
	type args struct {
		v1, v2 *knowledge.RunScope
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"the same scope is approved", args{&knowledge.RunScope{Producer: "session", Labels: repo}, &knowledge.RunScope{Producer: "session", Labels: repo}}, nil},
		{
			"a narrower scope is approved",
			args{&knowledge.RunScope{Producer: "session", Labels: repo}, &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}, "task": {"commit"}}}},
			nil,
		},
		{"a dropped label is refused", args{&knowledge.RunScope{Producer: "session", Labels: repo}, &knowledge.RunScope{Producer: "session"}}, knowledge.ErrScopeWidened},
		{
			"an added value is refused",
			args{&knowledge.RunScope{Producer: "session", Labels: repo}, &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop", "other"}}}},
			knowledge.ErrScopeWidened,
		},
		{
			"a lifted exception is refused",
			args{&knowledge.RunScope{Producer: "session", Except: trace.Labels{"task": {"docs"}}}, &knowledge.RunScope{Producer: "session"}},
			knowledge.ErrScopeWidened,
		},
		{"another producer is refused", args{&knowledge.RunScope{Producer: "session", Labels: repo}, &knowledge.RunScope{Producer: "ci", Labels: repo}}, knowledge.ErrScopeWidened},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			l := runLedger(t)
			_, _, err := l.Propose(ctx, runItem("k1", tc.args.v1))
			require.NoError(t, err)
			require.NoError(t, testkit.Err(l.Approve(ctx, "k1", 1, "ann")))
			_, _, err = l.Propose(ctx, runItem("k1", tc.args.v2))
			require.NoError(t, err)

			_, err = l.Approve(ctx, "k1", 2, "jed")

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// A run carries every approved item of its producer whose scope overlaps so the review cap holds them together
func TestLedgerRunFolder(t *testing.T) {
	ctx := context.Background()
	l := runLedger(t)
	big := runItem("big", &knowledge.RunScope{Producer: "session"})
	big.Content = strings.Repeat("x", knowledge.ReviewChars-200)
	_, _, err := l.Propose(ctx, big)
	require.NoError(t, err)
	require.NoError(t, testkit.Err(l.Approve(ctx, "big", 1, "ann")))
	more := runItem("more", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}})
	more.Content = strings.Repeat("y", 300)
	_, _, err = l.Propose(ctx, more)
	require.NoError(t, err)
	apart := runItem("apart", &knowledge.RunScope{Producer: "ci"})
	apart.Content = strings.Repeat("z", 300)
	_, _, err = l.Propose(ctx, apart)
	require.NoError(t, err)

	folder, err := l.Folder(ctx, "more", 1)
	require.NoError(t, err)
	assert.Equal(t, "session", folder.Producer)
	assert.Len(t, folder.Carried, 1)
	_, err = l.Approve(ctx, "more", 1, "ann")
	assert.ErrorIs(t, err, knowledge.ErrFolderFull)
	assert.NoError(t, testkit.Err(l.Approve(ctx, "apart", 1, "ann")), "another producer shares no run")
	_, err = l.Compactable(ctx, "big")
	assert.ErrorIs(t, err, knowledge.ErrCompactionInvalid)
}

func TestRunScopeRecorded(t *testing.T) {
	vocab := trace.Labels{"repo": {"nodloop"}, "task": {"commit"}}
	tcs := []struct {
		name string
		args knowledge.RunScope
		want string
	}{
		{"recorded labels pass", knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}, Except: trace.Labels{"task": {"commit"}}}, ""},
		{
			"each unrecorded label is named",
			knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodlop"}}, Except: trace.Labels{"tsak": {"commit"}}},
			"no run of session carries repo=nodlop, tsak=commit",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.args.Recorded(vocab)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, knowledge.ErrScopeUnobserved)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
