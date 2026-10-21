package mcp_test

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestServerDecisionTurn(t *testing.T) {
	type args struct {
		tool string
		fail bool
	}
	type want struct {
		err           error
		text          string
		released      int32
		interventions int32
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"approve releases after its intervention", args{"approve", false}, want{released: 1, interventions: 1}},
		{"reaffirm releases after its intervention", args{"reaffirm", false}, want{released: 1, interventions: 1}},
		{"approve lock failure writes nothing", args{"approve", true}, want{err: testkit.ErrTool, text: assert.AnError.Error()}},
		{"reaffirm lock failure writes nothing", args{"reaffirm", true}, want{err: testkit.ErrTool, text: assert.AnError.Error()}},
		{"compaction lock failure writes nothing", args{"approve_compaction", true}, want{err: testkit.ErrTool, text: assert.AnError.Error()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			_, _, err := st.Ledger.Propose(t.Context(), knowledge.Knowledge{
				ID: "example", Kind: knowledge.KindJudgment, Content: "Example rule", Author: "example",
				Run: &knowledge.RunScope{Producer: "example"}, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"example-run"}},
			})
			require.NoError(t, err)
			if tc.args.tool == "reaffirm" {
				_, err := st.Ledger.Approve(t.Context(), "example", 1, "reviewer")
				require.NoError(t, err)
			}
			var taken, released, interventions atomic.Int32
			turn := func() (func(), error) {
				taken.Add(1)
				if tc.args.fail {
					return nil, assert.AnError
				}
				return func() {
					released.Add(1)
					events, err := st.Traces.List(t.Context(), record.TraceFilter{Name: string(trace.NameIntervention)})
					if err != nil {
						interventions.Store(-1)
						return
					}
					interventions.Store(int32(len(events)))
				}, nil
			}
			c := connectWith(t, st, mcp.Policy{Turn: turn})
			input := map[string]any{"id": "example", "version": 1, "approver": "reviewer"}
			if tc.args.tool == "approve_compaction" {
				input = map[string]any{"compaction": "example", "approver": "reviewer"}
			}

			err = c.Run(t, tc.args.tool, input)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, errText(err), tc.want.text)
			assert.Equal(t, int32(1), taken.Load())
			assert.Equal(t, tc.want.released, released.Load())
			assert.Equal(t, tc.want.interventions, interventions.Load())
		})
	}
}
