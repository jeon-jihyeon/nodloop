package trace_test

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestInterventionTrace(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	keep := trace.Intervention{Action: trace.ActionJudge, Target: "k-1:2", ID: "k-1", Version: 2, Choice: trace.ChoiceKeep}
	with := func(in trace.Intervention, proof trace.Proof, person string) trace.Intervention {
		in.Proof, in.Person = proof, person
		return in
	}
	value, before, nan, inf := 0.3, 0.15, math.NaN(), math.Inf(1)
	approved := trace.InterventionOutput{Records: []trace.Written{{ID: "k-1", Version: 2, Status: "approved"}}}
	type args struct {
		in     trace.Intervention
		out    trace.InterventionOutput
		source trace.Source
	}
	type want struct {
		input  string
		output string
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a person's judgment names the person, the proof and the records it wrote",
			args{with(keep, trace.ProofTerminal, "ann"), approved, trace.SourceCommand},
			want{
				`{"action":"judge","target":"k-1:2","id":"k-1","version":2,"choice":"keep","authority":"person","proof":"terminal","person":"ann"}`,
				`{"records":[{"id":"k-1","version":2,"status":"approved"}]}`, nil,
			},
		},
		{
			"a proposal is the agent's and wrote nothing",
			args{with(keep, trace.ProofNone, ""), trace.InterventionOutput{}, trace.SourceManual},
			want{`{"action":"judge","target":"k-1:2","id":"k-1","version":2,"choice":"keep","authority":"agent","proof":"none"}`, `{"records":[]}`, nil},
		},
		{
			"a compaction is a target of its own",
			args{
				trace.Intervention{Action: trace.ActionJudge, Compaction: "c-1", Choice: trace.ChoiceKeep, Proof: trace.ProofKey, Person: "ann"},
				trace.InterventionOutput{}, trace.SourceManual,
			},
			want{`{"action":"judge","compaction":"c-1","choice":"keep","authority":"person","proof":"key","person":"ann"}`, `{"records":[]}`, nil},
		},
		{
			"a scope choice keeps its labels",
			args{trace.Intervention{
				Action: trace.ActionJudge, ID: "k-1", Version: 1, Choice: trace.ChoiceScope, Labels: trace.Labels{"repo": {"acme"}},
				Proof: trace.ProofElicitation, Person: "ann",
			}, trace.InterventionOutput{}, trace.SourceManual},
			want{
				`{"action":"judge","id":"k-1","version":1,"choice":"scope","labels":{"repo":["acme"]},"authority":"person","proof":"elicitation","person":"ann"}`,
				`{"records":[]}`, nil,
			},
		},
		{
			"an answer names its question and the answer it wrote",
			args{
				trace.Intervention{Action: trace.ActionAnswer, Target: "d1", Decide: "d1", Choice: trace.ChoiceDrop, Proof: trace.ProofKey, Person: "ann"},
				trace.InterventionOutput{Records: []trace.Written{}, Answer: "a1"}, trace.SourceManual,
			},
			want{
				`{"action":"answer","target":"d1","decide":"d1","choice":"drop","authority":"person","proof":"key","person":"ann"}`,
				`{"records":[],"answer":"a1"}`, nil,
			},
		},
		{
			"a knob names its value and the value it had",
			args{
				trace.Intervention{Action: trace.ActionKnob, Target: "audit_tier1", Knob: "audit_tier1", Value: &value, Before: &before, Proof: trace.ProofKey, Person: "ann"},
				trace.InterventionOutput{}, trace.SourceManual,
			},
			want{
				`{"action":"knob","target":"audit_tier1","knob":"audit_tier1","value":0.3,"before":0.15,"authority":"person","proof":"key","person":"ann"}`,
				`{"records":[]}`, nil,
			},
		},
		{
			"a digest of an agent keeps the cursor it answered",
			args{trace.Intervention{Action: trace.ActionDigest, Proof: trace.ProofNone}, trace.InterventionOutput{Since: "c1"}, trace.SourceManual},
			want{`{"action":"digest","authority":"agent","proof":"none"}`, `{"records":[],"since":"c1"}`, nil},
		},
		{
			"a dismiss names the question it closes",
			args{with(trace.Intervention{Action: trace.ActionDismiss, Decide: "d1"}, trace.ProofNone, ""), trace.InterventionOutput{}, trace.SourceManual},
			want{`{"action":"dismiss","decide":"d1","authority":"agent","proof":"none"}`, `{"records":[]}`, nil},
		},
		{
			"a pause names its scope",
			args{
				with(trace.Intervention{Action: trace.ActionPause, Labels: trace.Labels{"repo": {"acme"}}}, trace.ProofNone, ""),
				trace.InterventionOutput{}, trace.SourceManual,
			},
			want{`{"action":"pause","labels":{"repo":["acme"]},"authority":"agent","proof":"none"}`, `{"records":[]}`, nil},
		},
		{
			"a resume names the pause it ends",
			args{with(trace.Intervention{Action: trace.ActionResume, Reverses: "i1"}, trace.ProofNone, ""), trace.InterventionOutput{}, trace.SourceManual},
			want{`{"action":"resume","reverses":"i1","authority":"agent","proof":"none"}`, `{"records":[]}`, nil},
		},
		{
			"a reanchor names its place",
			args{with(trace.Intervention{Action: trace.ActionReanchor, Target: "session"}, trace.ProofNone, ""), trace.InterventionOutput{}, trace.SourceManual},
			want{`{"action":"reanchor","target":"session","authority":"agent","proof":"none"}`, `{"records":[]}`, nil},
		},
		{"an unknown action is refused", args{with(trace.Intervention{Action: "approve"}, trace.ProofNone, ""), trace.InterventionOutput{}, trace.SourceManual},
			want{err: trace.ErrInterventionInvalid}},
		{"a proof the caller forgot to set is refused", args{with(keep, "", "ann"), approved, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{
			"a knob value that is no number is refused",
			args{with(trace.Intervention{Action: trace.ActionKnob, Knob: "audit_tier1", Value: &nan}, trace.ProofKey, "ann"), trace.InterventionOutput{}, trace.SourceManual},
			want{err: trace.ErrInterventionInvalid},
		},
		{
			"a knob value it had that is infinite is refused",
			args{
				with(trace.Intervention{Action: trace.ActionKnob, Knob: "audit_tier1", Value: &value, Before: &inf}, trace.ProofKey, "ann"),
				trace.InterventionOutput{}, trace.SourceManual,
			},
			want{err: trace.ErrInterventionInvalid},
		},
		{"a dismiss naming no question is refused", args{with(trace.Intervention{Action: trace.ActionDismiss}, trace.ProofNone, ""), trace.InterventionOutput{},
			trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"an answer without a choice is refused", args{with(trace.Intervention{Action: trace.ActionAnswer, Decide: "d1"}, trace.ProofNone, ""),
			trace.InterventionOutput{}, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"an unknown choice is refused", args{with(trace.Intervention{Action: trace.ActionJudge, ID: "k-1", Version: 1, Choice: "approve"}, trace.ProofNone, ""),
			trace.InterventionOutput{}, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"an unknown proof is refused", args{with(keep, "model", ""), trace.InterventionOutput{}, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"a judgment without a target is refused", args{with(trace.Intervention{Action: trace.ActionJudge, Choice: trace.ChoiceKeep}, trace.ProofNone, ""),
			trace.InterventionOutput{}, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"an undo of a compaction is refused", args{with(trace.Intervention{Action: trace.ActionJudge, Compaction: "c-1", Choice: trace.ChoiceUndo}, trace.ProofNone, ""),
			trace.InterventionOutput{}, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"an answer with a judgment choice of its own is refused", args{with(trace.Intervention{Action: trace.ActionAnswer, Decide: "d1", Choice: trace.ChoiceScope},
			trace.ProofNone, ""), trace.InterventionOutput{}, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"a dismiss with a choice is refused", args{with(trace.Intervention{Action: trace.ActionDismiss, Decide: "d1", Choice: trace.ChoiceKeep}, trace.ProofNone, ""),
			trace.InterventionOutput{}, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"a pause of nothing is refused", args{with(trace.Intervention{Action: trace.ActionPause}, trace.ProofNone, ""), trace.InterventionOutput{}, trace.SourceManual},
			want{err: trace.ErrInterventionInvalid}},
		{"a resume naming no pause is refused", args{with(trace.Intervention{Action: trace.ActionResume}, trace.ProofNone, ""), trace.InterventionOutput{},
			trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"a reverse naming nothing is refused", args{with(trace.Intervention{Action: trace.ActionReverse}, trace.ProofNone, ""), trace.InterventionOutput{},
			trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"a knob without a value is refused", args{with(trace.Intervention{Action: trace.ActionKnob, Knob: "item_hold"}, trace.ProofNone, ""),
			trace.InterventionOutput{}, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"a reanchor without a place is refused", args{with(trace.Intervention{Action: trace.ActionReanchor}, trace.ProofNone, ""), trace.InterventionOutput{},
			trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"a person's judgment without a name is refused", args{with(keep, trace.ProofKey, ""), approved, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"a proposal naming a person is refused", args{with(keep, trace.ProofNone, "ann"), trace.InterventionOutput{}, trace.SourceManual},
			want{err: trace.ErrInterventionInvalid}},
		{"a proposal that wrote records is refused", args{with(keep, trace.ProofNone, ""), approved, trace.SourceManual}, want{err: trace.ErrInterventionInvalid}},
		{"a proposal that wrote an answer is refused", args{with(keep, trace.ProofNone, ""), trace.InterventionOutput{Answer: "a1"}, trace.SourceManual},
			want{err: trace.ErrInterventionInvalid}},
		{"a pushed source is refused since the person chose what to act on", args{with(keep, trace.ProofKey, "ann"), approved, trace.SourcePush},
			want{err: trace.ErrInterventionInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.in.Trace("", "mcp-1", tc.args.source, tc.args.out, at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.input, string(got.Input))
			assert.Equal(t, tc.want.output, string(got.Output))
		})
	}
}

// The trace of an intervention groups under the session of its caller, names its action and keeps an id the caller made
func TestInterventionTraceFields(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	in := trace.Intervention{Action: trace.ActionJudge, ID: "k-1", Version: 1, Choice: trace.ChoiceUndo, Proof: trace.ProofNone}
	type args struct {
		id string
	}
	type want struct {
		id func(string) bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an id the caller made", args{"0123456789ab01234567"}, want{func(id string) bool { return id == "0123456789ab01234567" }}},
		{"a new id", args{""}, want{func(id string) bool { _, ok := trace.IDTime(id); return ok }}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := in.Trace(tc.args.id, "mcp-1", trace.SourceManual, trace.InterventionOutput{}, at)
			require.NoError(t, err)

			assert.Equal(t, []any{trace.NameIntervention, "mcp-1", "judge", at, trace.SourceManual, true, true},
				[]any{got.Name, got.SessionID, got.Subject, got.Time, got.Source, got.Name.Valid(), tc.want.id(got.ID)})
			var back trace.Intervention
			require.NoError(t, json.Unmarshal(got.Input, &back))
			assert.Equal(t, trace.AuthorityAgent, back.Authority)
			assert.NoError(t, schema.Validate(string(trace.NameIntervention), got.Record()))
		})
	}
}

func TestProofAuthority(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Proof
		want trace.Authority
	}{
		{"a person key proves a person", trace.ProofKey, trace.AuthorityPerson},
		{"a terminal proves a person", trace.ProofTerminal, trace.AuthorityPerson},
		{"an accepted form proves a person", trace.ProofElicitation, trace.AuthorityPerson},
		{"a reply word proves a person", trace.ProofReply, trace.AuthorityPerson},
		{"no proof is an agent", trace.ProofNone, trace.AuthorityAgent},
		{"a proof a caller forgot to set is an agent", "", trace.AuthorityAgent},
		{"an unknown proof is an agent", "badge", trace.AuthorityAgent},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Authority())
		})
	}
}

func TestChoiceJudges(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Choice
		want bool
	}{
		{"keep judges", trace.ChoiceKeep, true},
		{"undo judges", trace.ChoiceUndo, true},
		{"scope judges", trace.ChoiceScope, true},
		{"drop answers a question", trace.ChoiceDrop, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Judges())
		})
	}
}
