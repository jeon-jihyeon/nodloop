package knowledge_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestLedgerCompactionCapacityPerRun(t *testing.T) {
	type args struct {
		old, otherA, otherB, shared, next, nextShared int
		timed                                         bool
	}
	type want struct {
		err  error
		a, b int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"another context cannot supply credit", args{5000, 4000, 7500, 1000, 1000, 1501, false},
			want{knowledge.ErrFolderFull, 10000, 8500}},
		{"growth within the cap", args{5000, 4000, 7500, 1000, 1000, 1500, false}, want{nil, 2500, 9000}},
		{"decrease an imported overfull run", args{5000, 4000, 7500, 1000, 8500, 1000, false}, want{nil, 9500, 8500}},
		{"same labels at a future interval", args{5000, 4000, 7500, 1000, 1000, 1501, true},
			want{knowledge.ErrFolderFull, 10000, 8500}},
	}
	random := rand.New(rand.NewPCG(31, 19))
	for sample := range 128 {
		a := args{old: 500 + random.IntN(6500), otherA: 500 + random.IntN(6500),
			otherB: 500 + random.IntN(9000), shared: 500 + random.IntN(3000),
			next: 500 + random.IntN(10500), nextShared: 500 + random.IntN(4000), timed: sample%2 == 0}
		w := want{a: a.next + a.nextShared, b: a.otherB + a.nextShared}
		if w.a > max(knowledge.RunChars, a.old+a.otherA+a.shared) ||
			w.b > max(knowledge.RunChars, a.otherB+a.shared) {
			w = want{knowledge.ErrFolderFull, a.old + a.otherA + a.shared, a.otherB + a.shared}
		}
		tcs = append(tcs, struct {
			name string
			args args
			want want
		}{fmt.Sprintf("sample-%d", sample), a, w})
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			l, _ := newTestLedger(t, t.TempDir(), at)
			ctx := context.Background()
			seed := func(id string, kind knowledge.Kind, values []string, chars int) knowledge.Knowledge {
				k := runItem(id, &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"context": values}})
				if tc.args.timed {
					k.Run.ValidFrom, k.Run.ValidUntil = at.Add(time.Hour), at.Add(2*time.Hour)
				}
				k.Kind, k.Version, k.Status, k.Approver = kind, 1, knowledge.StatusApproved, "ann"
				k.Basis, k.Author = knowledge.BasisStated, "ann"
				k.Time, k.ApprovedAt, k.Content = at, at, ""
				k.Content = strings.Repeat("x", chars-utf8.RuneCountInString(k.Text()))
				return k
			}
			old := seed("acme-old", knowledge.KindJudgment, []string{"a"}, tc.args.old)
			otherA := seed("acme-other-a", knowledge.KindJudgment, []string{"a"}, tc.args.otherA)
			otherB := seed("acme-other-b", knowledge.KindJudgment, []string{"b"}, tc.args.otherB)
			shared := seed("acme-shared", knowledge.KindMeaning, []string{"a", "b"}, tc.args.shared)
			require.NoError(t, testkit.Err(l.Import(ctx, knowledge.Set{old, otherA, otherB, shared})))
			before, err := l.All(ctx)
			require.NoError(t, err)
			query := at.Add(time.Hour)
			assert.Equal(t, tc.args.old+tc.args.otherA+tc.args.shared,
				textRunes(before.ForAt("session", trace.Labels{"context": {"a"}}, query)))
			assert.Equal(t, tc.args.otherB+tc.args.shared,
				textRunes(before.ForAt("session", trace.Labels{"context": {"b"}}, query)))
			next := seed("acme-next", knowledge.KindJudgment, []string{"a"}, tc.args.next)
			next.Evidence.Knowledge = []knowledge.Ref{{ID: old.ID, Version: 1}, {ID: otherA.ID, Version: 1}}
			nextShared := seed("acme-next-shared", knowledge.KindMeaning, []string{"a", "b"}, tc.args.nextShared)
			nextShared.Evidence.Knowledge = []knowledge.Ref{{ID: shared.ID, Version: 1}}
			proposed, err := l.ProposeCompaction(ctx, old.ID, []knowledge.Knowledge{next, nextShared})
			require.NoError(t, err)
			coverage := knowledge.Coverage{Compaction: proposed.ID, Items: []knowledge.CoverageItem{
				{Old: knowledge.Ref{ID: old.ID, Version: 1}, CoveredBy: []knowledge.Ref{{ID: next.ID, Version: 1}}},
				{Old: knowledge.Ref{ID: otherA.ID, Version: 1}, CoveredBy: []knowledge.Ref{{ID: next.ID, Version: 1}}},
				{Old: knowledge.Ref{ID: shared.ID, Version: 1}, CoveredBy: []knowledge.Ref{{ID: nextShared.ID, Version: 1}}},
			}}
			_, err = l.ApproveCompaction(ctx, proposed.ID, "ann", coverage)
			assert.ErrorIs(t, err, tc.want.err)
			all, err := l.All(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.want.a, textRunes(all.ForAt("session", trace.Labels{"context": {"a"}}, query)))
			assert.Equal(t, tc.want.b, textRunes(all.ForAt("session", trace.Labels{"context": {"b"}}, query)))
		})
	}
}

func TestSetCapacityExceptCredit(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	a := runItem("acme-a", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"context": {"x"}}})
	a.Content, a.Version, a.Status, a.Time, a.ApprovedAt = strings.Repeat("x", 6000), 1, knowledge.StatusApproved, at, at
	b := runItem("acme-b", &knowledge.RunScope{Producer: "session", Except: trace.Labels{"context": {"x"}}})
	b.Content, b.Version, b.Status, b.Time, b.ApprovedAt = strings.Repeat("x", 4000), 1, knowledge.StatusApproved, at, at
	next := b
	next.Run = &knowledge.RunScope{Producer: "session"}
	next.Content, next.Version = strings.Repeat("x", 3500), 2
	next.Status, next.ApprovedAt = knowledge.StatusCandidate, time.Time{}
	folder, fits, err := (knowledge.Set{next, a, b}).Fits(next.ID, next.Version)
	require.NoError(t, err)
	assert.False(t, fits)
	assert.Greater(t, folder.Chars, knowledge.RunChars)
}
