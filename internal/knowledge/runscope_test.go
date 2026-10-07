package knowledge_test

import (
	"context"
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

func runLedger(t *testing.T) *knowledge.Ledger {
	t.Helper()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	return knowledge.NewLedger(store, vetofile.NewApprovedFile(t.TempDir(), "records"), func() time.Time {
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
// A record of the data review without a run scope never reaches a run
// The items of one run in the order a prompt carries them
func TestSetForOrder(t *testing.T) {
	at := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	approved := func(id string, scope *knowledge.RunScope, approvedAt, reviewedAt time.Time) knowledge.Knowledge {
		k := runItem(id, scope)
		k.Version, k.Status, k.Approver, k.ApprovedAt, k.ReviewedAt = 1, knowledge.StatusApproved, "ann", approvedAt, reviewedAt
		return k
	}
	repo := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	dir := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}, "dir": {"cmd"}}}
	tcs := []struct {
		name string
		args knowledge.Set
		want []string
	}{
		{
			"a narrower scope goes first whatever its age",
			knowledge.Set{approved("a-repo", repo, at, time.Time{}), approved("z-dir", dir, at.Add(-time.Hour), time.Time{})},
			[]string{"z-dir", "a-repo"},
		},
		{
			"among equal scopes the newest approval goes first",
			knowledge.Set{approved("a-old", repo, at, time.Time{}), approved("b-new", repo, at.Add(time.Hour), time.Time{})},
			[]string{"b-new", "a-old"},
		},
		{
			"a reaffirm counts as the newest word of a person",
			knowledge.Set{approved("a-reaffirmed", repo, at, at.Add(2*time.Hour)), approved("b-new", repo, at.Add(time.Hour), time.Time{})},
			[]string{"a-reaffirmed", "b-new"},
		},
		{
			"the id breaks a tie",
			knowledge.Set{approved("b", repo, at, time.Time{}), approved("a", repo, at, time.Time{})},
			[]string{"a", "b"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, k := range tc.args.For("session", trace.Labels{"repo": {"nodloop"}, "dir": {"cmd"}}) {
				got = append(got, k.ID)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

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
}

func TestSetWaiting(t *testing.T) {
	repo := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	other := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"other"}}}
	record := func(id string, version int, status knowledge.Status, run *knowledge.RunScope) knowledge.Knowledge {
		k := runItem(id, run)
		k.Version, k.Status = version, status
		if status != knowledge.StatusCandidate {
			k.Approver = "ann"
		}
		return k
	}
	compacted := record("compacted", 1, knowledge.StatusCandidate, repo)
	compacted.Compaction = "c1"
	// Newest first as the store lists them
	set := knowledge.Set{
		record("retired", 1, knowledge.StatusRetired, repo), record("retired", 1, knowledge.StatusCandidate, repo),
		record("next", 2, knowledge.StatusCandidate, repo), record("next", 1, knowledge.StatusApproved, repo),
		record("new", 1, knowledge.StatusCandidate, repo), record("elsewhere", 1, knowledge.StatusCandidate, other), compacted,
	}
	tcs := []struct {
		name   string
		labels trace.Labels
		want   []string
	}{
		{"the repo waits for a new id and the next version of an approved one", trace.Labels{"repo": {"nodloop"}}, []string{"next", "new"}},
		{"another repo waits for its own", trace.Labels{"repo": {"other"}}, []string{"elsewhere"}},
		{"a place no candidate reaches waits for nothing", trace.Labels{"repo": {"x"}}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, k := range set.Waiting("session", tc.labels) {
				got = append(got, k.ID)
			}
			assert.Equal(t, tc.want, got)
		})
	}
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
		{"an item without a run scope is refused", runItem("k1", nil), knowledge.ErrScopeRequired},
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

// Overlaps list run items of one producer and kind that one run could carry together
func TestLedgerRunOverlaps(t *testing.T) {
	ctx := context.Background()
	l := runLedger(t)
	for _, k := range []knowledge.Knowledge{
		runItem("nodloop", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}),
		runItem("other-repo", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"other"}}}),
		runItem("any-repo", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"task": {"commit"}}}),
		runItem("ci", &knowledge.RunScope{Producer: "ci", Labels: trace.Labels{"repo": {"nodloop"}}}),
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
		{
			"an exception the required labels already leave out may be dropped",
			args{&knowledge.RunScope{Producer: "session", Except: trace.Labels{"env": {"prod"}}}, &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"env": {"dev"}}}},
			nil,
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
	big.Content = strings.Repeat("x", knowledge.RunChars-200)
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
	compactable, err := l.Compactable(ctx, "big")
	require.NoError(t, err)
	assert.Len(t, compactable.Items, 1, "the item that waits for approval and the item of another producer stay out")
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

func TestLedgerNarrowExcept(t *testing.T) {
	type args struct {
		scope  *knowledge.RunScope
		key    string
		values []string
	}
	type want struct {
		except trace.Labels
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the refuted dirs become exceptions",
			args{&knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}, "dir", []string{"docs", "tmp"}},
			want{except: trace.Labels{"dir": {"docs", "tmp"}}},
		},
		{
			"an exception already there is kept once",
			args{&knowledge.RunScope{Producer: "session", Except: trace.Labels{"dir": {"docs"}}}, "dir", []string{"docs", "tmp"}},
			want{except: trace.Labels{"dir": {"docs", "tmp"}}},
		},
		{
			"excepting every value the scope allows is refused",
			args{&knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}, "repo", []string{"nodloop"}},
			want{err: knowledge.ErrNarrowExhausted},
		},
		{"no refuted value is refused", args{&knowledge.RunScope{Producer: "session"}, "dir", nil}, want{err: knowledge.ErrNarrowInvalid}},
		{"no refuted key is refused", args{&knowledge.RunScope{Producer: "session"}, "", []string{"docs"}}, want{err: knowledge.ErrNarrowInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			l := runLedger(t)
			_, _, err := l.Propose(ctx, runItem("k1", tc.args.scope))
			require.NoError(t, err)
			require.NoError(t, testkit.Err(l.Approve(ctx, "k1", 1, "ann")))

			got, _, err := l.Narrow(ctx, "k1", 1, tc.args.key, tc.args.values, []string{"refuted-run"}, "author")

			require.ErrorIs(t, err, tc.want.err)
			if tc.want.err != nil {
				return
			}
			assert.Equal(t, 2, got.Version)
			assert.Equal(t, tc.want.except, got.Run.Except)
			assert.Equal(t, tc.args.scope.Labels, got.Run.Labels)
			assert.Contains(t, got.Evidence.OutcomeTraceIDs, "refuted-run")
			assert.NoError(t, testkit.Err(l.Approve(ctx, "k1", 2, "ann")), "a narrowed version never widens")
		})
	}
}
