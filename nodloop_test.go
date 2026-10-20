package nodloop_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

var at = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

func open(t *testing.T) *nodloop.Client {
	t.Helper()
	c, _ := openIn(t)
	return c
}

// A client over a new record directory and the directory
func openIn(t *testing.T) (*nodloop.Client, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := nodloop.Open(dir, nodloop.WithClock(func() time.Time { return at }))
	require.NoError(t, err)
	return c, dir
}

// Approves the version as the person ann at her terminal over the records a client writes
// The package itself records only proposals since a call in process proves no person
func approveAsPerson(t *testing.T, dir, id string, version int) {
	t.Helper()
	store, err := knowledgefile.New(dir)
	require.NoError(t, err)
	now := func() time.Time { return at }
	_, err = knowledge.NewLedger(store, vetofile.NewApprovedFile("", ""), now, knowledge.NewIDs(now)).Approve(context.Background(), id, version, "ann")
	require.NoError(t, err)
}

// A run a person corrected teaches an item the next run of the same tenant receives once the person approved it
// A run of another tenant never receives it
func TestClientLoop(t *testing.T) {
	ctx := context.Background()
	c, dir := openIn(t)
	acme := nodloop.Labels{"tenant": {"acme"}, "task": {"refund"}}
	run, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Labels: acme, Output: []byte("Here are the refund steps")})
	require.NoError(t, err)
	require.NoError(t, c.Judge(ctx, nodloop.Judgment{
		Run: run, Verdict: nodloop.VerdictReject, ReasonCode: nodloop.ReasonScope, Reason: "the refund window was missing", Reviewer: "ann",
	}))
	proposed, err := c.Propose(ctx, nodloop.Proposal{
		Kind: nodloop.KindJudgment, Content: "Quote the refund window before the steps", From: run, ID: "refund-window",
	})
	require.NoError(t, err)
	waiting, err := c.Waiting(ctx, "support-bot", acme)
	require.NoError(t, err)
	proposal, err := c.Approve(ctx, proposed.ID, proposed.Version, "ann")
	require.NoError(t, err)
	before, err := c.Items(ctx, "support-bot", acme)
	require.NoError(t, err)

	approveAsPerson(t, dir, proposed.ID, proposed.Version)

	items, err := c.Items(ctx, "support-bot", acme)
	require.NoError(t, err)
	other, err := c.Items(ctx, "support-bot", nodloop.Labels{"tenant": {"globex"}, "task": {"refund"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"refund-window"}, ids(waiting))
	assert.Equal(t, nodloop.AuthorityAgent, proposal.Authority)
	assert.Empty(t, before, "a proposal changes nothing a run receives")
	assert.Equal(t, []string{"refund-window"}, ids(items))
	assert.Equal(t, acme, items[0].Labels)
	assert.Empty(t, other)
}

// Approve, ApproveIn and Retire record an agent proposal and change no status
func TestClientJudgmentsProveNoPerson(t *testing.T) {
	type want struct {
		choice nodloop.Choice
		err    error
	}
	tcs := []struct {
		name string
		args func(ctx context.Context, c *nodloop.Client, id string) (nodloop.Intervention, error)
		want want
	}{
		{"approve records a proposal", func(ctx context.Context, c *nodloop.Client, id string) (nodloop.Intervention, error) {
			return c.Approve(ctx, id, 1, "ann")
		}, want{choice: nodloop.ChoiceKeep}},
		{"approve in labels records a proposal", func(ctx context.Context, c *nodloop.Client, id string) (nodloop.Intervention, error) {
			return c.ApproveIn(ctx, id, 1, nodloop.Labels{}, "ann")
		}, want{choice: nodloop.ChoiceScope}},
		{"retire records a proposal", func(ctx context.Context, c *nodloop.Client, id string) (nodloop.Intervention, error) {
			return c.Retire(ctx, id, 1, "ann")
		}, want{choice: nodloop.ChoiceUndo}},
		{"the reserved name is refused", func(ctx context.Context, c *nodloop.Client, id string) (nodloop.Intervention, error) {
			return c.Approve(ctx, id, 1, "nodloop")
		}, want{err: nodloop.ErrApproverReserved}},
		{"an unknown version is refused", func(ctx context.Context, c *nodloop.Client, id string) (nodloop.Intervention, error) {
			return c.Retire(ctx, id, 9, "ann")
		}, want{err: nodloop.ErrNotFound}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			c := open(t)
			run, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Labels: nodloop.Labels{"tenant": {"acme"}}, Output: []byte("x")})
			require.NoError(t, err)
			require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: run, Verdict: nodloop.VerdictReject, Reason: "wrong", Reviewer: "ann"}))
			proposed, err := c.Propose(ctx, nodloop.Proposal{Kind: nodloop.KindJudgment, Content: "x", From: run})
			require.NoError(t, err)

			got, err := tc.args(ctx, c, proposed.ID)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.choice, got.Choice)
			waiting, err := c.Waiting(ctx, "support-bot", nodloop.Labels{"tenant": {"acme"}})
			require.NoError(t, err)
			assert.Equal(t, []string{proposed.ID}, ids(waiting), "the candidate still waits")
		})
	}
}

// Intervene records every action but a digest as an agent proposal and refuses what the records cannot carry out
func TestClientIntervene(t *testing.T) {
	value := func(v float64) *float64 { return &v }
	type args struct {
		in func(id string) nodloop.Intervention
	}
	type want struct {
		authority nodloop.Authority
		err       error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a judgment is a proposal", args{func(id string) nodloop.Intervention {
			return nodloop.Intervention{Action: nodloop.InterventionJudge, Target: id + ":1", Choice: nodloop.ChoiceKeep}
		}}, want{nodloop.AuthorityAgent, nil}},
		{"a pause is a proposal", args{func(string) nodloop.Intervention {
			return nodloop.Intervention{Action: nodloop.InterventionPause, Labels: nodloop.Labels{"tenant": {"acme"}}}
		}}, want{nodloop.AuthorityAgent, nil}},
		{"a digest needs no proof", args{func(string) nodloop.Intervention {
			return nodloop.Intervention{Action: nodloop.InterventionDigest}
		}}, want{nodloop.AuthorityAgent, nil}},
		{"a knob outside its range is refused", args{func(string) nodloop.Intervention {
			return nodloop.Intervention{Action: nodloop.InterventionKnob, Target: "item_hold", Value: value(0.9)}
		}}, want{err: nodloop.ErrInterventionRefused}},
		{"an unknown version is refused", args{func(id string) nodloop.Intervention {
			return nodloop.Intervention{Action: nodloop.InterventionJudge, Target: id + ":9", Choice: nodloop.ChoiceKeep}
		}}, want{err: nodloop.ErrNotFound}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			c := open(t)
			run, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Labels: nodloop.Labels{"tenant": {"acme"}}, Output: []byte("x")})
			require.NoError(t, err)
			require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: run, Verdict: nodloop.VerdictReject, Reason: "wrong", Reviewer: "ann"}))
			proposed, err := c.Propose(ctx, nodloop.Proposal{Kind: nodloop.KindJudgment, Content: "x", From: run})
			require.NoError(t, err)

			got, err := c.Intervene(ctx, tc.args.in(proposed.ID))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.authority, got.Authority)
			assert.Equal(t, tc.want.err == nil, got.ID != "")
			waiting, err := c.Waiting(ctx, "support-bot", nodloop.Labels{"tenant": {"acme"}})
			require.NoError(t, err)
			assert.Equal(t, []string{proposed.ID}, ids(waiting), "the candidate still waits")
		})
	}
}

// A judgment in process proves no person so a named one carries proof none and report loop counts it apart from a proven one
func TestClientJudgeProvesNoPerson(t *testing.T) {
	ctx := context.Background()
	c, dir := openIn(t)
	var runs []string
	for range 2 {
		run, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Output: []byte("x")})
		require.NoError(t, err)
		runs = append(runs, run)
	}

	require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: runs[0], Verdict: nodloop.VerdictApprove, Reviewer: "ann"}))
	require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: runs[1], Verdict: nodloop.VerdictApprove, Reviewer: "session"}))

	store, err := feedbackfile.New(dir)
	require.NoError(t, err)
	found, err := store.List(ctx, record.FeedbackFilter{})
	require.NoError(t, err)
	proofs := map[string]string{}
	for _, fb := range found {
		proofs[fb.Reviewer] = fb.Proof
	}
	report, err := c.Report(ctx, "loop")
	require.NoError(t, err)
	var got struct {
		Totals struct {
			Judged   int `json:"judged"`
			Named    int `json:"named"`
			Inferred int `json:"inferred"`
		} `json:"totals"`
	}
	require.NoError(t, json.Unmarshal(report, &got))
	assert.Equal(t, map[string]string{"ann": "none", "session": ""}, proofs)
	assert.Equal(t, [3]int{0, 1, 1}, [3]int{got.Totals.Judged, got.Totals.Named, got.Totals.Inferred})
}

// A judgment and an outcome without a reviewer or under the reserved name of the capture are refused before anything is recorded
func TestClientReviewerRequired(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"no reviewer is refused", "", nodloop.ErrReviewerRequired},
		{"the reserved name of the capture is refused", "nodloop", nodloop.ErrReviewerReserved},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			c, dir := openIn(t)
			run, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Output: []byte("x")})
			require.NoError(t, err)

			judged := c.Judge(ctx, nodloop.Judgment{Run: run, Verdict: nodloop.VerdictReject, Reason: "wrong", Reviewer: tc.args})
			checked := c.Outcome(ctx, nodloop.Outcome{Run: run, Result: nodloop.ResultConfirmed, Reviewer: tc.args})

			assert.ErrorIs(t, judged, tc.want)
			assert.ErrorIs(t, checked, tc.want)
			verdicts, err := feedbackfile.New(dir)
			require.NoError(t, err)
			found, err := verdicts.List(ctx, record.FeedbackFilter{})
			require.NoError(t, err)
			outcomes, err := feedbackfile.NewOutcomeStore(dir)
			require.NoError(t, err)
			checks, err := outcomes.List(ctx, "")
			require.NoError(t, err)
			assert.Empty(t, found)
			assert.Empty(t, checks)
		})
	}
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
				require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: run, Verdict: nodloop.VerdictReject, Reason: "wrong", Reviewer: "ann"}))
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

// ApproveIn proposes a candidate where the person picked, every tenant or one a run carries, and reaches no run until the person decides
func TestClientApproveIn(t *testing.T) {
	type args struct {
		labels nodloop.Labels
	}
	type want struct {
		err error
		// Whether a run of another tenant receives the item
		globex bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"empty labels are proposed for every tenant", args{nodloop.Labels{}}, want{}},
		{"a recorded tenant is proposed", args{nodloop.Labels{"tenant": {"acme"}}}, want{}},
		{"a tenant no run carries is refused", args{nodloop.Labels{"tenant": {"initech"}}}, want{err: nodloop.ErrScopeUnobserved}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			c := open(t)
			acme := nodloop.Labels{"tenant": {"acme"}, "task": {"refund"}}
			run, err := c.Record(ctx, nodloop.Run{Producer: "support-bot", Labels: acme, Output: []byte("Here are the refund steps")})
			require.NoError(t, err)
			_, err = c.Record(ctx, nodloop.Run{Producer: "support-bot", Labels: nodloop.Labels{"tenant": {"globex"}}, Output: []byte("ok")})
			require.NoError(t, err)
			require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: run, Verdict: nodloop.VerdictReject, Reason: "too long", Reviewer: "ann"}))
			proposed, err := c.Propose(ctx, nodloop.Proposal{Kind: nodloop.KindJudgment, Content: "Answer in three lines", From: run})
			require.NoError(t, err)

			_, err = c.ApproveIn(ctx, proposed.ID, proposed.Version, tc.args.labels, "ann")

			require.ErrorIs(t, err, tc.want.err)
			other, err := c.Items(ctx, "support-bot", nodloop.Labels{"tenant": {"globex"}})
			require.NoError(t, err)
			assert.Equal(t, tc.want.globex, len(other) == 1)
		})
	}
}

// An approved veto blocks the call it names and lets others through
func TestClientCheckCall(t *testing.T) {
	ctx := context.Background()
	c, dir := openIn(t)
	run, err := c.Record(ctx, nodloop.Run{Producer: "ops-bot", Labels: nodloop.Labels{"env": {"prod"}}, Output: []byte("ran rm -rf /data")})
	require.NoError(t, err)
	require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: run, Verdict: nodloop.VerdictReject, Reason: "never delete data", Reviewer: "ann"}))
	proposed, err := c.Propose(ctx, nodloop.Proposal{
		Kind: nodloop.KindJudgment, Content: "Never delete under /data", From: run, ID: "no-rm-data",
		Veto: &nodloop.VetoRule{
			Tool: "Bash", When: []nodloop.VetoCondition{{Field: "commands", Match: `(?m)^rm .*/data`}},
			Example: map[string]any{"command": "rm -rf /data"},
		},
	})
	require.NoError(t, err)
	approveAsPerson(t, dir, proposed.ID, proposed.Version)
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
