package trace_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestInterventionTrace(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	keep := trace.Intervention{Action: trace.ActionJudge, ID: "k-1", Version: 2, Choice: trace.ChoiceKeep}
	with := func(proof trace.Proof, person string) trace.Intervention {
		in := keep
		in.Proof, in.Person = proof, person
		return in
	}
	approved := []trace.Written{{ID: "k-1", Version: 2, Status: "approved"}}
	type args struct {
		in    trace.Intervention
		wrote []trace.Written
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
			args{with(trace.ProofTerminal, "ann"), approved},
			want{
				`{"action":"judge","id":"k-1","version":2,"choice":"keep","authority":"person","proof":"terminal","person":"ann"}`,
				`{"records":[{"id":"k-1","version":2,"status":"approved"}]}`, nil,
			},
		},
		{
			"a proposal is the agent's and wrote nothing",
			args{with(trace.ProofNone, ""), nil},
			want{`{"action":"judge","id":"k-1","version":2,"choice":"keep","authority":"agent","proof":"none"}`, `{"records":[]}`, nil},
		},
		{
			"a compaction is a target of its own",
			args{trace.Intervention{Action: trace.ActionJudge, Compaction: "c-1", Choice: trace.ChoiceKeep, Proof: trace.ProofKey, Person: "ann"}, nil},
			want{`{"action":"judge","compaction":"c-1","choice":"keep","authority":"person","proof":"key","person":"ann"}`, `{"records":[]}`, nil},
		},
		{
			"a scope choice keeps its labels",
			args{trace.Intervention{
				Action: trace.ActionJudge, ID: "k-1", Version: 1, Choice: trace.ChoiceScope, Labels: trace.Labels{"repo": {"acme"}},
				Proof: trace.ProofElicitation, Person: "ann",
			}, nil},
			want{
				`{"action":"judge","id":"k-1","version":1,"choice":"scope","labels":{"repo":["acme"]},"authority":"person","proof":"elicitation","person":"ann"}`,
				`{"records":[]}`, nil,
			},
		},
		{"an unknown action is refused", args{trace.Intervention{Action: "approve", ID: "k-1", Version: 1, Choice: trace.ChoiceKeep, Proof: trace.ProofNone}, nil},
			want{err: trace.ErrInterventionInvalid}},
		{"an unknown choice is refused", args{trace.Intervention{Action: trace.ActionJudge, ID: "k-1", Version: 1, Choice: "approve", Proof: trace.ProofNone}, nil},
			want{err: trace.ErrInterventionInvalid}},
		{"an unknown proof is refused", args{with("model", ""), nil}, want{err: trace.ErrInterventionInvalid}},
		{"a judgment without a target is refused", args{trace.Intervention{Action: trace.ActionJudge, Choice: trace.ChoiceKeep, Proof: trace.ProofNone}, nil},
			want{err: trace.ErrInterventionInvalid}},
		{"a person's judgment without a name is refused", args{with(trace.ProofKey, ""), approved}, want{err: trace.ErrInterventionInvalid}},
		{"a proposal naming a person is refused", args{with(trace.ProofNone, "ann"), nil}, want{err: trace.ErrInterventionInvalid}},
		{"a proposal that wrote records is refused", args{with(trace.ProofNone, ""), approved}, want{err: trace.ErrInterventionInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.in.Trace(tc.args.wrote, "mcp-1", at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.input, string(got.Input))
			assert.Equal(t, tc.want.output, string(got.Output))
		})
	}
}

// The trace of an intervention groups under the session of its caller and names its action
func TestInterventionTraceFields(t *testing.T) {
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	in := trace.Intervention{Action: trace.ActionJudge, ID: "k-1", Version: 1, Choice: trace.ChoiceUndo, Proof: trace.ProofNone}

	got, err := in.Trace(nil, "mcp-1", at)
	require.NoError(t, err)

	assert.Equal(t, []any{trace.NameIntervention, "mcp-1", "judge", at, true}, []any{got.Name, got.SessionID, got.Subject, got.Time, got.Name.Valid()})
	var back trace.Intervention
	require.NoError(t, json.Unmarshal(got.Input, &back))
	assert.Equal(t, trace.AuthorityAgent, back.Authority)
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
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Authority())
		})
	}
}
