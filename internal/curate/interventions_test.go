package curate_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The knob events of the bench oldest first as cause and the tier 1 audit share they hold
func (b bench) knobEvents(t *testing.T) []string {
	t.Helper()
	found, err := b.stores.Traces.List(context.Background(), record.TraceFilter{Name: string(trace.NameKnob)})
	require.NoError(t, err)
	out := []string{}
	for _, tr := range slices.Backward(found) {
		ev, ok := curate.RestoreKnob(trace.Restore(tr))
		require.True(t, ok)
		out = append(out, string(ev.Cause)+" "+string(rune('0'+int(ev.Knobs.Audit.Tier1*10))))
	}
	return out
}

// A raise of an audit share takes effect at once in the cycle and a lower waits for the next cycle start
func TestIntervenerRaise(t *testing.T) {
	ctx := context.Background()
	value := func(v float64) *float64 { return &v }
	type args struct {
		value float64
		// Appends the knob trace may make before failing and -1 for none
		appends int
	}
	type want struct {
		events []string
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a raise appends a narrowing of the cycle", args{0.3, -1}, want{[]string{"start 1", "narrow 3"}, nil}},
		{"a lower appends nothing", args{0.05, -1}, want{[]string{"start 1"}, nil}},
		{"a raise whose narrowing cannot be appended fails after the intervention", args{0.3, 1}, want{[]string{"start 1"}, assert.AnError}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, nil, nil)
			_, err := b.curator().Pass(ctx, b.setting(t))
			require.NoError(t, err)
			traces := &testkit.FlakyTraces{Store: b.stores.Traces, Appends: failing(tc.args.appends)}
			iv := curate.NewIntervener(b.stores.Ledger, traces, curate.InterveneSetting{Build: "0.10.0"}, b.stores.Clock.Now)

			_, err = iv.Intervene(ctx, curate.Request{Action: trace.ActionKnob, Target: "audit_tier1", Value: value(tc.args.value)}, person)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.events, b.knobEvents(t))
			assert.Len(t, b.interventions(t), 1, "the intervention is recorded")
		})
	}
}

// The next cycle starts with the knobs the person set during the cycle before
func TestCuratorPassSettings(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, nil, nil)
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	iv := b.intervener(nil)
	value := func(v float64) *float64 { return &v }
	for _, r := range []curate.Request{
		{Action: trace.ActionKnob, Target: "item_hold", Value: value(0.2)}, {Action: trace.ActionKnob, Target: "cycle_weeks", Value: value(1)},
	} {
		_, err := iv.Intervene(ctx, r, person)
		require.NoError(t, err)
	}
	b.stores.Clock.Advance(first.Event.Start.AddDate(0, 0, 14).Sub(b.stores.Clock.Now()))

	next, err := b.curator().Pass(ctx, b.setting(t))

	require.NoError(t, err)
	assert.NotEqual(t, first.Event.CycleID, next.Event.CycleID)
	assert.Equal(t, []any{knowledge.ItemHold(0.2), 1}, []any{next.Event.Knobs.ItemHold, next.Event.Knobs.CycleWeeks})
}

// A pause sends a candidate it covers to the person with no judge call and a dismissed question stays closed without new evidence
func TestCuratorPassInterventions(t *testing.T) {
	ctx := context.Background()
	candidate := knowledge.StatusCandidate
	type args struct {
		// The intervention before the second pass given the decide of the first pass about k-add
		request func(decide string) curate.Request
		// Corroborates k-add with a new correction after the intervention
		evidence bool
	}
	type want struct {
		decided []string
		due     bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a pause of the kind sends a new candidate to the person",
			args{request: func(string) curate.Request {
				return curate.Request{Action: trace.ActionPause, Target: string(curate.ChangeAdd)}
			}},
			want{[]string{"add k-late v1 ask paused 2"}, true},
		},
		{
			"a pause of a scope that does not cover the candidate leaves it to the judge",
			args{request: func(string) curate.Request {
				return curate.Request{Action: trace.ActionPause, Labels: trace.Labels{"repo": {"svc"}}}
			}},
			want{[]string{"add k-late v1 shadow  1"}, true},
		},
		{
			"a dismissed question is no longer due and its version waits undecided",
			args{request: func(decide string) curate.Request { return curate.Request{Action: trace.ActionDismiss, Target: decide} }},
			want{[]string{"add k-late v1 shadow  1"}, false},
		},
		{
			"a dismissed question comes back with new evidence",
			args{request: func(decide string) curate.Request { return curate.Request{Action: trace.ActionDismiss, Target: decide} }, evidence: true},
			want{[]string{"add k-add v1 shadow  1", "add k-late v1 shadow  1"}, true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{lesson("k-add", 1, "Name the failing test first", acme("acme"), candidate, "")}, nil)
			first, err := b.curator().Pass(ctx, b.setting(t))
			require.NoError(t, err)
			require.Len(t, first.Decided, 1)
			_, err = b.intervener(nil).Intervene(ctx, tc.args.request(first.Decided[0].ID), person)
			require.NoError(t, err)
			late := lesson("k-late", 1, "Say which test failed", acme("acme"), candidate, "")
			b.correct(t, "r-k-late", late.Run.Labels, []feedback.Verdict{feedback.VerdictReject})
			_, _, err = b.stores.Ledger.Propose(ctx, late)
			require.NoError(t, err)
			if tc.args.evidence {
				b.correct(t, "r-more", acme("acme").Labels, []feedback.Verdict{feedback.VerdictReject})
				run, err := b.stores.Traces.Get(ctx, "r-more")
				require.NoError(t, err)
				verdicts, err := b.stores.Feedback.List(ctx, record.FeedbackFilter{TraceID: "r-more"})
				require.NoError(t, err)
				_, err = b.stores.Ledger.Corroborate(ctx, "k-add", 1, trace.Restore(run), feedback.RestoreAll(verdicts))
				require.NoError(t, err)
			}

			second, err := b.curator().Pass(ctx, b.setting(t))

			require.NoError(t, err)
			assert.Equal(t, tc.want.decided, decisionsOf(second.Decided))
			assert.Equal(t, tc.want.due, slices.ContainsFunc(second.Due, func(d curate.Due) bool { return d.Ref.ID == "k-add" }))
		})
	}
}

// The fold reads a pause over the labels a change may reach and leaves out what does not decode
func TestInterventionsPaused(t *testing.T) {
	at := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	pause := func(kind string, labels trace.Labels) trace.Trace {
		tr, err := trace.Intervention{Action: trace.ActionPause, Target: kind, Labels: labels, Proof: trace.ProofKey, Person: "ann"}.
			Trace("", "", trace.SourceManual, trace.InterventionOutput{}, at)
		require.NoError(t, err)
		return tr
	}
	broken := trace.Trace{ID: "x", Name: trace.NameIntervention, Input: []byte(`{`)}
	type args struct {
		traces trace.Traces
		kind   curate.ChangeKind
		scope  knowledge.RunScope
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a pause of the kind covers it in every scope", args{trace.Traces{pause("add", nil)}, curate.ChangeAdd, *acme("svc")}, true},
		{"a pause of another kind does not", args{trace.Traces{pause("update", nil)}, curate.ChangeAdd, *acme("svc")}, false},
		{"a pause of a repo covers a scope of that repo", args{trace.Traces{pause("", trace.Labels{"repo": {"acme"}})}, curate.ChangeAdd, *acme("acme", "dir", ".")}, true},
		{"a pause of a repo covers a scope that names no repo", args{trace.Traces{pause("", trace.Labels{"repo": {"acme"}})}, curate.ChangeAdd,
			knowledge.RunScope{Producer: "session"}}, true},
		{"a pause of a repo does not cover another repo", args{trace.Traces{pause("", trace.Labels{"repo": {"acme"}})}, curate.ChangeAdd, *acme("svc")}, false},
		{"a record that does not decode pauses nothing", args{trace.Traces{broken}, curate.ChangeAdd, *acme("acme")}, false},
		{"a proposal of a pause pauses nothing", args{trace.Traces{func() trace.Trace {
			tr, err := trace.Intervention{Action: trace.ActionPause, Target: "add", Proof: trace.ProofNone}.
				Trace("", "", trace.SourceManual, trace.InterventionOutput{}, at)
			require.NoError(t, err)
			return tr
		}()}, curate.ChangeAdd, *acme("acme")}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, curate.RestoreInterventions(tc.args.traces).Paused(tc.args.kind, tc.args.scope))
		})
	}
}
