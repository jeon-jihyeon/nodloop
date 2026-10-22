package curate_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

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

// An agent's proposal changes nothing and its question is due at once in the session that made it
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
	again, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: proposed.Decide, Choice: trace.ChoiceYes}, person)
	require.NoError(t, err)
	assert.NotEmpty(t, again.Wrote, "a yes again carries the proposal out again and takes nothing the reverse took back")
}

// A proposal waits for the next pass where curate.json is not at hand, names no cycle in force or another pass holds it
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
		{"a cycle that ended with no pass since", args{func(_ *testing.T, d desk) curate.Marks {
			d.stores.Clock.Advance(14 * 24 * time.Hour)
			return curate.NewMarker(d.stores.Dir)
		}}},
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

// A digest changes nothing, needs no proof, is never a proposal and answers the cursor since the person's last decision on record
func TestIntervenerDigest(t *testing.T) {
	ctx := context.Background()
	kept := func(t *testing.T, d desk) string {
		kept, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}, person)
		require.NoError(t, err)
		return kept.Trace.ID
	}
	type args struct {
		by curate.Submitter
		// The person's judgments before the digest returning the id of the last one and empty for none
		judged func(t *testing.T, d desk) string
	}
	tcs := []struct {
		name string
		args args
		// The intervention the digest recorded
		want string
	}{
		{"a digest before any intervention starts after the lesson the person approved", args{agent, func(*testing.T, desk) string { return "k-person approved" }}, "digest manual agent none"},
		{"a digest of an agent after a judgment starts after it", args{agent, kept}, "digest manual agent none"},
		{"a digest of a person names the person", args{person, kept}, "digest manual person key ann"},
		{"a digest after an approval at a terminal starts after its ledger record", args{person, func(t *testing.T, d desk) string {
			_ = kept(t, d)
			k, err := d.stores.Ledger.Approve(ctx, "k-ask", 1, "ann")
			require.NoError(t, err)
			return k.ID + " " + string(k.Status)
		}}, "digest manual person key ann"},
		{"a digest after a decision of nodloop starts after the person's", args{person, func(t *testing.T, d desk) string {
			id := kept(t, d)
			_, err := d.stores.Ledger.Withdraw(ctx, "k-carried", 1, knowledge.ReasonConflict, "d-later")
			require.NoError(t, err)
			return id
		}}, "digest manual person key ann"},
		{"a digest after a reply of the person starts after the answer it recorded", args{person, func(t *testing.T, d desk) string {
			_ = kept(t, d)
			d.expose(t, d.decideOf(t, "k-ask"))
			// The expose holds the HMAC of zero bytes as every line the click checks
			shown := func(string) string { return hex.EncodeToString(make([]byte, 32)) }
			replied, err := d.iv.Reply(ctx, curate.Reply{Session: "s-1", Key: "ab12", Seal: shown, Word: "drop", Person: "ann"})
			require.NoError(t, err)
			return replied.Answer
		}}, "digest manual person key ann"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			judged := tc.args.judged(t, d)
			before := d.received(t)

			got, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionDigest, Target: "session"}, tc.args.by)

			require.NoError(t, err)
			assert.Equal(t, judged, got.Since)
			assert.Empty(t, got.Decide)
			assert.Equal(t, before, d.received(t))
			recorded := d.interventions(t)
			assert.Equal(t, tc.want, recorded[len(recorded)-1])
		})
	}
}

// A digest whose last decision is a ledger record answers no cursor where no cursor of a ledger record is at hand
func TestIntervenerDigestNoCursor(t *testing.T) {
	d := newDesk(t)
	iv := curate.NewIntervener(d.stores.Ledger, d.stores.Traces, curate.InterveneSetting{
		Cursor: func(_ time.Time, id string) string { return id },
	}, d.stores.Clock.Now)

	got, err := iv.Intervene(context.Background(), curate.Request{Action: trace.ActionDigest, Target: "session"}, agent)

	require.NoError(t, err)
	assert.Empty(t, got.Since)
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
		{"a judgment through a decide whose version the records lack", args{func(t *testing.T, d desk) curate.Request {
			decide := d.asking(t, "r-k-add", curate.Change{Kind: curate.ChangeAdd, ID: "k-gone", Version: 1, Anchor: 1}, curate.RuleScopeWider)
			return curate.Request{Action: trace.ActionJudge, Target: decide, Choice: trace.ChoiceKeep}
		}, person}, knowledge.ErrNotFound},
		{"a judgment through a decide of an offer", args{func(t *testing.T, d desk) curate.Request {
			decide := d.asking(t, "", curate.Change{Kind: curate.ChangeActivate, Anchor: 1}, "")
			return curate.Request{Action: trace.ActionJudge, Target: decide, Choice: trace.ChoiceKeep}
		}, person}, curate.ErrTargetInvalid},
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
		{"a retry by another person of an answer whose intervention failed to append", args{func(t *testing.T, d desk) curate.Request {
			r := curate.Request{Action: trace.ActionAnswer, Target: d.decideOf(t, "k-ask"), Choice: trace.ChoiceKeep}
			require.ErrorIs(t, testkit.Err(d.failingAt(t, -1, 1).Intervene(ctx, r, person)), assert.AnError)
			return r
		}, curate.Submitter{Proof: trace.ProofKey, Person: "person", Source: trace.SourceManual}}, curate.ErrTargetInvalid},
		{"a dismiss of a question with a choice", args{func(t *testing.T, d desk) curate.Request {
			return curate.Request{Action: trace.ActionDismiss, Target: d.decideOf(t, "k-ask"), Choice: trace.ChoiceKeep}
		}, person}, trace.ErrInterventionInvalid},
		{"a pause of a kind tier 1 never carries out", args{fixed(curate.Request{Action: trace.ActionPause, Target: "withdraw"}), person},
			curate.ErrTargetInvalid},
		{"a pause of a label with no value", args{fixed(curate.Request{Action: trace.ActionPause, Labels: trace.Labels{"repo": {}}}), person},
			trace.ErrLabelEmpty},
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
		{"a digest under the reserved name", args{fixed(curate.Request{Action: trace.ActionDigest}),
			curate.Submitter{Proof: trace.ProofKey, Person: knowledge.ApproverNodloop, Source: trace.SourceManual}}, knowledge.ErrApproverReserved},
		{"a digest of a person who names no one", args{fixed(curate.Request{Action: trace.ActionDigest}),
			curate.Submitter{Proof: trace.ProofKey, Source: trace.SourceManual}}, knowledge.ErrApproverRequired},
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

// A reverse of a judgment whose version changed since or of a knob a later setting changed is refused
// so the person decides that change apart
func TestIntervenerReverseStale(t *testing.T) {
	ctx := context.Background()
	value := func(v float64) *float64 { return &v }
	type args struct {
		// The intervention the reverse names and the later one that changed what it wrote
		first, later curate.Request
	}
	tcs := []struct {
		name string
		args args
	}{
		{"a judgment whose version changed since", args{
			curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep},
			curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceUndo},
		}},
		{"a knob a later setting changed", args{
			curate.Request{Action: trace.ActionKnob, Target: "item_hold", Value: value(0.2)},
			curate.Request{Action: trace.ActionKnob, Target: "item_hold", Value: value(0.15)},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			first, err := d.iv.Intervene(ctx, tc.args.first, person)
			require.NoError(t, err)
			_, err = d.iv.Intervene(ctx, tc.args.later, person)
			require.NoError(t, err)

			_, err = d.iv.Intervene(ctx, curate.Request{Action: trace.ActionReverse, Target: first.Trace.ID}, person)

			assert.ErrorIs(t, err, knowledge.ErrReverseStale)
		})
	}
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
// A compaction that leaves no room for the change is refused before the first write so no compaction stands without its answer
func TestIntervenerMerge(t *testing.T) {
	ctx := context.Background()
	candidate, approved := knowledge.StatusCandidate, knowledge.StatusApproved
	big := "a long lesson for the place of the big repo, "
	for len(big) < 4200 {
		big += big
	}
	type want struct {
		err error
		// The records the merge wrote and the status of the change after it
		wrote  int
		change string
	}
	tcs := []struct {
		name string
		// The text the prepared compaction merges the two old items into
		content string
		want    want
	}{
		{"a compaction that makes room approves the change after it", "first and second " + big[:200], want{nil, 4, "k-more v1 approved ann"}},
		{"a compaction that leaves the folder full writes nothing", "first and second " + big, want{knowledge.ErrFolderFull, 0, "k-more v1 candidate"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
				lesson("k-big1", 1, "first "+big, acme("big"), approved, "ann"), lesson("k-big2", 1, "second "+big, acme("big"), approved, "ann"),
				lesson("k-more", 1, "third "+big, acme("big"), candidate, ""),
			}, nil)
			first, err := b.curator().Pass(ctx, b.setting(t))
			require.NoError(t, err)
			require.Equal(t, []string{"add k-more v1 hold room 2"}, decisionsOf(first.Decided))
			iv := b.intervener(nil)
			merge := curate.Request{Action: trace.ActionAnswer, Target: first.Decided[0].ID, Choice: trace.ChoiceMerge}
			_, err = iv.Intervene(ctx, merge, person)
			require.ErrorIs(t, err, curate.ErrTargetInvalid, "no compaction was prepared yet")
			_, err = b.stores.Ledger.ProposeCompaction(ctx, "k-big1", []knowledge.Knowledge{{
				Kind: knowledge.KindMeaning, Content: tc.content, Run: acme("big"), Author: "claude",
				Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-big1", Version: 1}, {ID: "k-big2", Version: 1}}},
			}})
			require.NoError(t, err)
			before, err := b.stores.Ledger.All(ctx)
			require.NoError(t, err)

			got, err := iv.Intervene(ctx, merge, person)

			assert.ErrorIs(t, err, tc.want.err)
			after, aerr := b.stores.Ledger.All(ctx)
			require.NoError(t, aerr)
			assert.Equal(t, tc.want.wrote, len(got.Wrote))
			assert.Equal(t, tc.want.wrote, len(after)-len(before))
			assert.Contains(t, statuses(t, b), tc.want.change)
			assert.Empty(t, b.orphans(t))
		})
	}
}

// A compaction judged keep through its id approves it with the coverage compact keeps and needs that check at hand
func TestIntervenerCompaction(t *testing.T) {
	ctx := context.Background()
	approved := knowledge.StatusApproved
	keep := curate.Request{Action: trace.ActionJudge, Target: "c-generated", Choice: trace.ChoiceKeep}
	type args struct {
		coverage bool
		// The judgments of the person before the one under test
		before []curate.Request
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
		{"with the coverage compact keeps", args{true, nil}, want{nil, 4}},
		{"without a coverage check at hand", args{false, nil}, want{curate.ErrTargetInvalid, 0}},
		{"approved already names no record it did not write", args{true, []curate.Request{keep}}, want{nil, 0}},
		{"an old item retired since", args{true, []curate.Request{{Action: trace.ActionJudge, Target: "k-b:1", Choice: trace.ChoiceUndo}}},
			want{knowledge.ErrCompactionOutdated, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, []knowledge.Knowledge{
				lesson("k-a", 1, "rule a", acme("acme"), approved, "ann"), lesson("k-b", 1, "rule b", acme("acme"), approved, "ann"),
			}, nil)
			_, err := b.stores.Ledger.ProposeCompaction(ctx, "k-a", []knowledge.Knowledge{{
				Kind: knowledge.KindMeaning, Content: "rule a and rule b", Run: acme("acme"), Author: "claude",
				Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-a", Version: 1}, {ID: "k-b", Version: 1}, {ID: "k-person", Version: 1}}},
			}})
			require.NoError(t, err)
			setting := curate.InterveneSetting{Coverage: b.coverage}
			if !tc.args.coverage {
				setting.Coverage = nil
			}
			iv := curate.NewIntervener(b.stores.Ledger, b.stores.Traces, setting, b.stores.Clock.Now)
			for _, r := range tc.args.before {
				_, err := iv.Intervene(ctx, r, person)
				require.NoError(t, err)
			}

			got, err := iv.Intervene(ctx, keep, person)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, got.Wrote, tc.want.wrote)
		})
	}
}

// The form names the action and its target in plain words with no id or version
// and a request the records cannot carry out or cannot read names no form
func TestIntervenerDescribe(t *testing.T) {
	ctx := context.Background()
	value := func(v float64) *float64 { return &v }
	type args struct {
		request func(t *testing.T, d desk) curate.Request
		// Lists the traces answer before failing and -1 for none
		lists int
	}
	fixed := func(r curate.Request) args { return args{func(*testing.T, desk) curate.Request { return r }, -1} }
	asked := func(c curate.Change, choice trace.Choice) args {
		return args{func(t *testing.T, d desk) curate.Request {
			return curate.Request{Action: trace.ActionAnswer, Target: d.asking(t, "r-k-add", c, ""), Choice: choice}
		}, -1}
	}
	paused := func(action trace.Action) args {
		return args{func(t *testing.T, d desk) curate.Request {
			paused, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionPause, Target: "add"}, person)
			require.NoError(t, err)
			return curate.Request{Action: action, Target: paused.Trace.ID}
		}, -1}
	}
	type want struct {
		form string
		err  error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"keep", fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}), want{`Keep the lesson "Name the failing test first"`, nil}},
		{"undo", fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceUndo}), want{`Undo the lesson "Name the failing test first"`, nil}},
		{"scope", fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceScope, Labels: trace.Labels{"repo": {"acme"}}}),
			want{`Keep the lesson "Name the failing test first" for repo acme`, nil}},
		{"scope everywhere", fixed(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceScope}),
			want{`Keep the lesson "Name the failing test first" for every place`, nil}},
		{"answer", args{func(t *testing.T, d desk) curate.Request {
			return curate.Request{Action: trace.ActionAnswer, Target: d.decideOf(t, "k-ask"), Choice: trace.ChoiceDrop}
		}, -1}, want{`Answer drop to the question about the lesson "Use the staging cluster"`, nil}},
		{"an answer about an offer", asked(curate.Change{Kind: curate.ChangeActivate, Anchor: 1}, trace.ChoiceYes),
			want{"Answer yes to the question about a change nodloop prepared", nil}},
		{"an answer about a compaction", asked(curate.Change{Kind: curate.ChangeCompaction, Compaction: "c-1", Anchor: 1}, trace.ChoiceMerge),
			want{"Answer merge to the question about the merge of lessons that was prepared", nil}},
		{"dismiss", args{func(t *testing.T, d desk) curate.Request {
			return curate.Request{Action: trace.ActionDismiss, Target: d.decideOf(t, "k-ask")}
		}, -1}, want{`Close the question about the lesson "Use the staging cluster" with no decision, so nodloop never carries it out`, nil}},
		{"pause", fixed(curate.Request{Action: trace.ActionPause, Target: "add", Labels: trace.Labels{"repo": {"svc", "acme"}}}),
			want{"Pause what nodloop carries out for the kind add in repo acme or svc, so every such change is asked of you", nil}},
		{"resume", paused(trace.ActionResume), want{"End the pause you set, so nodloop carries out again only what you had opened", nil}},
		{"reverse", paused(trace.ActionReverse),
			want{"Take back your earlier decision: Pause what nodloop carries out for the kind add, so every such change is asked of you", nil}},
		{"a tier 1 audit share", fixed(curate.Request{Action: trace.ActionKnob, Target: "audit_tier1", Value: value(0.3)}),
			want{"Set the share of judged decisions checked with you to 0.3 from the next cycle", nil}},
		{"a tier 0 audit share", fixed(curate.Request{Action: trace.ActionKnob, Target: "audit_tier0", Value: value(0.1)}),
			want{"Set the share of rule decisions checked with you to 0.1 from the next cycle", nil}},
		{"the item hold", fixed(curate.Request{Action: trace.ActionKnob, Target: "item_hold", Value: value(0.2)}),
			want{"Set the share of sessions that go without each lesson nodloop approved to 0.2 from the next cycle", nil}},
		{"the weeks of a cycle", fixed(curate.Request{Action: trace.ActionKnob, Target: "cycle_weeks", Value: value(3)}),
			want{"Set the weeks of one cycle to 3 from the next cycle", nil}},
		{"reanchor", fixed(curate.Request{Action: trace.ActionReanchor, Target: "session", Labels: trace.Labels{"repo": {"work"}}}),
			want{"Take the lessons nodloop approved for runs of session. repo=work as your own", nil}},
		{"digest", fixed(curate.Request{Action: trace.ActionDigest}), want{"Show what nodloop prepared since your last decision", nil}},
		{"a reverse of no intervention", fixed(curate.Request{Action: trace.ActionReverse, Target: "nothing"}), want{"", curate.ErrTargetInvalid}},
		{"records that cannot be read", args{func(*testing.T, desk) curate.Request { return curate.Request{Action: trace.ActionDigest} }, 0},
			want{"", assert.AnError}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			r := tc.args.request(t, d)
			iv := curate.NewIntervener(d.stores.Ledger, &testkit.FlakyTraces{Store: d.stores.Traces, Lists: failing(tc.args.lists)}, curate.InterveneSetting{}, d.stores.Clock.Now)

			got, err := iv.Describe(ctx, r)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.form, got)
		})
	}
}

// The stores fail an intervention before anything is recorded and an append that fails is the error of the call
func TestIntervenerFails(t *testing.T) {
	ctx := context.Background()
	// The lists every intervention makes before it carries anything out
	const read = 6
	type args struct {
		lists, appends int
		by             curate.Submitter
		request        func(t *testing.T, d desk) curate.Request
	}
	keep := func(*testing.T, desk) curate.Request {
		return curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}
	}
	answering := func(choice trace.Choice, rule curate.RuleCode) func(*testing.T, desk) curate.Request {
		return func(t *testing.T, d desk) curate.Request {
			decide := d.asking(t, "r-k-carried", curate.Change{Kind: curate.ChangeAdd, ID: "k-ask", Version: 1, Anchor: 1}, rule)
			return curate.Request{Action: trace.ActionAnswer, Target: decide, Choice: choice}
		}
	}
	tcs := []struct {
		name string
		args args
	}{
		{"records that cannot be read", args{0, -1, person, keep}},
		{"an intervention that cannot be appended", args{-1, 0, person, keep}},
		{"a proposal that cannot be appended", args{-1, 0, agent, keep}},
		{"a decide of a proposal that cannot be appended", args{-1, 1, agent, keep}},
		{"an answer that cannot be appended", args{-1, 0, person, answering(trace.ChoiceDrop, curate.RuleScopeWider)}},
		{"runs that cannot be read for a scope", args{read, -1, person, func(*testing.T, desk) curate.Request {
			return curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceScope, Labels: trace.Labels{"repo": {"acme"}}}
		}}},
		{"the run of a mine that cannot be read", args{read, -1, person, answering(trace.ChoiceMine, curate.RuleDrift)}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			r := tc.args.request(t, d)
			traces := &testkit.FlakyTraces{Store: d.stores.Traces, Appends: failing(tc.args.appends), Lists: failing(tc.args.lists)}
			iv := curate.NewIntervener(d.stores.Ledger, traces, curate.InterveneSetting{Marks: curate.NewMarker(d.stores.Dir)}, d.stores.Clock.Now)

			_, err := iv.Intervene(ctx, r, tc.args.by)

			assert.ErrorIs(t, err, assert.AnError)
		})
	}
}

// Each judgment writes its record under the person's name as the status of its version allows
func TestIntervenerJudge(t *testing.T) {
	ctx := context.Background()
	tcs := []struct {
		name string
		args curate.Request
		// The records written as id, version, status and approver
		want []string
	}{
		{"keep approves a waiting candidate", curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}, []string{"k-add v1 approved ann"}},
		{"keep reaffirms an approved version", curate.Request{Action: trace.ActionJudge, Target: "k-person:1", Choice: trace.ChoiceKeep},
			[]string{"k-person v1 approved ann"}},
		{"keep restores a retired version as its next version", curate.Request{Action: trace.ActionJudge, Target: "k-dup:1", Choice: trace.ChoiceKeep},
			[]string{"k-dup v2 approved ann"}},
		{"undo retires an approved version", curate.Request{Action: trace.ActionJudge, Target: "k-carried:1", Choice: trace.ChoiceUndo},
			[]string{"k-carried v1 retired ann"}},
		{"undo drops a waiting candidate", curate.Request{Action: trace.ActionJudge, Target: "k-ask:1", Choice: trace.ChoiceUndo}, []string{"k-ask v1 retired ann"}},
		{"scope approves a candidate in the labels picked", curate.Request{
			Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceScope, Labels: trace.Labels{"repo": {"acme"}},
		}, []string{"k-add v1 approved ann"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)

			got, err := d.iv.Intervene(ctx, tc.args, person)

			require.NoError(t, err)
			wrote := []string{}
			for _, k := range got.Wrote {
				wrote = append(wrote, fmt.Sprintf("%s v%d %s %s", k.ID, k.Version, k.Status, k.Approver))
			}
			assert.Equal(t, tc.want, wrote)
		})
	}
}

// A knob event of the open cycle that names no seed and no policy version fails what reads it at once
// and the intervention stays recorded
func TestIntervenerBrokenKnobEvent(t *testing.T) {
	ctx := context.Background()
	value := 0.3
	type args struct {
		request curate.Request
		by      curate.Submitter
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a raise of an audit share at once", args{curate.Request{Action: trace.ActionKnob, Target: "audit_tier1", Value: &value}, person}, curate.ErrKnobInvalid},
		{"the decide of a proposal", args{curate.Request{Action: trace.ActionPause, Target: "add", Session: "s-9"}, agent}, curate.ErrDecideInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, nil, nil)
			first, err := b.curator().Pass(ctx, b.setting(t))
			require.NoError(t, err)
			marker := curate.NewMarker(b.stores.Dir)
			held, err := marker.Hold()
			require.NoError(t, err)
			require.NoError(t, held.Write(first.Mark(written)))
			require.NoError(t, held.Release())
			broken := first.Event
			broken.Seed, broken.PolicyVersion = "", ""
			input, err := json.Marshal(broken)
			require.NoError(t, err)
			now := b.stores.Clock.Now()
			require.NoError(t, b.stores.Traces.Append(ctx, trace.Trace{ID: trace.NewID(now), Name: trace.NameKnob, Subject: broken.CycleID, Time: now, Input: input}.Record()))

			_, err = b.intervener(marker).Intervene(ctx, tc.args.request, tc.args.by)

			assert.ErrorIs(t, err, tc.want)
			assert.Len(t, b.interventions(t), 1)
		})
	}
}
