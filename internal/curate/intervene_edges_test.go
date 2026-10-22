package curate_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// An answer through the expose that showed the question names it with the delay since and one through the decide names the newest expose
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
			x := d.expose(t, decide)
			target := decide
			if tc.args.byExpose {
				target = x
			}

			got, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: target, Choice: trace.ChoiceDrop}, person)

			require.NoError(t, err)
			answer, err := d.stores.Traces.Get(ctx, got.Answer)
			require.NoError(t, err)
			assert.Equal(t, x, answer.Ref)
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
		{"a merge of a change whose version the records lack", args{func(t *testing.T, d desk) string {
			return d.asking(t, "r-k-add", curate.Change{Kind: curate.ChangeAdd, ID: "k-gone", Version: 1, Anchor: 1}, curate.RuleRoom)
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

// A knob set before any cycle names the start value as the one it had and a digest with no cursor at hand answers none
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

// The two choices each question offers with the one curate prepared first
func TestDecidedChoices(t *testing.T) {
	tcs := []struct {
		name string
		args curate.Decided
		want [2]curate.Choice
	}{
		{"an audit", curate.Decided{Decision: curate.Decision{Action: curate.ActionApply}}, [2]curate.Choice{curate.ChoiceSame, curate.ChoiceUndo}},
		{"a widening", curate.Decided{Change: curate.Change{Kind: curate.ChangeWiden}, Decision: curate.Decision{Action: curate.ActionAsk}},
			[2]curate.Choice{curate.ChoiceYes, curate.ChoiceNotYet}},
		{"a proposal", curate.Decided{Change: curate.Change{Kind: curate.ChangeIntervention}, Decision: curate.Decision{Action: curate.ActionAsk}},
			[2]curate.Choice{curate.ChoiceYes, curate.ChoiceNotYet}},
		{"drift", curate.Decided{Change: curate.Change{Kind: curate.ChangeAdd}, Decision: curate.Decision{RuleCode: curate.RuleDrift, Action: curate.ActionAsk}},
			[2]curate.Choice{curate.ChoiceMine, curate.ChoiceReview}},
		{"a reanchor of no rule", curate.Decided{Change: curate.Change{Kind: curate.ChangeReanchor}, Decision: curate.Decision{Tier: 2, Action: curate.ActionAsk}},
			[2]curate.Choice{curate.ChoiceMine, curate.ChoiceReview}},
		{"a compaction", curate.Decided{Change: curate.Change{Kind: curate.ChangeCompaction}, Decision: curate.Decision{Tier: 2, Action: curate.ActionAsk}},
			[2]curate.Choice{curate.ChoiceMerge, curate.ChoiceSeparate}},
		{"room", curate.Decided{Change: curate.Change{Kind: curate.ChangeAdd}, Decision: curate.Decision{RuleCode: curate.RuleRoom, Action: curate.ActionAsk}},
			[2]curate.Choice{curate.ChoiceMerge, curate.ChoiceSeparate}},
		{"a change the judge found against", curate.Decided{Change: curate.Change{Kind: curate.ChangeAdd},
			Decision: curate.Decision{Outcome: curate.OutcomeAgainst, Action: curate.ActionAsk}}, [2]curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Choices())
		})
	}
}

// An answer keep to a change the judge found against overturns its prediction
func TestIntervenerOverturn(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, answer(0.9, 0.8), []knowledge.Knowledge{lesson("k-against", 1, "Skip the tests", acme("acme"), knowledge.StatusCandidate, "")}, nil)
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	require.Equal(t, []string{"add k-against v1 hold judge_against 2"}, decisionsOf(first.Decided))

	got, err := b.intervener(nil).Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: first.Decided[0].ID, Choice: trace.ChoiceKeep}, person)

	require.NoError(t, err)
	answer, err := b.stores.Traces.Get(ctx, got.Answer)
	require.NoError(t, err)
	var a curate.Answer
	require.NoError(t, json.Unmarshal(answer.Input, &a))
	assert.True(t, a.Overturn)
}

// A mine takes the versions nodloop approved in the place of the run its question cites and nothing for a run no record holds
func TestIntervenerMine(t *testing.T) {
	ctx := context.Background()
	tcs := []struct {
		name string
		// The run the drift question cites
		args string
		want []knowledge.Ref
	}{
		{"a run of the place", "r-k-carried", []knowledge.Ref{{ID: "k-carried", Version: 1}}},
		{"a run no record holds", "r-gone", nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			decide := d.asking(t, tc.args, curate.Change{Kind: curate.ChangeAdd, ID: "k-add", Version: 1, Anchor: 1}, curate.RuleDrift)

			got, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: decide, Choice: trace.ChoiceMine}, person)

			require.NoError(t, err)
			answer, err := d.stores.Traces.Get(ctx, got.Answer)
			require.NoError(t, err)
			var a curate.Answer
			require.NoError(t, json.Unmarshal(answer.Input, &a))
			assert.Equal(t, tc.want, a.Refs)
		})
	}
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

// A person's write whose event fails to append after its ledger write is recorded once by its retry
// The retry takes the ledger write of the earlier try so the ledger and the event each hold it once and name each other
func TestIntervenerRetryAfterEvent(t *testing.T) {
	ctx := context.Background()
	type args struct {
		// The person's write through the intervener
		write func(iv *curate.Intervener) error
		// The trace the write records
		event trace.Name
	}
	reply := func(iv *curate.Intervener) error {
		_, err := iv.Reply(ctx, curate.Reply{Session: "s-new", Run: "run-s-new", Word: "keep", Person: "ann"})
		return err
	}
	judge := func(iv *curate.Intervener) error {
		_, err := iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-ask:1", Choice: trace.ChoiceKeep}, person)
		return err
	}
	type want struct {
		// The records of the ledger and the events that name the decision of k-ask
		records, events int
		status          string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a reply to a question", args{reply, trace.NameAnswer}, want{1, 1, "approved"}},
		{"a judgment of the manual path", args{judge, trace.NameIntervention}, want{1, 1, "approved"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			_, err := d.iv.Ask(ctx, d.stopIn(t, "s-new", "svc", true))
			require.NoError(t, err)
			require.ErrorIs(t, tc.args.write(d.flakyIntervener(-1, 0)), assert.AnError)

			err = tc.args.write(d.iv)

			require.NoError(t, err)
			all, err := d.stores.Ledger.All(ctx)
			require.NoError(t, err)
			k, err := all.Version("k-ask", 1)
			require.NoError(t, err)
			named := slices.DeleteFunc(slices.Clone(all), func(r knowledge.Knowledge) bool { return r.Decision != k.Decision })
			found, err := d.stores.Traces.List(ctx, record.TraceFilter{Name: string(tc.args.event)})
			require.NoError(t, err)
			events := slices.DeleteFunc(slices.Clone(found), func(tr record.Trace) bool { return tr.ID != k.Decision })
			assert.Equal(t, tc.want, want{len(named), len(events), string(k.Status)})
			assert.Len(t, found, 1, "the retry recorded no second event")
		})
	}
}

// An undo of a compaction curate applied reverses it whole so runs receive the items it merged as before
func TestIntervenerUndoCompaction(t *testing.T) {
	ctx := context.Background()
	approved := knowledge.StatusApproved
	b := newBench(t, nil, []knowledge.Knowledge{
		lesson("k-a", 1, "rule a", acme("work"), approved, knowledge.ApproverNodloop), lesson("k-b", 1, "rule b", acme("work"), approved, knowledge.ApproverNodloop),
	}, nil)
	b.knobs(t, func(*curate.Knobs) {})
	before := b.received(t)
	c, err := b.stores.Ledger.ProposeCompaction(ctx, "k-a", []knowledge.Knowledge{{
		Kind: knowledge.KindMeaning, Content: "rule a and rule b", Run: acme("work"), Author: "claude",
		Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-a", Version: 1}, {ID: "k-b", Version: 1}}},
	}})
	require.NoError(t, err)
	cov, err := b.coverage(ctx, c.ID)
	require.NoError(t, err)
	id := trace.NewID(written)
	require.NoError(t, testkit.Err(b.stores.Ledger.ApplyCompaction(ctx, c.ID, id, cov)))
	event := curate.Event{ID: id, Ref: "r-k-a", Source: trace.SourcePush, Draw: trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "5eed", id, "audit", 1)}
	tr, err := event.Decide(curate.Change{Kind: curate.ChangeCompaction, Compaction: c.ID, Anchor: 1}, curate.Decision{Tier: 1, Action: curate.ActionApply}, nil, written)
	require.NoError(t, err)
	require.NoError(t, b.stores.Traces.Append(ctx, tr.Record()))
	merged := b.received(t)

	got, err := b.intervener(nil).Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: id, Choice: trace.ChoiceUndo}, person)

	require.NoError(t, err)
	require.NotEqual(t, before, merged, "the compaction changed what runs receive")
	assert.Equal(t, before, b.received(t))
	assert.NotEmpty(t, got.Wrote)
}

// A write of the person takes no earlier write a recorded event names and none that a later write changed
func TestIntervenerRetryTakesOnlyItsOwnWrite(t *testing.T) {
	ctx := context.Background()
	keep := curate.Reply{Session: "s-new", Run: "run-s-new", Word: "keep", Person: "ann"}
	judge := func(choice trace.Choice) curate.Request {
		return curate.Request{Action: trace.ActionJudge, Target: "k-ask:1", Choice: choice}
	}
	type args struct {
		// Whether the first reply fails to append its answer
		lost bool
		// The judgment of the person after the first reply
		then trace.Choice
	}
	type want struct {
		// Whether the last reply fails and the answers recorded
		failed  bool
		answers []string
		status  string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a reaffirm after an answer writes under its own intervention", args{false, trace.ChoiceKeep}, want{false, []string{"keep reply push ann"}, "approved"}},
		{"a reply after a judgment changed the version takes no earlier write", args{true, trace.ChoiceUndo}, want{true, []string{}, "retired"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			_, err := d.iv.Ask(ctx, d.stopIn(t, "s-new", "svc", true))
			require.NoError(t, err)
			first := d.iv
			if tc.args.lost {
				first = d.flakyIntervener(-1, 0)
			}
			_ = testkit.Err(first.Reply(ctx, keep))
			judged, err := d.iv.Intervene(ctx, judge(tc.args.then), person)
			require.NoError(t, err)

			_, err = d.iv.Reply(ctx, keep)

			all, lerr := d.stores.Ledger.All(ctx)
			require.NoError(t, lerr)
			k, lerr := all.Version("k-ask", 1)
			require.NoError(t, lerr)
			assert.Equal(t, tc.want, want{err != nil, d.answers(t), string(k.Status)})
			assert.Equal(t, judged.Trace.ID, k.Decision, "the judgment wrote under its own intervention")
		})
	}
}
