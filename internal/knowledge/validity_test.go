package knowledge_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestSetForAt(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	one := knowledge.Knowledge{
		ID: "acme", Version: 1, Kind: knowledge.KindJudgment, Content: "Follow the team convention",
		Run:    &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}},
		Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Time: at,
	}
	two := one
	two.Version, two.Supersedes, two.Time, two.ApprovedAt = 2, 1, at.Add(time.Hour), at.Add(time.Hour)
	two.Content = "Follow the revised team convention"
	two.Run = &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}},
		ValidFrom: at.Add(2 * time.Hour), ValidUntil: at.Add(4 * time.Hour)}
	old := one
	old.Status, old.Time = knowledge.StatusSuperseded, two.Time
	retired := two
	retired.Status, retired.Time = knowledge.StatusRetired, at.Add(3*time.Hour)
	type args struct {
		set  knowledge.Set
		hour int
	}
	tcs := []struct {
		name string
		args args
		want []knowledge.Ref
	}{
		{"no future approval", args{knowledge.Set{two, old, one}, -1}, []knowledge.Ref{}},
		{"before a later record was known", args{knowledge.Set{two, old, one}, 0}, []knowledge.Ref{{ID: "acme", Version: 1}}},
		{"scheduled replacement keeps the old version", args{knowledge.Set{two, old, one}, 1},
			[]knowledge.Ref{{ID: "acme", Version: 1}}},
		{"inclusive effective start", args{knowledge.Set{two, old, one}, 2}, []knowledge.Ref{{ID: "acme", Version: 2}}},
		{"exclusive end never revives the old version", args{knowledge.Set{two, old, one}, 4}, []knowledge.Ref{}},
		{"a later retirement does not rewrite history", args{knowledge.Set{retired, two, old, one}, 2},
			[]knowledge.Ref{{ID: "acme", Version: 2}}},
		{"retirement ends the selected version", args{knowledge.Set{retired, two, old, one}, 3}, []knowledge.Ref{}},
		{"a superseded orphan has no pending approved replacement", args{knowledge.Set{old, one}, 2}, []knowledge.Ref{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.args.set.ForAt("session", trace.Labels{"repo": {"acme"}}, at.Add(time.Duration(tc.args.hour)*time.Hour))
			assert.Equal(t, tc.want, got.Refs())
		})
	}
}

func TestLedgerValidity(t *testing.T) {
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	type args struct {
		from, until time.Time
		veto        *knowledge.Veto
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"open start", args{until: at.Add(time.Hour)}, nil},
		{"open end", args{from: at}, nil},
		{"empty interval", args{from: at, until: at}, knowledge.ErrScopeInvalid},
		{"reversed interval", args{from: at.Add(time.Hour), until: at}, knowledge.ErrScopeInvalid},
		{"a guard cannot expire implicitly", args{until: at.Add(time.Hour), veto: &knowledge.Veto{}},
			knowledge.ErrScopeInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			draft := runItem("acme", &knowledge.RunScope{
				Producer: "session", ValidFrom: tc.args.from, ValidUntil: tc.args.until,
			})
			draft.Veto = tc.args.veto
			_, _, err := runLedger(t).Propose(context.Background(), draft)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestValidityContextSequence(t *testing.T) {
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	l := runLedger(t)
	ctx := context.Background()
	base := runItem("acme", &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"phase": {"normal"}}})
	base.Content = "Follow the team convention"
	_, _, err := l.Propose(ctx, base)
	require.NoError(t, err)
	_, err = l.Approve(ctx, "acme", 1, "ann")
	require.NoError(t, err)
	exception := runItem("acme-exception", &knowledge.RunScope{Producer: "session",
		Labels: trace.Labels{"phase": {"incident"}}, ValidFrom: at.Add(time.Hour), ValidUntil: at.Add(2 * time.Hour)})
	exception.Content = "Restore stability with the smallest safe change"
	_, _, err = l.Propose(ctx, exception)
	require.NoError(t, err)
	_, err = l.Approve(ctx, "acme-exception", 1, "ann")
	require.NoError(t, err)
	all, err := l.All(ctx)
	require.NoError(t, err)
	type args struct {
		phase string
		at    time.Time
	}
	tcs := []struct {
		name string
		args args
		want []knowledge.Ref
	}{
		{"ordinary work", args{"normal", at.Add(30 * time.Minute)}, []knowledge.Ref{{ID: "acme", Version: 1}}},
		{"incident exception", args{"incident", at.Add(time.Hour)}, []knowledge.Ref{{ID: "acme-exception", Version: 1}}},
		{"expired incident", args{"incident", at.Add(2 * time.Hour)}, []knowledge.Ref{}},
		{"normal recovery", args{"normal", at.Add(2 * time.Hour)}, []knowledge.Ref{{ID: "acme", Version: 1}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, all.ForAt("session", trace.Labels{"phase": {tc.args.phase}}, tc.args.at).Refs())
		})
	}
	assert.NotContains(t, all.Rules(), "acme-exception")
	assert.Empty(t, all.For("session", trace.Labels{"phase": {"incident"}}))
	assert.Contains(t, all.ForAt("session", trace.Labels{"phase": {"incident"}}, at.Add(time.Hour)).Prompt(), "until")
}
