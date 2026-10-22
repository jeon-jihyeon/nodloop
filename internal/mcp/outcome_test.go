package mcp_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestServerOutcomeFailure(t *testing.T) {
	tcs := []struct {
		name string
		args bool
	}{
		{"missing run", false}, {"append failure", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			c := testkit.Connect(t, serve(st, mcp.Caller{Person: "ann", Proves: true}))
			run := recordRun(t, c, "acme")
			id := "absent"
			if tc.args {
				id = run
				require.NoError(t, os.MkdirAll(filepath.Join(st.Dir, "outcomes.jsonl"), 0700))
			}
			err := c.Run(t, "outcome", map[string]any{"trace_id": id, "result": "confirmed"})
			assert.ErrorIs(t, err, testkit.ErrTool)
		})
	}
}

func TestServerOutcomeActor(t *testing.T) {
	type args struct {
		caller          mcp.Caller
		actor, reviewer string
	}
	type want struct {
		actor, proof string
		proven       bool
		records      int
		err          error
	}
	person := mcp.Caller{Person: "ann", Proves: true}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a name proves no evaluator", args{mcp.Caller{}, "", "ann"}, want{"", "", false, 1, nil}},
		{"person key defaults to person", args{person, "", ""}, want{"person", "key", true, 1, nil}},
		{"person key can submit a code observation", args{person, "code", ""}, want{"code", "", false, 1, nil}},
		{"session remains inferred under a person key", args{person, "", "session"}, want{"", "", false, 1, nil}},
		{"person claim over stdio proves nobody", args{mcp.Caller{}, "person", "ann"},
			want{"person", "none", false, 1, nil}},
		{"agent cannot claim person", args{mcp.Caller{Agent: true}, "person", "ann"},
			want{"", "", false, 0, testkit.ErrTool}},
		{"invalid actor writes nothing", args{person, "future", ""}, want{"", "", false, 0, testkit.ErrTool}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			c := testkit.Connect(t, serve(st, tc.args.caller))
			run := recordRun(t, c, "acme")
			in := map[string]any{"trace_id": run, "result": "confirmed", "reviewer": tc.args.reviewer}
			if tc.args.actor != "" {
				in["actor"] = tc.args.actor
			}
			err := c.Run(t, "outcome", in)
			assert.ErrorIs(t, err, tc.want.err)
			recorded, err := st.Outcomes.List(t.Context(), run)
			require.NoError(t, err)
			var actor, proof string
			var proven bool
			for _, r := range recorded {
				actor, proof, proven = r.Actor, r.Proof, feedback.RestoreOutcome(r).Proven()
			}
			assert.Equal(t, tc.want, want{actor, proof, proven, len(recorded), tc.want.err})
			items, err := st.Ledger.All(t.Context())
			require.NoError(t, err)
			assert.Empty(t, items)
		})
	}
}
