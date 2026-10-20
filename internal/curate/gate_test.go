package curate_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

var (
	add1     = curate.Opening{Kind: curate.ChangeAdd, Bin: curate.Bin1}
	add2     = curate.Opening{Kind: curate.ChangeAdd, Bin: curate.Bin2}
	update1  = curate.Opening{Kind: curate.ChangeUpdate, Bin: curate.Bin1}
	withdraw = curate.Opening{Kind: curate.ChangeWithdraw}
)

// A gauge whose bound was met over the answers
func met(o curate.Opening, answers int) curate.Gauge {
	return curate.Gauge{Opening: o, Answers: answers, Met: true}
}

// The knobs with the change applied to a copy of the start knobs
func knobs(change func(*curate.Knobs)) curate.Knobs {
	k := curate.StartKnobs()
	change(&k)
	return k
}

// The first pass of a cycle narrows on its own and only offers what would open
func TestKnobsStart(t *testing.T) {
	start := curate.StartKnobs()
	type args struct {
		knobs   curate.Knobs
		numbers curate.Numbers
	}
	type want struct {
		open    []curate.Opening
		tier1   float64
		changes []curate.KnobChange
		offers  []curate.Offer
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no numbers keep the knobs of the start", args{start, curate.Numbers{}}, want{start.Open, 0.15, nil, nil}},
		{
			"a posterior above the target with probability over one half narrows its kind and bin and doubles the audit p",
			args{start, curate.Numbers{Gauges: []curate.Gauge{{Opening: add1, Answers: 1, Differ: 1, Above: 0.736}}}},
			want{[]curate.Opening{withdraw}, 0.3, []curate.KnobChange{
				{Rule: curate.KnobPosterior, Opening: &add1, Answers: 1, Differ: 1}, {Rule: curate.KnobRaise, P: 0.3},
			}, nil},
		},
		{"a posterior at one half keeps its kind and bin", args{start, curate.Numbers{Gauges: []curate.Gauge{{Opening: add1, Above: 0.5}}}}, want{start.Open, 0.15, nil, nil}},
		{
			"an alarm narrows the kind and bin it names",
			args{start, curate.Numbers{Alarmed: []curate.Opening{withdraw}}},
			want{[]curate.Opening{add1}, 0.3, []curate.KnobChange{{Rule: curate.KnobAlarm, Opening: &withdraw}, {Rule: curate.KnobRaise, P: 0.3}}, nil},
		},
		{"an alarm on a closed kind and bin changes nothing", args{start, curate.Numbers{Alarmed: []curate.Opening{update1}}}, want{start.Open, 0.15, nil, nil}},
		{"a shift of the judge doubles the audit p", args{start, curate.Numbers{Shift: true}}, want{start.Open, 0.3, []curate.KnobChange{{Rule: curate.KnobRaise, P: 0.3}}, nil}},
		{
			"a raise stops at the ceiling",
			args{knobs(func(k *curate.Knobs) { k.Audit.Tier1 = 0.2 }), curate.Numbers{Shift: true}},
			want{start.Open, 0.3, []curate.KnobChange{{Rule: curate.KnobRaise, P: 0.3}}, nil},
		},
		{
			"a raise never lowers an audit p the person set above the ceiling",
			args{knobs(func(k *curate.Knobs) { k.Audit.Tier1 = 0.5 }), curate.Numbers{Shift: true}},
			want{start.Open, 0.5, nil, nil},
		},
		{
			"every open kind and bin meeting its bound lowers the audit p",
			args{start, curate.Numbers{Gauges: []curate.Gauge{met(add1, 43), met(withdraw, 43)}}},
			want{start.Open, 0.05, []curate.KnobChange{{Rule: curate.KnobLower, P: 0.05}}, nil},
		},
		{
			"an audit p at the floor stays",
			args{knobs(func(k *curate.Knobs) { k.Audit.Tier1 = 0.05 }), curate.Numbers{Gauges: []curate.Gauge{met(add1, 43), met(withdraw, 43)}}},
			want{start.Open, 0.05, nil, nil},
		},
		{"one open kind and bin short of its bound keeps the audit p", args{start, curate.Numbers{Gauges: []curate.Gauge{met(add1, 43)}}}, want{start.Open, 0.15, nil, nil}},
		{"knobs that open nothing never lower the audit p", args{knobs(func(k *curate.Knobs) { k.Open = nil }), curate.Numbers{}}, want{nil, 0.15, nil, nil}},
		{
			"the first closed kind and bin whose bound was met is offered and stays closed",
			args{start, curate.Numbers{Gauges: []curate.Gauge{met(add2, 66), {Opening: update1, Answers: 43, Differ: 0, Met: true}}}},
			want{start.Open, 0.15, nil, []curate.Offer{{Kind: curate.ChangeWiden, Opens: &update1, Answers: 43}}},
		},
		{
			"a kind and bin the pass narrowed is never offered",
			args{start, curate.Numbers{Gauges: []curate.Gauge{{Opening: add1, Answers: 66, Differ: 1, Above: 0.9, Met: true}}}},
			want{[]curate.Opening{withdraw}, 0.3, []curate.KnobChange{
				{Rule: curate.KnobPosterior, Opening: &add1, Answers: 66, Differ: 1}, {Rule: curate.KnobRaise, P: 0.3},
			}, nil},
		},
		{
			"two closed cycles in shadow with the bound of add met offer the activation",
			args{start, curate.Numbers{ShadowCycles: 2, ShadowMet: true}},
			want{start.Open, 0.15, nil, []curate.Offer{{Kind: curate.ChangeActivate}}},
		},
		{"one closed cycle in shadow offers no activation", args{start, curate.Numbers{ShadowCycles: 1, ShadowMet: true}}, want{start.Open, 0.15, nil, nil}},
		{"the bound of add not met offers no activation", args{start, curate.Numbers{ShadowCycles: 3}}, want{start.Open, 0.15, nil, nil}},
		{
			"an active tier 1 is offered no activation",
			args{knobs(func(k *curate.Knobs) { k.Activated = true }), curate.Numbers{ShadowCycles: 2, ShadowMet: true}},
			want{start.Open, 0.15, nil, nil},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.args.knobs.Start(tc.args.numbers)

			assert.Equal(t, tc.want, want{got.Knobs.Open, got.Knobs.Audit.Tier1, got.Changes, got.Offers})
			assert.Equal(t, curate.StartKnobs().Open, start.Open, "the knobs the pass started from stay as they were")
		})
	}
}

// Inside a cycle only an alarm moves the knobs
func TestKnobsAlarm(t *testing.T) {
	start := curate.StartKnobs()
	type want struct {
		open    []curate.Opening
		tier1   float64
		changes []curate.KnobChange
	}
	tcs := []struct {
		name string
		args curate.Numbers
		want want
	}{
		{"no alarm changes nothing", curate.Numbers{Gauges: []curate.Gauge{{Opening: add1, Above: 0.9}}, Shift: true}, want{start.Open, 0.15, nil}},
		{
			"an alarm returns its open kinds and bins to tier 2 at once with the answers behind them",
			curate.Numbers{Alarmed: []curate.Opening{add1, update1}, Gauges: []curate.Gauge{{Opening: add1, Answers: 12, Differ: 3}}},
			want{[]curate.Opening{withdraw}, 0.3, []curate.KnobChange{
				{Rule: curate.KnobAlarm, Opening: &add1, Answers: 12, Differ: 3}, {Rule: curate.KnobRaise, P: 0.3},
			}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := start.Alarm(tc.args)

			assert.Equal(t, tc.want, want{got.Knobs.Open, got.Knobs.Audit.Tier1, got.Changes})
			assert.Empty(t, got.Offers)
		})
	}
}

// The yes to a widen question opens what tier 1 may carry out and nothing else
func TestKnobsWidened(t *testing.T) {
	start := curate.StartKnobs()
	type want struct {
		open  []curate.Opening
		opens bool
	}
	tcs := []struct {
		name string
		args curate.Opening
		want want
	}{
		{"a closed kind and bin opens", update1, want{[]curate.Opening{add1, withdraw, update1}, true}},
		{"an open kind and bin stays once", add1, want{start.Open, true}},
		{"a kind tier 1 never carries out stays closed", curate.Opening{Kind: curate.ChangeDrop}, want{start.Open, false}},
		{"a withdrawal in a bin stays closed", curate.Opening{Kind: curate.ChangeWithdraw, Bin: curate.Bin1}, want{start.Open, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := start.Widened(tc.args)

			assert.Equal(t, tc.want, want{got.Open, got.Opens(tc.args)})
			assert.Equal(t, curate.StartKnobs().Open, start.Open, "the knobs widened stay as they were")
		})
	}
}

// The decide of an offer prepares its question as a valid change
func TestOfferChange(t *testing.T) {
	tcs := []struct {
		name string
		args curate.Offer
		want string
	}{
		{"a widening names what it opens", curate.Offer{Kind: curate.ChangeWiden, Opens: &update1, Answers: 43}, `{"kind":"widen","anchor":4,"opens":{"kind":"update","bin":"b1"}}`},
		{"the activation names nothing", curate.Offer{Kind: curate.ChangeActivate}, `{"kind":"activate","anchor":4}`},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := decided("d1", 1).Decide(tc.args.Change(4), curate.Decision{Tier: 2, Action: curate.ActionAsk}, nil, at)

			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got.Input))
		})
	}
}

// A narrowing keeps the cycle with the changes behind it and a new policy version since the knobs moved
func TestKnobEventChanges(t *testing.T) {
	event := startKnobs()
	event.Knobs = curate.StartKnobs()
	move := event.Knobs.Alarm(curate.Numbers{Alarmed: []curate.Opening{add1}})
	narrowed := event
	narrowed.Knobs, narrowed.Changes, narrowed.Cause, narrowed.Before = move.Knobs, move.Changes, curate.KnobNarrow, "k-1"

	before, err := event.Trace("0.10.0", at)
	require.NoError(t, err)
	after, err := narrowed.Trace("0.10.0", at.Add(time.Hour))
	require.NoError(t, err)

	assert.NoError(t, schema.Validate(string(trace.NameKnob), after.Record()))
	assert.Contains(t, string(after.Input), `"changes":[{"rule":"alarm","opening":{"kind":"add","bin":"b1"}},{"rule":"raise","p":0.3}]`)
	assert.Contains(t, string(before.Input), `"open":[{"kind":"add","bin":"b1"},{"kind":"withdraw"}],"audit":{"tier0":0.05,"tier1":0.15}`)
	assert.NotEqual(t, event.Policy("0.10.0"), narrowed.Policy("0.10.0"))
}
