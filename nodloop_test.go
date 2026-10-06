package nodloop_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop"
)

var at = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

func open(t *testing.T) *nodloop.Client {
	t.Helper()
	c, err := nodloop.Open(t.TempDir(), nodloop.WithClock(func() time.Time { return at }))
	require.NoError(t, err)
	return c
}

// A run a person corrected teaches an item the next run of the same tenant receives and another tenant does not
func TestClientLoop(t *testing.T) {
	ctx := context.Background()
	c := open(t)
	acme := nodloop.Labels{"tenant": {"acme"}, "task": {"refund"}}
	run, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Labels: acme, Output: []byte("Here are the refund steps")})
	require.NoError(t, err)
	require.NoError(t, c.Judge(ctx, nodloop.Judgment{
		Run: run, Verdict: nodloop.VerdictReject, ReasonCode: nodloop.ReasonScope, Reason: "the refund window was missing",
	}))
	proposed, err := c.Propose(ctx, nodloop.Proposal{
		Kind: nodloop.KindJudgment, Content: "Quote the refund window before the steps", From: run, ID: "refund-window",
	})
	require.NoError(t, err)
	waiting, err := c.Waiting(ctx, "support-bot", acme)
	require.NoError(t, err)

	_, err = c.Approve(ctx, proposed.ID, proposed.Version, "ann")
	require.NoError(t, err)

	items, err := c.Items(ctx, "support-bot", acme)
	require.NoError(t, err)
	other, err := c.Items(ctx, "support-bot", nodloop.Labels{"tenant": {"globex"}, "task": {"refund"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"refund-window"}, ids(waiting))
	assert.Equal(t, []string{"refund-window"}, ids(items))
	assert.Equal(t, acme, items[0].Labels)
	assert.Empty(t, other)
}

func ids(items []nodloop.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestClientPropose(t *testing.T) {
	type args struct {
		proposal nodloop.Proposal
		// Judge the recorded run with a reject first
		corrected bool
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a run without a correction teaches nothing", args{nodloop.Proposal{Kind: nodloop.KindJudgment, Content: "x", From: "{run}"}, false}, nodloop.ErrNotCorrected},
		{"a label no run carries is refused", args{nodloop.Proposal{
			Kind: nodloop.KindJudgment, Content: "x", Producer: "support-bot", Labels: nodloop.Labels{"tenant": {"initech"}}, Runs: []string{"{run}"},
		}, true}, nodloop.ErrScopeUnobserved},
		{"new labels let a new tenant be named before its first run", args{nodloop.Proposal{
			Kind: nodloop.KindJudgment, Content: "x", Producer: "support-bot", Labels: nodloop.Labels{"tenant": {"initech"}}, Runs: []string{"{run}"}, NewLabels: true,
		}, true}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			c := open(t)
			run, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Labels: nodloop.Labels{"tenant": {"acme"}}, Output: []byte("answer")})
			require.NoError(t, err)
			if tc.args.corrected {
				require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: run, Verdict: nodloop.VerdictReject, Reason: "wrong"}))
			}
			p := tc.args.proposal
			if p.From != "" {
				p.From = run
			}
			if len(p.Runs) > 0 {
				p.Runs = []string{run}
			}

			_, err = c.Propose(ctx, p)

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// An approved veto blocks the call it names and lets others through
func TestClientCheckCall(t *testing.T) {
	ctx := context.Background()
	c := open(t)
	run, err := c.Record(ctx, nodloop.Run{Producer: "ops-bot", Labels: nodloop.Labels{"env": {"prod"}}, Output: []byte("ran rm -rf /data")})
	require.NoError(t, err)
	require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: run, Verdict: nodloop.VerdictReject, Reason: "never delete data"}))
	proposed, err := c.Propose(ctx, nodloop.Proposal{
		Kind: nodloop.KindJudgment, Content: "Never delete under /data", From: run, ID: "no-rm-data",
		Veto: &nodloop.VetoRule{
			Tool: "Bash", When: []nodloop.VetoCondition{{Field: "commands", Match: `(?m)^rm .*/data`}},
			Example: map[string]any{"command": "rm -rf /data"},
		},
	})
	require.NoError(t, err)
	_, err = c.Approve(ctx, proposed.ID, proposed.Version, "ann")
	require.NoError(t, err)
	type args struct {
		producer string
		tool     string
		input    map[string]any
	}
	rmData := map[string]any{"command": "cd / && rm -rf /data"}
	blocked := nodloop.Decision{Action: nodloop.ActionBlock, Veto: "no-rm-data", Reason: "Never delete under /data"}
	tcs := []struct {
		name string
		args args
		want nodloop.Decision
	}{
		{"the forbidden call is blocked", args{"ops-bot", "Bash", rmData}, blocked},
		{"another call is allowed", args{"ops-bot", "Bash", map[string]any{"command": "ls /data"}}, nodloop.Decision{Action: nodloop.ActionAllow}},
		{"another tool is allowed", args{"ops-bot", "Read", map[string]any{"file_path": "/data/x"}}, nodloop.Decision{Action: nodloop.ActionAllow}},
		{"another producer is not bound by the veto", args{"docs-bot", "Bash", rmData}, nodloop.Decision{Action: nodloop.ActionAllow}},
		{"no producer checks every veto", args{"", "Bash", rmData}, blocked},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := c.CheckCall(ctx, tc.args.producer, tc.args.tool, tc.args.input)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
