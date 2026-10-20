package curate_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// An answer through the expose that showed the question names it and the delay since, and one through the decide names the newest expose
func TestIntervenerAnswerExpose(t *testing.T) {
	ctx := context.Background()
	type args struct {
		// Whether the request names the expose instead of the decide
		byExpose bool
	}
	tcs := []struct {
		name string
		args args
	}{
		{"named by its expose", args{true}},
		{"named by its decide", args{false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			decide := d.decideOf(t, "k-ask")
			shown := curate.Event{Session: "s-1", Ref: decide, Source: trace.SourcePush, Draw: d.first.Event.AuditDraw("s-1", 1)}
			x, err := shown.Expose(curate.Exposure{
				Kind: curate.QuestionDirection, Key: "ab12", Surface: curate.SurfaceContext, Rules: curate.QuestionRules,
				Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Text: hex.EncodeToString(make([]byte, 32)), Round: 1,
			}, d.stores.Clock.Now())
			require.NoError(t, err)
			require.NoError(t, d.stores.Traces.Append(ctx, x.Record()))
			target := decide
			if tc.args.byExpose {
				target = x.ID
			}

			got, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: target, Choice: trace.ChoiceDrop}, person)

			require.NoError(t, err)
			answer, err := d.stores.Traces.Get(ctx, got.Answer)
			require.NoError(t, err)
			assert.Equal(t, x.ID, answer.Ref)
		})
	}
}

// What a person's keep, drop or yes cannot carry out is refused and nothing is recorded
func TestIntervenerAnswerRefused(t *testing.T) {
	ctx := context.Background()
	type args struct {
		// The decide of the question under test
		decide func(t *testing.T, d desk) string
		choice trace.Choice
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a yes to a proposal no longer recorded", args{func(t *testing.T, d desk) string {
			return d.asking(t, "", curate.Change{Kind: curate.ChangeIntervention, Proposal: "gone", Anchor: 1}, curate.RuleProposal)
		}, trace.ChoiceYes}, curate.ErrTargetInvalid},
		{"a yes to a proposal the records can no longer carry out", args{func(t *testing.T, d desk) string {
			paused, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionPause, Target: "add"}, person)
			require.NoError(t, err)
			p, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionResume, Target: paused.Trace.ID}, agent)
			require.NoError(t, err)
			_, err = d.iv.Intervene(ctx, curate.Request{Action: trace.ActionResume, Target: paused.Trace.ID}, person)
			require.NoError(t, err)
			return p.Decide
		}, trace.ChoiceYes}, curate.ErrTargetInvalid},
		{"a keep the ledger refuses since the update reaches further than the version it replaces", args{func(t *testing.T, d desk) string {
			wider := lesson("k-person", 2, "Run every test before a commit", &knowledge.RunScope{Producer: "session"}, knowledge.StatusCandidate, "")
			wider.Base = 1
			_, err := d.stores.Ledger.Import(ctx, []knowledge.Knowledge{wider})
			require.NoError(t, err)
			return d.asking(t, "r-k-person", curate.Change{Kind: curate.ChangeUpdate, ID: "k-person", Version: 2, Anchor: 1}, curate.RulePersonItem)
		}, trace.ChoiceKeep}, knowledge.ErrScopeWidened},
		{"a merge of a change that needs no room", args{func(t *testing.T, d desk) string {
			return d.asking(t, "r-k-add", curate.Change{Kind: curate.ChangeAdd, ID: "k-add", Version: 1, Anchor: 1}, curate.RuleRoom)
		}, trace.ChoiceMerge}, curate.ErrTargetInvalid},
		{"a merge of a compaction no record names", args{func(t *testing.T, d desk) string {
			return d.asking(t, "r-k-add", curate.Change{Kind: curate.ChangeCompaction, Compaction: "c-gone", Anchor: 1}, "")
		}, trace.ChoiceMerge}, knowledge.ErrNotFound},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			decide := tc.args.decide(t, d)
			recorded := d.interventions(t)

			_, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: decide, Choice: tc.args.choice}, person)

			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, recorded, d.interventions(t))
		})
	}
}

// A question whose decide no cycle stands behind has no draw envelope for its answer
func TestIntervenerAnswerWithoutCycle(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, []knowledge.Knowledge{lesson("k-ask", 1, "Use the staging cluster", acme("svc"), knowledge.StatusCandidate, "")}, nil)
	now := b.stores.Clock.Now()
	ev := curate.KnobEvent{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d", Seed: "5eed"}
	id := trace.NewID(now)
	tr, err := curate.Event{ID: id, Ref: "r-k-ask", Source: trace.SourcePush, Draw: ev.AuditDraw(id, 0)}.
		Decide(curate.Change{Kind: curate.ChangeAdd, ID: "k-ask", Version: 1, Anchor: 1}, curate.Decision{Tier: 2, RuleCode: curate.RuleScopeWider, Action: curate.ActionAsk}, nil, now)
	require.NoError(t, err)
	require.NoError(t, b.stores.Traces.Append(ctx, tr.Record()))

	_, err = b.intervener(nil).Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: id, Choice: trace.ChoiceKeep}, person)

	assert.ErrorIs(t, err, curate.ErrTargetInvalid)
}

// A knob set before any cycle names the start value as the one it had, and a digest with no cursor at hand answers none
func TestIntervenerBeforeAnyCycle(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, nil, nil)
	iv := curate.NewIntervener(b.stores.Ledger, b.stores.Traces, curate.InterveneSetting{}, b.stores.Clock.Now)
	value := 0.3

	knob, err := iv.Intervene(ctx, curate.Request{Action: trace.ActionKnob, Target: "audit_tier1", Value: &value}, person)
	require.NoError(t, err)
	digest, err := iv.Intervene(ctx, curate.Request{Action: trace.ActionDigest}, person)
	require.NoError(t, err)

	var in trace.Intervention
	require.NoError(t, json.Unmarshal(knob.Trace.Input, &in))
	assert.Equal(t, []any{0.15, ""}, []any{*in.Before, digest.Since})
}

// A form of an answer to a question about an offer names no lesson, and one of a judgment of a compaction names the merge
func TestIntervenerDescribeNoLesson(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	widen := d.asking(t, "", curate.Change{Kind: curate.ChangeWiden, Opens: &curate.Opening{Kind: curate.ChangeUpdate, Bin: curate.Bin1}, Anchor: 1}, "")
	merge := d.asking(t, "r-k-add", curate.Change{Kind: curate.ChangeCompaction, Compaction: "c-1", Anchor: 1}, "")
	tcs := []struct {
		name string
		args curate.Request
		want string
	}{
		{"an offer", curate.Request{Action: trace.ActionAnswer, Target: widen, Choice: trace.ChoiceYes}, "Answer yes to the question about a change nodloop prepared"},
		{"a compaction", curate.Request{Action: trace.ActionAnswer, Target: merge, Choice: trace.ChoiceMerge},
			"Answer merge to the question about the merge of lessons that was prepared"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.iv.Describe(ctx, tc.args)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	_, err := curate.NewIntervener(d.stores.Ledger, &testkit.FlakyTraces{Store: d.stores.Traces, Lists: failing(0)}, curate.InterveneSetting{}, d.stores.Clock.Now).
		Describe(ctx, curate.Request{Action: trace.ActionDigest})
	assert.ErrorIs(t, err, assert.AnError)
}

// The choices of each question and whether an answer overturns what curate carried out or what the judge predicted
func TestDecidedChoices(t *testing.T) {
	type want struct {
		choices [2]curate.Choice
		// The choice that overturns and empty where none does
		overturns curate.Choice
	}
	tcs := []struct {
		name string
		args curate.Decided
		want want
	}{
		{"an audit", curate.Decided{Decision: curate.Decision{Action: curate.ActionApply}}, want{[2]curate.Choice{curate.ChoiceSame, curate.ChoiceUndo}, ""}},
		{"a widening", curate.Decided{Change: curate.Change{Kind: curate.ChangeWiden}, Decision: curate.Decision{Action: curate.ActionAsk}},
			want{[2]curate.Choice{curate.ChoiceYes, curate.ChoiceNotYet}, ""}},
		{"a proposal", curate.Decided{Change: curate.Change{Kind: curate.ChangeIntervention}, Decision: curate.Decision{Action: curate.ActionAsk}},
			want{[2]curate.Choice{curate.ChoiceYes, curate.ChoiceNotYet}, ""}},
		{"drift", curate.Decided{Change: curate.Change{Kind: curate.ChangeAdd}, Decision: curate.Decision{RuleCode: curate.RuleDrift, Action: curate.ActionAsk}},
			want{[2]curate.Choice{curate.ChoiceMine, curate.ChoiceReview}, ""}},
		{"room", curate.Decided{Change: curate.Change{Kind: curate.ChangeAdd}, Decision: curate.Decision{RuleCode: curate.RuleRoom, Action: curate.ActionAsk}},
			want{[2]curate.Choice{curate.ChoiceMerge, curate.ChoiceSeparate}, ""}},
		{"a change the judge found against", curate.Decided{Change: curate.Change{Kind: curate.ChangeAdd},
			Decision: curate.Decision{Outcome: curate.OutcomeAgainst, Action: curate.ActionAsk}}, want{[2]curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want.choices, tc.args.Choices())
		})
	}
}

// An answer keep to a change the judge found against overturns its prediction
func TestIntervenerOverturn(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, answer(0.9, 0.8), []knowledge.Knowledge{lesson("k-against", 1, "Skip the tests", acme("acme"), knowledge.StatusCandidate, "")}, nil)
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	require.Equal(t, []string{"add k-against v1 ask judge_against 2"}, decisionsOf(first.Decided))

	got, err := b.intervener(nil).Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: first.Decided[0].ID, Choice: trace.ChoiceKeep}, person)

	require.NoError(t, err)
	answer, err := b.stores.Traces.Get(ctx, got.Answer)
	require.NoError(t, err)
	var a curate.Answer
	require.NoError(t, json.Unmarshal(answer.Input, &a))
	assert.True(t, a.Overturn)
}

// A version nodloop approved that the person took joins the anchor and its version names what took it
func TestAnchorTaking(t *testing.T) {
	carried := lesson("k-carried", 1, "Squash before a merge", acme("acme"), knowledge.StatusApproved, knowledge.ApproverNodloop)
	other := lesson("k-other", 1, "Rebase before a push", acme("acme"), knowledge.StatusApproved, knowledge.ApproverNodloop)
	all := knowledge.Set{personLesson(), carried, other}
	type args struct {
		taken map[knowledge.Ref]string
	}
	type want struct {
		items []string
		cause string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"nothing taken", args{nil}, want{[]string{"k-person"}, curate.CauseApproval}},
		{"a version taken by a reanchor", args{map[knowledge.Ref]string{{ID: "k-carried", Version: 1}: "i-1"}}, want{[]string{"k-carried", "k-person"}, "i-1"}},
		{"a version no longer approved", args{map[knowledge.Ref]string{{ID: "k-carried", Version: 2}: "i-1"}}, want{[]string{"k-person"}, curate.CauseApproval}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := curate.NewAnchor(all, nil, nil).Taking(tc.args.taken)

			next, changed := a.Next(curate.AnchorVersion{})

			require.True(t, changed)
			ids := []string{}
			for _, k := range a.Items() {
				ids = append(ids, k.ID)
			}
			assert.Equal(t, tc.want.items, ids)
			assert.Equal(t, tc.want.cause, next.Cause)
		})
	}
}
