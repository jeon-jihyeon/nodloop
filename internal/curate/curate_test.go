package curate_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

var at = time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)

// The HMAC of a text as 64 hex
var hmac = strings.Repeat("ab", 32)

// The event of a decide of the id drawn at the audit p under the seed of the cycle
func decided(id string, p float64) curate.Event {
	return curate.Event{
		ID: id, Ref: "run-1", Source: trace.SourcePush,
		Draw: trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "seed", id, "audit", p),
	}
}

func TestEventDecide(t *testing.T) {
	share, over := 1.0, 1.5
	update := curate.Opening{Kind: curate.ChangeUpdate, Bin: curate.Bin1}
	drop := curate.Change{Kind: curate.ChangeDrop, ID: "k-1", Version: 1, Anchor: 3}
	dropped := curate.Decision{
		Tier: 0, RuleCode: curate.RuleSameAsItem, Action: curate.ActionDrop, Reason: knowledge.ReasonDuplicate,
		Failure: curate.FailureHard, Reopen: curate.ReopenPersonOnly,
	}
	add := curate.Change{Kind: curate.ChangeAdd, ID: "k-2", Version: 1, Extract: "e-1", Anchor: 3, Diff: hmac}
	applied := curate.Decision{Tier: 1, Judge: "c-1", Outcome: curate.OutcomeConform, Vote: &share, Bin: curate.Bin1, Action: curate.ActionApply}
	with := func(d curate.Decision, change func(*curate.Decision)) curate.Decision {
		change(&d)
		return d
	}
	failed := errors.New("judge timed out")
	type args struct {
		event    curate.Event
		change   curate.Change
		decision curate.Decision
		failed   error
	}
	type want struct {
		input, output, failure string
		err                    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a tier 0 drop names its reason, failure class and reopen condition",
			args{decided("d1", 0.05), drop, dropped, nil},
			want{
				`{"kind":"drop","id":"k-1","version":1,"anchor":3}`,
				`{"tier":0,"rule_code":"same_as_item","action":"drop","reason":"duplicate","failure":"hard","reopen":"person_only"}`, "", nil,
			},
		},
		{
			"a tier 1 add carries the judge, the outcome, the vote and the bin",
			args{decided("d2", 0.15), add, applied, nil},
			want{
				`{"kind":"add","id":"k-2","version":1,"extract":"e-1","anchor":3,"diff":"` + hmac + `"}`,
				`{"tier":1,"judge":"c-1","outcome":"conform","vote":1,"bin":"b1","action":"apply"}`, "", nil,
			},
		},
		{
			"a retry carries the error of its judge call",
			args{decided("d3", 0.15), add, curate.Decision{Tier: 1, Action: curate.ActionRetry}, failed},
			want{`{"kind":"add","id":"k-2","version":1,"extract":"e-1","anchor":3,"diff":"` + hmac + `"}`,
				`{"tier":1,"action":"retry"}`, "judge_member_failed", nil},
		},
		{
			"a widening names the kind and bin it opens and no version",
			args{decided("d4", 1), curate.Change{Kind: curate.ChangeWiden, Anchor: 3, Opens: &update}, curate.Decision{Tier: 2, Action: curate.ActionAsk}, nil},
			want{`{"kind":"widen","anchor":3,"opens":{"kind":"update","bin":"b1"}}`, `{"tier":2,"action":"ask"}`, "", nil},
		},
		{
			"a decision of the judge carries its confidence",
			args{decided("d4", 0.15), add, with(applied, func(d *curate.Decision) { d.Confidence = &share }), nil},
			want{
				`{"kind":"add","id":"k-2","version":1,"extract":"e-1","anchor":3,"diff":"` + hmac + `"}`,
				`{"tier":1,"judge":"c-1","outcome":"conform","vote":1,"bin":"b1","confidence":1,"action":"apply"}`, "", nil,
			},
		},
		{
			"a widening without what it opens is refused",
			args{decided("d5", 1), curate.Change{Kind: curate.ChangeWiden, Anchor: 3}, curate.Decision{Tier: 2, Action: curate.ActionAsk}, nil},
			want{err: curate.ErrDecideInvalid},
		},
		{
			"a widening of a kind tier 1 never carries out is refused",
			args{decided("d5", 1), curate.Change{Kind: curate.ChangeWiden, Anchor: 3, Opens: &curate.Opening{Kind: curate.ChangeDrop}}, curate.Decision{Tier: 2, Action: curate.ActionAsk}, nil},
			want{err: curate.ErrDecideInvalid},
		},
		{
			"another change naming what it opens is refused",
			args{decided("d5", 1), curate.Change{Kind: curate.ChangeActivate, Anchor: 3, Opens: &update}, curate.Decision{Tier: 2, Action: curate.ActionAsk}, nil},
			want{err: curate.ErrDecideInvalid},
		},
		{
			"a proposal without the intervention it asks about is refused",
			args{decided("d5", 1), curate.Change{Kind: curate.ChangeIntervention, Anchor: 3}, curate.Decision{Tier: 2, RuleCode: curate.RuleProposal, Action: curate.ActionAsk}, nil},
			want{err: curate.ErrDecideInvalid},
		},
		{
			"another change naming a proposal is refused",
			args{decided("d5", 1), curate.Change{Kind: curate.ChangeAdd, ID: "k-2", Version: 1, Anchor: 3, Proposal: "i-1"}, applied, nil},
			want{err: curate.ErrDecideInvalid},
		},
		{"a confidence past 1 is refused", args{decided("d5", 1), add, with(applied, func(d *curate.Decision) { d.Confidence = &over }), nil}, want{err: curate.ErrDecideInvalid}},
		{"an unknown kind is refused", args{decided("d5", 1), curate.Change{Kind: "merge"}, applied, nil}, want{err: curate.ErrDecideInvalid}},
		{"a change of a version without it is refused", args{decided("d5", 1), curate.Change{Kind: curate.ChangeAdd, ID: "k-2"}, applied, nil}, want{err: curate.ErrDecideInvalid}},
		{"a compaction without its id is refused", args{decided("d5", 1), curate.Change{Kind: curate.ChangeCompaction}, applied, nil}, want{err: curate.ErrDecideInvalid}},
		{
			"a join that names no older candidate is refused",
			args{decided("d5", 1), curate.Change{Kind: curate.ChangeJoin, ID: "k-2", Version: 1}, applied, nil}, want{err: curate.ErrDecideInvalid},
		},
		{
			"a join names the older candidate it adds the evidence to",
			args{decided("d6", 1), curate.Change{Kind: curate.ChangeJoin, ID: "k-2", Version: 1, Old: []knowledge.Ref{{ID: "k-1", Version: 1}}, Anchor: 3},
				curate.Decision{RuleCode: curate.RuleSameAsCandidate, Action: curate.ActionJoin, Reason: knowledge.ReasonDuplicate}, nil},
			want{
				`{"kind":"join","id":"k-2","version":1,"old":[{"id":"k-1","version":1}],"anchor":3}`,
				`{"tier":0,"rule_code":"same_as_candidate","action":"join","reason":"duplicate"}`, "", nil,
			},
		},
		{"an unknown action is refused", args{decided("d5", 1), add, with(applied, func(d *curate.Decision) { d.Action = "approve" }), nil}, want{err: curate.ErrDecideInvalid}},
		{"a tier past 2 is refused", args{decided("d5", 1), add, with(applied, func(d *curate.Decision) { d.Tier = 3 }), nil}, want{err: curate.ErrDecideInvalid}},
		{"an unknown rule code is refused", args{decided("d5", 1), add, with(applied, func(d *curate.Decision) { d.RuleCode = "close" }), nil}, want{err: curate.ErrDecideInvalid}},
		{"an unknown bin is refused", args{decided("d5", 1), add, with(applied, func(d *curate.Decision) { d.Bin = "b9" }), nil}, want{err: curate.ErrDecideInvalid}},
		{"a drop without its reopen condition is refused", args{decided("d5", 1), drop, with(dropped, func(d *curate.Decision) { d.Reopen = "" }), nil}, want{err: curate.ErrDecideInvalid}},
		{"a retry without its error is refused", args{decided("d5", 1), add, curate.Decision{Tier: 1, Action: curate.ActionRetry}, nil}, want{err: curate.ErrDecideInvalid}},
		{"an error on another action is refused", args{decided("d5", 1), add, applied, failed}, want{err: curate.ErrDecideInvalid}},
		{"a decide without its id is refused", args{curate.Event{Ref: "run-1"}, add, applied, nil}, want{err: curate.ErrDecideInvalid}},
		{"a decide without its run is refused", args{curate.Event{ID: "d5"}, add, applied, nil}, want{err: curate.ErrDecideInvalid}},
		{
			"a decide drawn on another unit is refused",
			args{curate.Event{ID: "d5", Ref: "run-1", Source: trace.SourcePush, Draw: decided("k-2", 1).Draw}, add, applied, nil},
			want{err: curate.ErrDecideInvalid},
		},
		{
			"a decide of a manual source is refused",
			args{curate.Event{ID: "d5", Ref: "run-1", Source: trace.SourceManual, Draw: decided("d5", 1).Draw}, add, applied, nil},
			want{err: curate.ErrDecideInvalid},
		},
		{
			"a draw without its policy version is refused",
			args{func() curate.Event { e := decided("d5", 1); e.Draw.PolicyVersion = ""; return e }(), add, applied, nil},
			want{err: trace.ErrDrawInvalid},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.event.Decide(tc.args.change, tc.args.decision, tc.args.failed, at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want, want{string(got.Input), string(got.Output), got.Error, tc.want.err})
		})
	}
}

// A decide is pushed under its own id with the audit draw on it and validates against its published schema
func TestEventDecideFields(t *testing.T) {
	change := curate.Change{Kind: curate.ChangeAdd, ID: "k-2", Version: 1, Anchor: 3}

	got, err := decided("d1", 0.15).Decide(change, curate.Decision{Tier: 1, Action: curate.ActionShadow}, nil, at)
	require.NoError(t, err)

	assert.Equal(t, []any{"d1", trace.NameDecide, "run-1", "add", at, trace.SourcePush, "d1", true},
		[]any{got.ID, got.Name, got.Ref, got.Subject, got.Time, got.Source, got.Draw.Unit, got.Draw.Drawn})
	assert.NoError(t, schema.Validate(string(trace.NameDecide), got.Record()))
	var back curate.Change
	require.NoError(t, json.Unmarshal(got.Input, &back))
	assert.Equal(t, change, back)
}
