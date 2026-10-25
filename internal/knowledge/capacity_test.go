package knowledge_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func scheduledLedger(t *testing.T, oldChars, newChars int) *knowledge.Ledger {
	t.Helper()
	l, ctx := runLedger(t), context.Background()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	old := runItem("acme", &knowledge.RunScope{Producer: "session"})
	old.Content = strings.Repeat("x", oldChars)
	_, _, err := l.Propose(ctx, old)
	require.NoError(t, err)
	require.NoError(t, testkit.Err(l.Approve(ctx, "acme", 1, "ann")))
	next := runItem("acme", &knowledge.RunScope{Producer: "session",
		ValidFrom: at.Add(time.Hour), ValidUntil: at.Add(2 * time.Hour)})
	next.Content = strings.Repeat("x", newChars)
	_, _, err = l.Propose(ctx, next)
	require.NoError(t, err)
	require.NoError(t, testkit.Err(l.Approve(ctx, "acme", 2, "ann")))
	return l
}

func TestLedgerScheduledCapacity(t *testing.T) {
	type args struct {
		old, next, extra int
		from, until      time.Duration
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"pending predecessor still costs its text", args{old: 8000, next: 30, extra: 3000}, knowledge.ErrFolderFull},
		{"before the transition", args{old: 8000, next: 30, extra: 3000, until: time.Hour}, knowledge.ErrFolderFull},
		{"exact transition", args{old: 8000, next: 30, extra: 3000, from: time.Hour}, nil},
		{"one nanosecond of overlap", args{old: 8000, next: 30, extra: 3000, from: time.Hour - 1}, knowledge.ErrFolderFull},
		{"disjoint versions never add together", args{old: 8000, next: 8000, extra: 500}, nil},
		{"a long future version does not fill an earlier interval",
			args{old: 30, next: 8000, extra: 3000, until: time.Hour}, nil},
		{"a future version still has a budget",
			args{old: 30, next: 8000, extra: 3000, from: time.Hour}, knowledge.ErrFolderFull},
		{"an expired replacement never revives its predecessor",
			args{old: 8000, next: 30, extra: 3000, from: 2 * time.Hour}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, ctx := scheduledLedger(t, tc.args.old, tc.args.next), context.Background()
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			run := &knowledge.RunScope{Producer: "session"}
			if tc.args.from != 0 {
				run.ValidFrom = at.Add(tc.args.from)
			}
			if tc.args.until != 0 {
				run.ValidUntil = at.Add(tc.args.until)
			}
			extra := runItem("acme-extra", run)
			extra.Content = strings.Repeat("y", tc.args.extra)
			_, _, err := l.Propose(ctx, extra)
			require.NoError(t, err)
			all, err := l.All(ctx)
			require.NoError(t, err)
			folder, fits, err := all.Fits("acme-extra", 1)
			require.NoError(t, err)
			preview, err := l.Folder(ctx, "acme-extra", 1)
			require.NoError(t, err)
			_, err = l.Approve(ctx, "acme-extra", 1, "ann")
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, tc.want == nil, fits)
			assert.Equal(t, folder.Chars, preview.Chars)
			all, err = l.All(ctx)
			require.NoError(t, err)
			for _, offset := range []time.Duration{30 * time.Minute, time.Hour - 1, time.Hour, 2 * time.Hour, 3 * time.Hour} {
				var chars int
				for _, k := range all.ForAt("session", nil, at.Add(offset)) {
					chars += utf8.RuneCountInString(k.Text())
				}
				assert.LessOrEqual(t, chars, knowledge.RunChars)
			}
		})
	}
}

func TestLedgerCapacityComparedInTheSameInterval(t *testing.T) {
	type args struct {
		later int
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"an earlier overfull interval cannot hide later growth", args{later: 8500}, knowledge.ErrFolderFull},
		{"an unchanged earlier overflow cannot block a fitting later interval", args{later: 500}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, ctx := runLedger(t), context.Background()
			at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
			var seeds knowledge.Set
			for i, size := range []int{10000, tc.args.later} {
				k := runItem([]string{"acme-old", "acme-later"}[i], &knowledge.RunScope{Producer: "session"})
				k.Version, k.Status, k.Approver, k.ApprovedAt, k.Time = 1, knowledge.StatusApproved, "ann", at, at
				k.Basis, k.Content = knowledge.BasisStated, strings.Repeat("x", size)
				k.Run.ValidFrom, k.Run.ValidUntil = at.Add(time.Duration(i)*time.Hour), at.Add(time.Duration(i+1)*time.Hour)
				seeds = append(seeds, k)
			}
			require.NoError(t, testkit.Err(l.Import(ctx, seeds)))
			extra := runItem("acme-extra", &knowledge.RunScope{Producer: "session", ValidFrom: at.Add(time.Hour)})
			extra.Content = strings.Repeat("y", 1000)
			_, _, err := l.Propose(ctx, extra)
			require.NoError(t, err)
			all, err := l.All(ctx)
			require.NoError(t, err)
			_, fits, err := all.Fits("acme-extra", 1)
			require.NoError(t, err)
			_, err = l.Approve(ctx, "acme-extra", 1, "ann")
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, tc.want == nil, fits)
		})
	}
}
