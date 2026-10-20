package nodloop_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop"
)

// A client with an approved item for tenant acme
func taught(t *testing.T, opts ...nodloop.Option) (*nodloop.Client, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	c, err := nodloop.Open(dir, append([]nodloop.Option{nodloop.WithClock(func() time.Time { return at })}, opts...)...)
	require.NoError(t, err)
	run, err := c.Record(ctx, nodloop.Run{Producer: "stream-bot", Labels: nodloop.Labels{"tenant": {"acme"}}, Output: []byte("lag rose")})
	require.NoError(t, err)
	require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: run, Verdict: nodloop.VerdictReject, Reason: "name the partition", Reviewer: "ann"}))
	proposed, err := c.Propose(ctx, nodloop.Proposal{Kind: nodloop.KindJudgment, Content: "Name the partition", From: run, ID: "partition"})
	require.NoError(t, err)
	approveAsPerson(t, dir, proposed.ID, proposed.Version)
	return c, dir
}

// A drawn key gets the refs of every item the run would have received and no item
func TestClientDraw(t *testing.T) {
	type args struct {
		share float64
		key   string
	}
	type want struct {
		items    []string
		withheld []nodloop.Ref
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a drawn key gets the withheld refs", args{0.999, "window-1"}, want{[]string{}, []nodloop.Ref{{ID: "partition", Version: 1}}}},
		{"an empty key never withholds", args{0.999, ""}, want{[]string{"partition"}, nil}},
		{"no holdout never withholds", args{0, "window-1"}, want{[]string{"partition"}, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := taught(t, nodloop.WithHoldout(tc.args.share))

			d, err := c.Draw(context.Background(), "stream-bot", nodloop.Labels{"tenant": {"acme"}}, tc.args.key)

			require.NoError(t, err)
			assert.Equal(t, tc.want.items, ids(d.Items))
			assert.Equal(t, tc.want.withheld, d.Withheld)
		})
	}
}

// A run recorded with the withheld refs of a draw stores them beside an empty applied
func TestClientRecordWithheld(t *testing.T) {
	ctx := context.Background()
	c, dir := taught(t, nodloop.WithHoldout(0.999))
	d, err := c.Draw(ctx, "stream-bot", nodloop.Labels{"tenant": {"acme"}}, "window-1")
	require.NoError(t, err)

	_, err = c.Record(ctx, nodloop.Run{
		Producer: "stream-bot", Labels: nodloop.Labels{"tenant": {"acme"}}, Output: []byte("x"), Withheld: d.Withheld, HoldoutKey: "window-1",
	})

	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(dir, "traces.jsonl"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var last struct {
		Input json.RawMessage `json:"input"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &last))
	assert.JSONEq(t, `{"applied":[],"withheld":[{"id":"partition","version":1}],"drawn":true,"share":0.999,"scheme":1,"unit":"9764aa8e55e52d75"}`, string(last.Input))
}

// The model of a run lands in its trace and a run without one records none
func TestClientRecordModel(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"the model of the run lands in the trace", "gpt-4o-mini", "gpt-4o-mini"},
		{"a run without a model records none", "", ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			c, err := nodloop.Open(dir, nodloop.WithClock(func() time.Time { return at }))
			require.NoError(t, err)

			_, err = c.Record(context.Background(), nodloop.Run{Producer: "stream-bot", Output: []byte("x"), Model: tc.args})

			require.NoError(t, err)
			b, err := os.ReadFile(filepath.Join(dir, "traces.jsonl"))
			require.NoError(t, err)
			var run struct {
				Model string `json:"model"`
			}
			require.NoError(t, json.Unmarshal(b, &run))
			assert.Equal(t, tc.want, run.Model)
		})
	}
}

// The duration of a run lands in its trace and a negative one records nothing
func TestClientRecordDuration(t *testing.T) {
	type want struct {
		ms  int64
		err error
	}
	tcs := []struct {
		name string
		args time.Duration
		want want
	}{
		{"the duration of the run lands in the trace", 2500 * time.Millisecond, want{2500, nil}},
		{"a run without a duration records none", 0, want{0, nil}},
		{"a negative duration is refused", -time.Second, want{0, nodloop.ErrDurationNegative}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			c, err := nodloop.Open(dir, nodloop.WithClock(func() time.Time { return at }))
			require.NoError(t, err)

			_, err = c.Record(context.Background(), nodloop.Run{Producer: "stream-bot", Output: []byte("x"), Duration: tc.args})

			require.ErrorIs(t, err, tc.want.err)
			if tc.want.err != nil {
				return
			}
			b, err := os.ReadFile(filepath.Join(dir, "traces.jsonl"))
			require.NoError(t, err)
			var run struct {
				DurationMS int64 `json:"duration_ms"`
			}
			require.NoError(t, json.Unmarshal(b, &run))
			assert.Equal(t, tc.want.ms, run.DurationMS)
		})
	}
}

// Report effect counts a run in its arm only when Record names the key it drew with
func TestClientReportEffect(t *testing.T) {
	type want struct {
		runs, undrawn int
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"a run recorded with its key counts in the withheld arm", "window-1", want{1, 0}},
		{"a run recorded without a key is undrawn", "", want{0, 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			c, _ := taught(t, nodloop.WithHoldout(0.999))
			d, err := c.Draw(ctx, "stream-bot", nodloop.Labels{"tenant": {"acme"}}, "window-1")
			require.NoError(t, err)
			_, err = c.Record(ctx, nodloop.Run{
				Producer: "stream-bot", Labels: nodloop.Labels{"tenant": {"acme"}}, Output: []byte("x"), Withheld: d.Withheld, HoldoutKey: tc.args,
			})
			require.NoError(t, err)

			report, err := c.Report(ctx, "effect")

			require.NoError(t, err)
			var rows []struct {
				Arm     string `json:"arm"`
				Runs    int    `json:"runs"`
				Undrawn int    `json:"undrawn"`
			}
			require.NoError(t, json.Unmarshal(report, &rows))
			require.Len(t, rows, 2)
			assert.Equal(t, "withheld", rows[1].Arm)
			assert.Equal(t, tc.want, want{rows[1].Runs, rows[1].Undrawn})
		})
	}
}

func TestClientReportRefusesAnUnknownName(t *testing.T) {
	_, err := open(t).Report(context.Background(), "nope")

	assert.ErrorIs(t, err, nodloop.ErrReportUnknown)
}

// An outcome lands on a recorded run and a run that does not exist or a result outside the set is refused
func TestClientOutcome(t *testing.T) {
	type args struct {
		// {recorded} stands for the id of the run the test records
		run    string
		result nodloop.Result
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a confirmed outcome of a recorded run", args{"{recorded}", nodloop.ResultConfirmed}, nil},
		{"an unknown result is refused", args{"{recorded}", "maybe"}, nodloop.ErrResultUnknown},
		{"a run that does not exist is refused", args{"no-such-run", nodloop.ResultRefuted}, nodloop.ErrRunNotFound},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			c := open(t)
			id, err := c.Record(ctx, nodloop.Run{Producer: "stream-bot", Output: []byte("x")})
			require.NoError(t, err)

			err = c.Outcome(ctx, nodloop.Outcome{Run: strings.ReplaceAll(tc.args.run, "{recorded}", id), Result: tc.args.result, Reviewer: "ann"})

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// A directory a service pruned under WithRetention keeps the cutoff so a client without retention reads the same window
func TestClientReportReadsTheWindowOfTheLastPrune(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	clock := at.Add(-40 * 24 * time.Hour)
	now := nodloop.WithClock(func() time.Time { return clock })
	service, err := nodloop.Open(dir, now, nodloop.WithRetention(30))
	require.NoError(t, err)
	_, err = service.Record(ctx, nodloop.Run{Producer: "stream-bot", Output: []byte("old")})
	require.NoError(t, err)
	service.WaitPrune()
	clock = at
	_, err = service.Record(ctx, nodloop.Run{Producer: "stream-bot", Output: []byte("recent")})
	require.NoError(t, err)
	service.WaitPrune()
	reader, err := nodloop.Open(dir, now)
	require.NoError(t, err)

	report, err := reader.Report(ctx, "loop")

	require.NoError(t, err)
	var loop struct {
		Totals struct {
			Runs int `json:"runs"`
		} `json:"totals"`
	}
	require.NoError(t, json.Unmarshal(report, &loop))
	assert.Equal(t, 1, loop.Totals.Runs)
}

func TestClientRecordRefusesAppliedAndWithheld(t *testing.T) {
	c := open(t)
	ref := []nodloop.Ref{{ID: "partition", Version: 1}}

	_, err := c.Record(context.Background(), nodloop.Run{Producer: "stream-bot", Output: []byte("x"), Applied: ref, Withheld: ref})

	assert.ErrorIs(t, err, nodloop.ErrWithheldApplied)
}

// With retention a record prunes the unjudged runs past it once a day and keeps the judged run and the recent ones
func TestClientRetention(t *testing.T) {
	ctx := context.Background()
	clock := at.Add(-40 * 24 * time.Hour)
	dir := t.TempDir()
	c, err := nodloop.Open(dir, nodloop.WithClock(func() time.Time { return clock }), nodloop.WithRetention(30))
	require.NoError(t, err)
	judged, err := c.Record(ctx, nodloop.Run{Producer: "stream-bot", Output: []byte("judged")})
	require.NoError(t, err)
	require.NoError(t, c.Judge(ctx, nodloop.Judgment{Run: judged, Verdict: nodloop.VerdictApprove, Reviewer: "ann"}))
	_, err = c.Record(ctx, nodloop.Run{Producer: "stream-bot", Output: []byte("unjudged")})
	require.NoError(t, err)
	c.WaitPrune()
	clock = at

	recent, err := c.Record(ctx, nodloop.Run{Producer: "stream-bot", Output: []byte("recent")})
	require.NoError(t, err)
	c.WaitPrune()

	b, err := os.ReadFile(filepath.Join(dir, "traces.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(b), "\n"))
	assert.Contains(t, string(b), judged)
	assert.Contains(t, string(b), recent)
	assert.NotContains(t, string(b), "unjudged")
}

// A failed daily prune goes to WithPruneError and Record returns the recorded run without waiting for the prune
// The report blocks until Record returned so a Record that waited for the prune would never return
func TestClientRecordReportsAFailedPrune(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var reported []error
	returned := make(chan struct{})
	c, err := nodloop.Open(dir, nodloop.WithClock(func() time.Time { return at }), nodloop.WithRetention(30),
		nodloop.WithPruneError(func(err error) {
			<-returned
			reported = append(reported, err)
		}))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "feedback.jsonl"), []byte("not json\n"), 0o600))

	id, err := c.Record(ctx, nodloop.Run{Producer: "stream-bot", Output: []byte("x")})
	close(returned)
	c.WaitPrune()

	require.NoError(t, err)
	assert.NotEmpty(t, id)
	require.Len(t, reported, 1)
	assert.ErrorContains(t, reported[0], "corrupt")
}

func TestOpenRefusesItsOptions(t *testing.T) {
	tcs := []struct {
		name string
		args nodloop.Option
		want error
	}{
		{"a retention under a week", nodloop.WithRetention(3), nodloop.ErrRetentionTooShort},
		{"a holdout of 1", nodloop.WithHoldout(1), nodloop.ErrHoldoutInvalid},
		{"a negative holdout", nodloop.WithHoldout(-0.1), nodloop.ErrHoldoutInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := nodloop.Open(t.TempDir(), tc.args)

			assert.ErrorIs(t, err, tc.want)
		})
	}
}
