package nodloop_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop"
)

func TestClientCorrelationScope(t *testing.T) {
	tenant := nodloop.Labels{"tenant": {"acme"}}
	pinned := nodloop.Labels{"tenant": {"acme"}, "correlation": {"conversation-7"}}
	tcs := []struct {
		name string
		args nodloop.Labels
		want nodloop.Labels
	}{
		{"default scope excludes correlation", nil, tenant},
		{"explicit scope preserves correlation", pinned, pinned},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			c := open(t)
			run, err := c.Record(ctx, nodloop.Run{
				Producer: "support-bot", SessionID: "host-session-7", Labels: pinned, Output: []byte("answer"),
			})
			require.NoError(t, err)
			require.NoError(t, c.Judge(ctx, nodloop.Judgment{
				Run: run, Verdict: nodloop.VerdictReject, Reason: "missing the window", Reviewer: "ann",
			}))

			item, err := c.Propose(ctx, nodloop.Proposal{
				From: run, Kind: nodloop.KindJudgment, Content: "Quote the refund window", Labels: tc.args,
			})
			require.NoError(t, err)
			assert.Equal(t, tc.want, item.Labels)
			page, err := c.Records(ctx, nodloop.Query{Kind: "run"})
			require.NoError(t, err)
			require.Len(t, page.Records, 1)
			var stored struct {
				ID      string         `json:"record_id"`
				Session string         `json:"session_id"`
				Labels  nodloop.Labels `json:"labels"`
			}
			require.NoError(t, json.Unmarshal(page.Records[0], &stored))
			assert.Equal(t, run, stored.ID)
			assert.Equal(t, "host-session-7", stored.Session)
			assert.Equal(t, pinned, stored.Labels)
		})
	}
}
