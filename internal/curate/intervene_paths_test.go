package curate_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The questions due in curate.json as change kind, rule, the version, the proposal named and the session
func (b bench) dueOf(t *testing.T) []string {
	t.Helper()
	m, err := curate.ReadMark(b.stores.Dir)
	require.NoError(t, err)
	out := []string{}
	for _, d := range m.Due {
		named := ""
		if d.Proposal != "" {
			named = "proposal " + d.Session
		}
		out = append(out, string(d.Change)+" "+string(d.Rule)+" "+d.Ref.ID+" "+named)
	}
	slices.Sort(out)
	return out
}

// An agent's proposal changes nothing, its question is due at once in the session that made it,
// and the person's yes carries it out as the person's own while a reverse of that answer takes it back and opens the question again
func TestIntervenerProposal(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	before := d.received(t)
	due := d.dueOf(t)
	request := curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep, Session: "s-9"}

	proposed, err := d.iv.Intervene(ctx, request, agent)

	require.NoError(t, err)
	assert.Equal(t, before, d.received(t), "a proposal changes no record")
	assert.NotEmpty(t, proposed.Decide)
	assert.Equal(t, slices.Concat(due, []string{"intervention proposal k-add proposal s-9"}), d.dueOf(t), "one question beyond those due")
	assert.Equal(t, []string{"judge manual agent none"}, d.interventions(t))

	yes, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: proposed.Decide, Choice: trace.ChoiceYes}, person)
	require.NoError(t, err)
	kept := d.received(t)
	back, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionReverse, Target: yes.Trace.ID}, person)
	require.NoError(t, err)

	assert.Contains(t, kept, `approved k-add "Name the failing test first" runs of session. repo=acme`)
	assert.Equal(t, []string{"judge manual agent none", "judge manual person key ann", "answer manual person key ann", "reverse manual person key ann"},
		d.interventions(t))
	assert.Equal(t, before, d.received(t))
	assert.NotEmpty(t, back.Wrote)
	_, err = d.iv.Describe(ctx, curate.Request{Action: trace.ActionAnswer, Target: proposed.Decide, Choice: trace.ChoiceYes})
	assert.NoError(t, err, "the question of the proposal is open again")
}

// A proposal waits for the next pass where curate.json is not at hand, has no cycle yet or another pass holds it
func TestIntervenerProposalLater(t *testing.T) {
	ctx := context.Background()
	request := curate.Request{Action: trace.ActionPause, Target: string(curate.ChangeAdd), Session: "s-9"}
	type args struct {
		marks func(t *testing.T, d desk) curate.Marks
	}
	tcs := []struct {
		name string
		args args
	}{
		{"no curate.json at hand", args{func(*testing.T, desk) curate.Marks { return nil }}},
		{"curate.json of no cycle", args{func(t *testing.T, _ desk) curate.Marks { return curate.NewMarker(t.TempDir()) }}},
		{"curate.json another pass holds", args{func(t *testing.T, d desk) curate.Marks {
			marker := curate.NewMarker(d.stores.Dir)
			held, err := marker.Hold()
			require.NoError(t, err)
			t.Cleanup(func() { _ = held.Release() })
			return marker
		}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)

			proposed, err := d.bench.intervener(tc.args.marks(t, d)).Intervene(ctx, request, agent)

			require.NoError(t, err)
			assert.Empty(t, proposed.Decide)
		})
	}
}

// The next pass prepares the question of a proposal no decide asks about yet and never a second one
func TestCuratorPassProposals(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	_, err := d.bench.intervener(nil).Intervene(ctx, curate.Request{Action: trace.ActionPause, Target: string(curate.ChangeAdd), Session: "s-9"}, agent)
	require.NoError(t, err)
	_, err = d.bench.intervener(nil).Intervene(ctx, curate.Request{Action: trace.ActionDigest}, agent)
	require.NoError(t, err)

	first, err := d.curator().Pass(ctx, d.setting(t))
	require.NoError(t, err)
	second, err := d.curator().Pass(ctx, d.setting(t))
	require.NoError(t, err)

	assert.Equal(t, []string{"intervention  v0 ask proposal 2"}, decisionsOf(first.Decided))
	assert.Equal(t, []string{}, decisionsOf(second.Decided))
	i := slices.IndexFunc(second.Due, func(due curate.Due) bool { return due.Proposal != "" })
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, []any{curate.QuestionDirection, curate.RuleProposal, "s-9"}, []any{second.Due[i].Kind, second.Due[i].Rule, second.Due[i].Session})
}

// A digest changes nothing, needs no proof, is never a proposal and answers the cursor since the person's last judgment
func TestIntervenerDigest(t *testing.T) {
	ctx := context.Background()
	type args struct {
		by curate.Submitter
		// Whether the person judged before the digest
		judged bool
	}
	type want struct {
		// Whether the cursor names the person's judgment
		since bool
		// The intervention the digest recorded
		recorded string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a digest before any judgment starts at the first record", args{agent, false}, want{false, "digest manual agent none"}},
		{"a digest of an agent after a judgment starts after it", args{agent, true}, want{true, "digest manual agent none"}},
		{"a digest of a person names the person", args{person, true}, want{true, "digest manual person key ann"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			judged := ""
			if tc.args.judged {
				kept, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}, person)
				require.NoError(t, err)
				judged = kept.Trace.ID
			}
			before := d.received(t)

			got, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionDigest, Target: "session"}, tc.args.by)

			require.NoError(t, err)
			assert.Equal(t, tc.want.since, got.Since == judged && judged != "")
			assert.Empty(t, got.Decide)
			assert.Equal(t, before, d.received(t))
			recorded := d.interventions(t)
			assert.Equal(t, tc.want.recorded, recorded[len(recorded)-1])
		})
	}
}

// What the records cannot carry out is refused before anything is recorded
func TestIntervenerRefused(t *testing.T) {
	ctx := context.Background()
	value := func(v float64) *float64 { return &v }
	type args struct {
		request func(t *testing.T, d desk) curate.Request
		by      curate.Submitter
	}
	fixed := func(r curate.Request) func(*testing.T, desk) curate.Request {
		return func(*testing.T, desk) curate.Request { return r }
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"an unknown action", args{fixed(curate.Request{Action: "approve"}), person}, curate.ErrTargetInvalid},
		{"a judgment of an unknown version", args{fixed(curate.Request{Action: trace.ActionJudge, Target: "k-gone:1", Choice: trace.ChoiceKeep}), person},
			knowledge.ErrNotFound},
		{"a judgment of a target that names nothing", args{fixed(curate.Request{Action: trace.ActionJudge, Target: "nothing", Choice: trace.ChoiceKeep}), person},
			curate.ErrTargetInvalid},
		{"a judgment that does not fit the status", args{fixed(curate.Request{Action: trace.ActionJudge, Target: "k-person:1", Choice: trace.ChoiceScope}), person},
			curate.ErrTargetInvalid},
		{"a scope a recorded run never carried", args{fixed(curate.Request{
			Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceScope, Labels: trace.Labels{"repo": {"elsewhere"}},
		}), person}, knowledge.ErrScopeUnobserved},
		{"a scope a candidate cannot take", args{fixed(curate.Request{
			Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceScope, Labels: trace.Labels{"repo": {}},
		}), person}, trace.ErrLabelEmpty},
		{"an answer to no question", args{fixed(curate.Request{Action: trace.ActionAnswer, Target: "nothing", Choice: trace.ChoiceKeep}), person},
			curate.ErrTargetInvalid},
		{"an answer the question does not offer", args{func(t *testing.T, d desk) curate.Request {
			return curate.Request{Action: trace.ActionAnswer, Target: d.decideOf(t, "k-ask"), Choice: trace.ChoiceYes}
		}, person}, curate.ErrTargetInvalid},
		{"a dismiss of a question with a choice", args{func(t *testing.T, d desk) curate.Request {
			return curate.Request{Action: trace.ActionDismiss, Target: d.decideOf(t, "k-ask"), Choice: trace.ChoiceKeep}
		}, person}, trace.ErrInterventionInvalid},
		{"a pause of a kind tier 1 never carries out", args{fixed(curate.Request{Action: trace.ActionPause, Target: "withdraw"}), person},
			curate.ErrTargetInvalid},
		{"a resume of no pause", args{fixed(curate.Request{Action: trace.ActionResume, Target: "nothing"}), person}, curate.ErrTargetInvalid},
		{"a knob outside its range", args{fixed(curate.Request{Action: trace.ActionKnob, Target: "audit_tier1", Value: value(0.9)}), person},
			curate.ErrTargetInvalid},
		{"a knob of no name", args{fixed(curate.Request{Action: trace.ActionKnob, Target: "speed", Value: value(0.1)}), person}, curate.ErrTargetInvalid},
		{"a knob without a value", args{fixed(curate.Request{Action: trace.ActionKnob, Target: "item_hold"}), person}, curate.ErrTargetInvalid},
		{"a cycle of part of a week", args{fixed(curate.Request{Action: trace.ActionKnob, Target: "cycle_weeks", Value: value(1.5)}), person},
			curate.ErrTargetInvalid},
		{"a reanchor of a place nodloop approved nothing in", args{fixed(curate.Request{
			Action: trace.ActionReanchor, Target: "session", Labels: trace.Labels{"repo": {"svc"}},
		}), person}, curate.ErrTargetInvalid},
		{"a reverse of no intervention", args{fixed(curate.Request{Action: trace.ActionReverse, Target: "nothing"}), person}, curate.ErrTargetInvalid},
		{"a judgment under the reserved name", args{fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}),
			curate.Submitter{Proof: trace.ProofKey, Person: knowledge.ApproverNodloop, Source: trace.SourceManual}}, knowledge.ErrApproverReserved},
		{"a judgment the person makes as pushed", args{fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}),
			curate.Submitter{Proof: trace.ProofKey, Person: "ann", Source: trace.SourcePush}}, trace.ErrInterventionInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			recorded := d.interventions(t)

			_, err := d.iv.Intervene(ctx, tc.args.request(t, d), tc.args.by)

			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, recorded, d.interventions(t), "nothing is recorded")
		})
	}
}

// A question once answered or dismissed is closed and a reverse of a digest or of a proposal is refused
func TestIntervenerClosed(t *testing.T) {
	ctx := context.Background()
	type args struct {
		first  func(t *testing.T, d desk) curate.Request
		by     curate.Submitter
		second func(t *testing.T, d desk, first string) curate.Request
	}
	ask := func(action trace.Action, choice trace.Choice) func(*testing.T, desk) curate.Request {
		return func(t *testing.T, d desk) curate.Request {
			return curate.Request{Action: action, Target: d.decideOf(t, "k-ask"), Choice: choice}
		}
	}
	again := func(t *testing.T, d desk, _ string) curate.Request {
		return curate.Request{Action: trace.ActionAnswer, Target: d.decideOf(t, "k-ask"), Choice: trace.ChoiceKeep}
	}
	reverse := func(_ *testing.T, _ desk, first string) curate.Request {
		return curate.Request{Action: trace.ActionReverse, Target: first}
	}
	tcs := []struct {
		name string
		args args
	}{
		{"an answered question", args{ask(trace.ActionAnswer, trace.ChoiceDrop), person, again}},
		{"a dismissed question", args{ask(trace.ActionDismiss, ""), person, again}},
		{"a reverse of a digest", args{func(*testing.T, desk) curate.Request { return curate.Request{Action: trace.ActionDigest} }, person, reverse}},
		{"a reverse of a proposal", args{ask(trace.ActionAnswer, trace.ChoiceKeep), agent, reverse}},
		{"a resume of a pause taken back", args{
			func(*testing.T, desk) curate.Request {
				return curate.Request{Action: trace.ActionPause, Target: string(curate.ChangeAdd)}
			}, person,
			func(t *testing.T, d desk, first string) curate.Request {
				_, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionResume, Target: first}, person)
				require.NoError(t, err)
				return curate.Request{Action: trace.ActionResume, Target: first}
			},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			first, err := d.iv.Intervene(ctx, tc.args.first(t, d), tc.args.by)
			require.NoError(t, err)

			_, err = d.iv.Intervene(ctx, tc.args.second(t, d, first.Trace.ID), person)

			assert.ErrorIs(t, err, curate.ErrTargetInvalid)
		})
	}
}

// A reverse of a judgment whose version changed since is refused so the person decides that change apart
func TestIntervenerReverseStale(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	kept, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}, person)
	require.NoError(t, err)
	_, err = d.iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceUndo}, person)
	require.NoError(t, err)

	_, err = d.iv.Intervene(ctx, curate.Request{Action: trace.ActionReverse, Target: kept.Trace.ID}, person)

	assert.ErrorIs(t, err, knowledge.ErrReverseStale)
}

// A manual judgment carries source manual and no draw and leaves no question beyond those due before it
func TestIntervenerManualAddsNoQuestion(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	elicited := curate.Submitter{Proof: trace.ProofElicitation, Person: "person", Source: trace.SourceManual}

	got, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-ask:1", Choice: trace.ChoiceKeep, Session: "s-9"}, elicited)

	require.NoError(t, err)
	pass, err := d.curator().Pass(ctx, d.setting(t))
	require.NoError(t, err)
	assert.Equal(t, []any{trace.SourceManual, (*trace.Draw)(nil), ""}, []any{got.Trace.Source, got.Trace.Draw, got.Decide})
	assert.Less(t, len(pass.Due), len(d.first.Due), "the question the judgment settled is no longer due and none came")
	assert.Equal(t, []string{}, decisionsOf(pass.Decided))
}

// The prepared compaction a merge answer approves before the change that needed its room
func TestIntervenerMerge(t *testing.T) {
	ctx := context.Background()
	candidate, approved := knowledge.StatusCandidate, knowledge.StatusApproved
	big := "a long lesson for the place of the big repo, "
	for len(big) < 4200 {
		big += big
	}
	b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
		lesson("k-big1", 1, "first "+big, acme("big"), approved, "ann"), lesson("k-big2", 1, "second "+big, acme("big"), approved, "ann"),
		lesson("k-more", 1, "third "+big, acme("big"), candidate, ""),
	}, nil)
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	require.Equal(t, []string{"add k-more v1 ask room 2"}, decisionsOf(first.Decided))
	iv := b.intervener(nil)
	merge := curate.Request{Action: trace.ActionAnswer, Target: first.Decided[0].ID, Choice: trace.ChoiceMerge}
	_, err = iv.Intervene(ctx, merge, person)
	require.ErrorIs(t, err, curate.ErrTargetInvalid, "no compaction was prepared yet")
	_, err = b.stores.Ledger.ProposeCompaction(ctx, "k-big1", []knowledge.Knowledge{{
		Kind: knowledge.KindMeaning, Content: "first and second " + big[:200], Run: acme("big"), Author: "claude",
		Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-big1", Version: 1}, {ID: "k-big2", Version: 1}}},
	}})
	require.NoError(t, err)

	got, err := iv.Intervene(ctx, merge, person)

	require.NoError(t, err)
	assert.Contains(t, statuses(t, b), "k-more v1 approved ann")
	assert.Len(t, got.Wrote, 4, "the new item, the two old ones it retires and the change")
}

// A compaction judged keep through its id approves it with the coverage compact keeps and needs that check at hand
func TestIntervenerCompaction(t *testing.T) {
	ctx := context.Background()
	approved := knowledge.StatusApproved
	type args struct {
		coverage bool
	}
	type want struct {
		err   error
		wrote int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"with the coverage compact keeps", args{true}, want{nil, 4}},
		{"without a coverage check at hand", args{false}, want{curate.ErrTargetInvalid, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, []knowledge.Knowledge{
				lesson("k-a", 1, "rule a", acme("acme"), approved, "ann"), lesson("k-b", 1, "rule b", acme("acme"), approved, "ann"),
			}, nil)
			c, err := b.stores.Ledger.ProposeCompaction(ctx, "k-a", []knowledge.Knowledge{{
				Kind: knowledge.KindMeaning, Content: "rule a and rule b", Run: acme("acme"), Author: "claude",
				Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-a", Version: 1}, {ID: "k-b", Version: 1}, {ID: "k-person", Version: 1}}},
			}})
			require.NoError(t, err)
			setting := curate.InterveneSetting{Coverage: b.coverage}
			if !tc.args.coverage {
				setting.Coverage = nil
			}
			iv := curate.NewIntervener(b.stores.Ledger, b.stores.Traces, setting, b.stores.Clock.Now)

			got, err := iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: c.ID, Choice: trace.ChoiceKeep}, person)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, got.Wrote, tc.want.wrote)
		})
	}
}

// The form names the action and its target in plain words with no id or version
func TestIntervenerDescribe(t *testing.T) {
	ctx := context.Background()
	value := 0.3
	type args struct {
		request func(t *testing.T, d desk) curate.Request
	}
	fixed := func(r curate.Request) args { return args{func(*testing.T, desk) curate.Request { return r }} }
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"keep", fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}), `Keep the lesson "Name the failing test first"`},
		{"undo", fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceUndo}), `Undo the lesson "Name the failing test first"`},
		{"scope", fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceScope, Labels: trace.Labels{"repo": {"acme"}}}),
			`Keep the lesson "Name the failing test first" for repo acme`},
		{"scope everywhere", fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceScope}),
			`Keep the lesson "Name the failing test first" for every place`},
		{"answer", args{func(t *testing.T, d desk) curate.Request {
			return curate.Request{Action: trace.ActionAnswer, Target: d.decideOf(t, "k-ask"), Choice: trace.ChoiceDrop}
		}}, `Answer drop to the question about the lesson "Use the staging cluster"`},
		{"dismiss", args{func(t *testing.T, d desk) curate.Request {
			return curate.Request{Action: trace.ActionDismiss, Target: d.decideOf(t, "k-ask")}
		}}, `Close the question about the lesson "Use the staging cluster" with no decision, so nodloop never carries it out`},
		{"pause", fixed(curate.Request{Action: trace.ActionPause, Target: "add", Labels: trace.Labels{"repo": {"acme", "svc"}}}),
			"Pause what nodloop carries out for the kind add in repo acme or svc, so every such change is asked of you"},
		{"knob", fixed(curate.Request{Action: trace.ActionKnob, Target: "audit_tier1", Value: &value}),
			"Set the share of judged decisions checked with you to 0.3 from the next cycle"},
		{"reanchor", fixed(curate.Request{Action: trace.ActionReanchor, Target: "session", Labels: trace.Labels{"repo": {"work"}}}),
			"Take the lessons nodloop approved for runs of session. repo=work as your own"},
		{"digest", fixed(curate.Request{Action: trace.ActionDigest}), "Show what nodloop prepared since your last decision"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)

			got, err := d.iv.Describe(ctx, tc.args.request(t, d))

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// What a resume, a reverse and a knob of every name say, and a refused request names no form
func TestIntervenerDescribeEarlier(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	paused, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionPause, Target: "add"}, person)
	require.NoError(t, err)
	value := func(v float64) *float64 { return &v }
	tcs := []struct {
		name string
		args curate.Request
		want string
	}{
		{"resume", curate.Request{Action: trace.ActionResume, Target: paused.Trace.ID},
			"End the pause you set, so nodloop carries out again only what you had opened"},
		{"reverse", curate.Request{Action: trace.ActionReverse, Target: paused.Trace.ID},
			"Take back your earlier decision: Pause what nodloop carries out for the kind add, so every such change is asked of you"},
		{"a tier 0 audit share", curate.Request{Action: trace.ActionKnob, Target: "audit_tier0", Value: value(0.1)},
			"Set the share of rule decisions checked with you to 0.1 from the next cycle"},
		{"the item hold", curate.Request{Action: trace.ActionKnob, Target: "item_hold", Value: value(0.2)},
			"Set the share of sessions that go without each lesson nodloop approved to 0.2 from the next cycle"},
		{"the weeks of a cycle", curate.Request{Action: trace.ActionKnob, Target: "cycle_weeks", Value: value(3)},
			"Set the weeks of one cycle to 3 from the next cycle"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.iv.Describe(ctx, tc.args)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	_, err = d.iv.Describe(ctx, curate.Request{Action: trace.ActionReverse, Target: "nothing"})
	assert.ErrorIs(t, err, curate.ErrTargetInvalid)
}

// The stores fail an intervention before anything is recorded and an append that fails is the error of the call
func TestIntervenerFails(t *testing.T) {
	ctx := context.Background()
	type args struct {
		lists, appends int
		by             curate.Submitter
		request        curate.Request
	}
	keep := curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}
	tcs := []struct {
		name string
		args args
	}{
		{"records that cannot be read", args{0, -1, person, keep}},
		{"an intervention that cannot be appended", args{-1, 0, person, keep}},
		{"a proposal that cannot be appended", args{-1, 0, agent, keep}},
		{"a decide of a proposal that cannot be appended", args{-1, 1, agent, keep}},
		{"an answer that cannot be appended", args{-1, 0, person, curate.Request{Action: trace.ActionAnswer, Choice: trace.ChoiceDrop}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			r := tc.args.request
			if r.Action == trace.ActionAnswer {
				r.Target = d.decideOf(t, "k-ask")
			}
			traces := &testkit.FlakyTraces{Store: d.stores.Traces, Appends: failing(tc.args.appends), Lists: failing(tc.args.lists)}
			iv := curate.NewIntervener(d.stores.Ledger, traces, curate.InterveneSetting{Marks: curate.NewMarker(d.stores.Dir)}, d.stores.Clock.Now)

			_, err := iv.Intervene(ctx, r, tc.args.by)

			assert.ErrorIs(t, err, assert.AnError)
		})
	}
}
