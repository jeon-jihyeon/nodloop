package curate_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A person under a key that proves her
var person = curate.Submitter{Proof: trace.ProofKey, Person: "ann", Source: trace.SourceManual}

// A model's call that proves no person
var agent = curate.Submitter{Proof: trace.ProofNone, Source: trace.SourceManual}

// The bench of the write side after a first pass that left a question about each waiting version
// 1. k-add waits in acme and the judge finds it conform so it waits in shadow
// 2. k-ask waits in svc where no lesson a person approved reaches so it asks the person
// 3. k-dup says what the person's lesson says so tier 0 dropped it and the audit at p 1 drew it
// 4. k-carried is a version nodloop approved in repo work
type desk struct {
	bench
	iv    *curate.Intervener
	first curate.Result
}

func newDesk(t *testing.T) desk {
	t.Helper()
	ctx := context.Background()
	candidate, approved := knowledge.StatusCandidate, knowledge.StatusApproved
	b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
		lesson("k-add", 1, "Name the failing test first", acme("acme"), candidate, ""),
		lesson("k-ask", 1, "Use the staging cluster", acme("svc"), candidate, ""),
		lesson("k-dup", 1, "Run the tests before a commit", acme("acme"), candidate, ""),
		lesson("k-carried", 1, "Squash before a merge", acme("work"), approved, knowledge.ApproverNodloop),
	}, nil)
	b.knobs(t, func(k *curate.Knobs) { k.Audit = curate.Audit{Tier0: 1, Tier1: 1} })
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	marker := curate.NewMarker(b.stores.Dir)
	held, err := marker.Hold()
	require.NoError(t, err)
	require.NoError(t, held.Write(first.Mark(written)))
	require.NoError(t, held.Release())
	return desk{bench: b, iv: b.intervener(marker), first: first}
}

// The intervener of the bench with curate.json at hand and a cursor that is the id of the record it starts after
func (b bench) intervener(marks curate.Marks) *curate.Intervener {
	return curate.NewIntervener(b.stores.Ledger, b.stores.Traces, curate.InterveneSetting{
		Build: "0.10.0", Coverage: b.coverage, Marks: marks, Cursor: func(_ time.Time, id string) string { return id },
	}, b.stores.Clock.Now)
}

// A coverage check that finds every old item of the compaction covered by its new items
func (b bench) coverage(ctx context.Context, id string) (knowledge.Coverage, error) {
	c, err := b.stores.Ledger.Compaction(ctx, id)
	if err != nil {
		return knowledge.Coverage{}, err
	}
	by := []knowledge.Ref{}
	for _, k := range c.Items {
		by = append(by, knowledge.Ref{ID: k.ID, Version: k.Version})
	}
	cv := knowledge.Coverage{Compaction: id}
	for _, old := range c.Replaced {
		cv.Items = append(cv.Items, knowledge.CoverageItem{Old: knowledge.Ref{ID: old.ID, Version: old.Version}, CoveredBy: by})
	}
	return cv, nil
}

// The decide of the first pass about the version
func (d desk) decideOf(t *testing.T, id string) string {
	t.Helper()
	i := slices.IndexFunc(d.first.Decided, func(x curate.Decided) bool { return x.Change.ID == id })
	require.GreaterOrEqual(t, i, 0, id)
	return d.first.Decided[i].ID
}

// Appends a decide of the cycle of the first pass that asks the person about the change for the rule
func (d desk) asking(t *testing.T, ref string, c curate.Change, rule curate.RuleCode) string {
	t.Helper()
	now := d.stores.Clock.Now()
	id := trace.NewID(now)
	tr, err := curate.Event{ID: id, Ref: ref, Source: trace.SourcePush, Draw: d.first.Event.AuditDraw(id, 0)}.
		Decide(c, curate.Decision{Tier: 2, RuleCode: rule, Action: curate.ActionAsk}, nil, now)
	require.NoError(t, err)
	require.NoError(t, d.stores.Traces.Append(context.Background(), tr.Record()))
	return id
}

// Appends a decide of the cycle of the first pass that carried out the change in tier 1 with its audit drawn
func (d desk) audited(t *testing.T, ref string, c curate.Change) string {
	t.Helper()
	now := d.stores.Clock.Now()
	id := trace.NewID(now)
	tr, err := curate.Event{ID: id, Ref: ref, Source: trace.SourcePush, Draw: d.first.Event.AuditDraw(id, 1)}.
		Decide(c, curate.Decision{Tier: 1, Outcome: curate.OutcomeConform, Bin: curate.Bin1, Action: curate.ActionApply}, nil, now)
	require.NoError(t, err)
	require.NoError(t, d.stores.Traces.Append(context.Background(), tr.Record()))
	return id
}

// Appends the expose that showed the question of the decide in session s-1 under the key ab12 and returns its id
func (d desk) expose(t *testing.T, decide string) string {
	t.Helper()
	shown := curate.Event{Session: "s-1", Ref: decide, Source: trace.SourcePush, Draw: d.first.Event.AuditDraw("s-1", 1)}
	x, err := shown.Expose(curate.Exposure{
		Kind: curate.QuestionDirection, Key: "ab12", Surface: curate.SurfaceContext, Rules: curate.QuestionRules,
		Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Text: hex.EncodeToString(make([]byte, 32)), Round: 1,
	}, d.stores.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, d.stores.Traces.Append(context.Background(), x.Record()))
	return x.ID
}

// The fold of the interventions, answers and decides of the records
func (b bench) folded(t *testing.T) curate.Interventions {
	t.Helper()
	var all trace.Traces
	for _, name := range []trace.Name{trace.NameIntervention, trace.NameAnswer, trace.NameDecide} {
		found, err := b.stores.Traces.List(context.Background(), record.TraceFilter{Name: string(name)})
		require.NoError(t, err)
		all = append(all, trace.RestoreAll(found)...)
	}
	return curate.RestoreInterventions(all)
}

// What runs receive and what waits by id, content and scope with every version number left out
func (b bench) received(t *testing.T) []string {
	t.Helper()
	all, err := b.stores.Ledger.All(context.Background())
	require.NoError(t, err)
	out := []string{}
	for _, k := range all.Approved() {
		out = append(out, fmt.Sprintf("approved %s %q %s", k.ID, k.Content, k.Run))
	}
	for _, k := range all.AllWaiting() {
		out = append(out, fmt.Sprintf("waiting %s %q %s", k.ID, k.Content, k.Run))
	}
	slices.Sort(out)
	return out
}

// Everything an intervention may change
type deskState struct {
	Received []string
	// The knobs the next cycle starts with
	Knobs     curate.Knobs
	Paused    bool
	Dismissed bool
	// Whether the question about k-ask is open
	Open  bool
	Taken int
}

func (d desk) state(t *testing.T) deskState {
	t.Helper()
	ints := d.folded(t)
	ask := d.decideOf(t, "k-ask")
	_, dismissed := ints.Dismissed(ask)
	_, err := d.iv.Describe(context.Background(), curate.Request{Action: trace.ActionAnswer, Target: ask, Choice: trace.ChoiceKeep})
	return deskState{
		Received: d.received(t), Knobs: ints.Knobs(d.first.Event.Knobs, time.Time{}), Paused: ints.Paused(curate.ChangeAdd, *acme("acme")),
		Dismissed: dismissed, Open: err == nil, Taken: len(ints.Reanchored()),
	}
}

// The authority, the proof and the person of every intervention recorded oldest first
func (b bench) interventions(t *testing.T) []string {
	t.Helper()
	found, err := b.stores.Traces.List(context.Background(), record.TraceFilter{Name: string(trace.NameIntervention)})
	require.NoError(t, err)
	out := []string{}
	for _, tr := range slices.Backward(found) {
		var in trace.Intervention
		require.NoError(t, json.Unmarshal(tr.Input, &in))
		out = append(out, strings.TrimSpace(fmt.Sprintf("%s %s %s %s %s", in.Action, tr.Source, in.Authority, in.Proof, in.Person)))
	}
	return out
}

// Every action of the write side and its reverse return the ledger and the knobs of the next cycle to where they were
// and both interventions are recorded
func TestIntervenerReverse(t *testing.T) {
	ctx := context.Background()
	value := func(v float64) *float64 { return &v }
	type args struct {
		// Interventions of the person before the one under test that answer the id the request takes
		setup   func(t *testing.T, d desk) string
		request func(t *testing.T, d desk, prior string) curate.Request
	}
	judge := func(target string, choice trace.Choice) func(*testing.T, desk, string) curate.Request {
		return func(*testing.T, desk, string) curate.Request {
			return curate.Request{Action: trace.ActionJudge, Target: target, Choice: choice}
		}
	}
	answering := func(id string, choice trace.Choice) func(*testing.T, desk, string) curate.Request {
		return func(t *testing.T, d desk, _ string) curate.Request {
			return curate.Request{Action: trace.ActionAnswer, Target: d.decideOf(t, id), Choice: choice}
		}
	}
	knob := func(name string, v float64) func(*testing.T, desk, string) curate.Request {
		return func(*testing.T, desk, string) curate.Request {
			return curate.Request{Action: trace.ActionKnob, Target: name, Value: value(v)}
		}
	}
	prior := func(action trace.Action) func(*testing.T, desk, string) curate.Request {
		return func(_ *testing.T, _ desk, prior string) curate.Request {
			return curate.Request{Action: action, Target: prior}
		}
	}
	offered := func(c curate.Change) func(*testing.T, desk) string {
		return func(t *testing.T, d desk) string { return d.asking(t, "", c, "") }
	}
	answerPrior := func(choice trace.Choice) func(*testing.T, desk, string) curate.Request {
		return func(_ *testing.T, _ desk, prior string) curate.Request {
			return curate.Request{Action: trace.ActionAnswer, Target: prior, Choice: choice}
		}
	}
	tcs := []struct {
		name string
		args args
	}{
		{"keep approves a waiting candidate", args{request: judge("k-add:1", trace.ChoiceKeep)}},
		{"undo retires a lesson a person approved", args{request: judge("k-person:1", trace.ChoiceUndo)}},
		{"undo drops a waiting candidate", args{request: judge("k-ask:1", trace.ChoiceUndo)}},
		{"undo retires a lesson nodloop approved", args{request: judge("k-carried:1", trace.ChoiceUndo)}},
		{"keep restores a dropped version as its next version", args{request: judge("k-dup:1", trace.ChoiceKeep)}},
		{"scope approves a candidate in the labels picked", args{request: func(*testing.T, desk, string) curate.Request {
			return curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceScope, Labels: trace.Labels{"repo": {"acme"}}}
		}}},
		{"keep through the decide of a question approves its version", args{request: func(t *testing.T, d desk, _ string) curate.Request {
			return curate.Request{Action: trace.ActionJudge, Target: d.decideOf(t, "k-ask"), Choice: trace.ChoiceKeep}
		}}},
		{"answer keep approves the version of a direction question", args{request: answering("k-ask", trace.ChoiceKeep)}},
		{"answer drop retires the version of a direction question", args{request: answering("k-ask", trace.ChoiceDrop)}},
		{"answer keep of a shadow question approves its version", args{request: answering("k-add", trace.ChoiceKeep)}},
		{"answer undo of an audit restores what tier 0 dropped", args{request: answering("k-dup", trace.ChoiceUndo)}},
		{"answer undo of an audit retires what tier 1 approved", args{
			setup: func(t *testing.T, d desk) string {
				return d.audited(t, "r-k-carried", curate.Change{Kind: curate.ChangeAdd, ID: "k-carried", Version: 1, Anchor: 1})
			},
			request: answerPrior(trace.ChoiceUndo),
		}},
		{"answer mine takes the versions nodloop approved in the place", args{
			setup: func(t *testing.T, d desk) string {
				return d.asking(t, "r-k-carried", curate.Change{Kind: curate.ChangeAdd, ID: "k-add", Version: 1, Anchor: 1}, curate.RuleDrift)
			},
			request: answerPrior(trace.ChoiceMine),
		}},
		{"answer yes to a widening opens the kind and bin from the next cycle", args{
			setup:   offered(curate.Change{Kind: curate.ChangeWiden, Opens: &curate.Opening{Kind: curate.ChangeUpdate, Bin: curate.Bin1}, Anchor: 1}),
			request: answerPrior(trace.ChoiceYes),
		}},
		{"answer yes to the activation ends the shadow from the next cycle", args{
			setup:   offered(curate.Change{Kind: curate.ChangeActivate, Anchor: 1}),
			request: answerPrior(trace.ChoiceYes),
		}},
		{"dismiss closes the question", args{request: func(t *testing.T, d desk, _ string) curate.Request {
			return curate.Request{Action: trace.ActionDismiss, Target: d.decideOf(t, "k-ask")}
		}}},
		{"pause of a kind", args{request: func(*testing.T, desk, string) curate.Request {
			return curate.Request{Action: trace.ActionPause, Target: string(curate.ChangeAdd)}
		}}},
		{"pause of a scope", args{request: func(*testing.T, desk, string) curate.Request {
			return curate.Request{Action: trace.ActionPause, Labels: trace.Labels{"repo": {"acme"}}}
		}}},
		{"resume ends a pause", args{
			setup: func(t *testing.T, d desk) string {
				in, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionPause, Target: string(curate.ChangeAdd)}, person)
				require.NoError(t, err)
				return in.Trace.ID
			},
			request: prior(trace.ActionResume),
		}},
		{"knob raises the tier 1 audit share", args{request: knob("audit_tier1", 0.3)}},
		{"knob raises the tier 0 audit share", args{request: knob("audit_tier0", 0.5)}},
		{"knob sets the item hold", args{request: knob("item_hold", 0.2)}},
		{"knob sets the weeks of a cycle", args{request: knob("cycle_weeks", 4)}},
		{"knob set again in the cycle returns to the setting before it", args{
			setup: func(t *testing.T, d desk) string {
				_, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionKnob, Target: "item_hold", Value: value(0.2)}, person)
				require.NoError(t, err)
				return ""
			},
			request: knob("item_hold", 0.15),
		}},
		{"reanchor takes the versions nodloop approved in a place", args{request: func(*testing.T, desk, string) curate.Request {
			return curate.Request{Action: trace.ActionReanchor, Target: "session", Labels: trace.Labels{"repo": {"work"}}}
		}}},
		{"a reverse of a reverse carries the judgment out again", args{
			setup: func(t *testing.T, d desk) string {
				kept, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}, person)
				require.NoError(t, err)
				back, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionReverse, Target: kept.Trace.ID}, person)
				require.NoError(t, err)
				return back.Trace.ID
			},
			request: prior(trace.ActionReverse),
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			prior := ""
			if tc.args.setup != nil {
				prior = tc.args.setup(t, d)
			}
			before, recorded := d.state(t), len(d.interventions(t))
			done, err := d.iv.Intervene(ctx, tc.args.request(t, d, prior), person)
			require.NoError(t, err)
			changed := d.state(t)

			back, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionReverse, Target: done.Trace.ID}, person)

			require.NoError(t, err)
			assert.NotEqual(t, before, changed, "the intervention changed something")
			assert.Equal(t, before, d.state(t))
			assert.Len(t, d.interventions(t), recorded+2)
			assert.Equal(t, []any{trace.SourceManual, trace.SourceManual, (*trace.Draw)(nil)}, []any{done.Trace.Source, back.Trace.Source, done.Trace.Draw})
		})
	}
}
