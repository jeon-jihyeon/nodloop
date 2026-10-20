package nodloop_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

// The record_id of each record of a page in its order
func recordIDs(t *testing.T, p nodloop.Page) []string {
	t.Helper()
	ids := []string{}
	for _, r := range p.Records {
		var head struct {
			Kind string `json:"record_kind"`
			ID   string `json:"record_id"`
		}
		require.NoError(t, json.Unmarshal(r, &head))
		require.NoError(t, schema.Validate(head.Kind, r))
		ids = append(ids, head.ID)
	}
	return ids
}

// The records a client wrote page oldest first across a cursor in their published schema with the text left out unless asked
func TestClientRecords(t *testing.T) {
	ctx := context.Background()
	clock := at
	c, err := nodloop.Open(t.TempDir(), nodloop.WithClock(func() time.Time { return clock }))
	require.NoError(t, err)
	first, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Output: []byte("Here are the refund steps")})
	require.NoError(t, err)
	clock = clock.Add(time.Millisecond)
	second, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Output: []byte("The window is 30 days")})
	require.NoError(t, err)
	require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: first, Verdict: nodloop.VerdictReject, Reason: "the window was missing", Reviewer: "ann"}))
	proposed, err := c.Propose(ctx, nodloop.Proposal{Kind: nodloop.KindJudgment, Content: "Quote the refund window", From: first, ID: "window"})
	require.NoError(t, err)
	proposal, err := c.Approve(ctx, proposed.ID, proposed.Version, "ann")
	require.NoError(t, err)
	clock = clock.Add(time.Minute)

	one, err := c.Records(ctx, nodloop.Query{Kind: "run", Limit: 1})
	require.NoError(t, err)
	two, err := c.Records(ctx, nodloop.Query{Kind: "run", Since: one.Next})
	require.NoError(t, err)
	shown, err := c.Records(ctx, nodloop.Query{Kind: "run", Limit: 1, Content: true})
	require.NoError(t, err)
	verdicts, err := c.Records(ctx, nodloop.Query{Kind: "verdict"})
	require.NoError(t, err)
	items, err := c.Records(ctx, nodloop.Query{Kind: "knowledge"})
	require.NoError(t, err)
	interventions, err := c.Records(ctx, nodloop.Query{Kind: "intervention"})
	require.NoError(t, err)

	assert.Equal(t, []string{first}, recordIDs(t, one))
	assert.Equal(t, []string{second}, recordIDs(t, two))
	assert.NotContains(t, string(one.Records[0]), "refund steps")
	assert.Contains(t, string(shown.Records[0]), "refund steps")
	assert.Len(t, recordIDs(t, verdicts), 1)
	assert.NotContains(t, string(verdicts.Records[0]), "the window was missing")
	assert.Len(t, recordIDs(t, items), 1)
	assert.NotContains(t, string(items.Records[0]), "Quote the refund window")
	assert.Equal(t, []string{proposal.ID}, recordIDs(t, interventions))
}

// A query of an unknown kind, a cursor no page answered or a kind outside the record directory fails
func TestClientRecordsRefuses(t *testing.T) {
	tcs := []struct {
		name string
		args nodloop.Query
		want error
	}{
		{"an unknown kind", nodloop.Query{Kind: "session"}, nodloop.ErrKindUnknown},
		{"a cursor no page answered", nodloop.Query{Kind: "run", Since: "%%%"}, nodloop.ErrCursorInvalid},
		{"the guard log of the machine", nodloop.Query{Kind: "guard"}, nodloop.ErrNotServed},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := open(t).Records(context.Background(), tc.args)

			assert.ErrorIs(t, err, tc.want)
		})
	}
}
