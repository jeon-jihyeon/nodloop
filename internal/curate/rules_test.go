package curate_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A scope lies in a temporary place when every dir it names lies below a temp dir or holds a scratchpad segment
func TestTempDirsHold(t *testing.T) {
	temps := curate.TempDirs{"/tmp", "/var/folders/xy/T/", "relative"}
	scoped := func(dirs ...string) knowledge.RunScope {
		return knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}, "dir": dirs}}
	}
	tcs := []struct {
		name string
		args knowledge.RunScope
		want bool
	}{
		{"a dir below the temp dir", scoped("/tmp/acme"), true},
		{"the temp dir itself", scoped("/tmp"), true},
		{"a dir below a temp dir named with a trailing slash", scoped("/var/folders/xy/T/run"), true},
		{"a scratchpad segment anywhere", scoped("/work/scratchpad/notes"), true},
		{"a dir whose name only starts like the temp dir", scoped("/tmpfiles/acme"), false},
		{"a tmp folder inside a repository", scoped("tmp/notes"), false},
		{"a relative dir below a relative entry is no temp dir of the OS", scoped("relative/acme"), false},
		{"one dir of two outside the temp dir", scoped("/tmp/acme", "/work/acme"), false},
		{"a scope with no dir", knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, temps.Hold(tc.args))
		})
	}
}

// The few-shot pool is the person's own newest decision of each version, an approval as drafted or a retire for its direction
func TestAnchorShots(t *testing.T) {
	approved, retired := knowledge.StatusApproved, knowledge.StatusRetired
	rec := func(id string, status knowledge.Status, approver string, drafted bool, reason knowledge.Reason, at time.Time) knowledge.Knowledge {
		k := lesson(id, 1, "lesson "+id, acme("acme"), status, approver)
		k.Drafted, k.Reason, k.Time = drafted, reason, at
		if status == approved {
			k.ApprovedAt = at
		}
		return k
	}
	later := written.Add(time.Hour)
	reaffirmed := rec("k-reaffirmed", approved, "ann", true, "", later)
	reaffirmed.ApprovedAt = written
	all := knowledge.Set{
		rec("k-later", retired, "ann", true, knowledge.ReasonPerson, later),
		rec("k-later", approved, "ann", true, "", written),
		rec("k-legacy", retired, "ann", true, "", later),
		rec("k-legacy", approved, "ann", true, "", written),
		rec("k-kept", approved, "ann", true, "", written),
		rec("k-rewritten", approved, "ann", false, "", written),
		rec("k-carried", approved, knowledge.ApproverNodloop, true, "", written),
		rec("k-dropped", retired, knowledge.ApproverNodloop, true, knowledge.ReasonDuplicate, written),
		reaffirmed,
		rec("k-reaffirmed", approved, "ann", true, "", written),
	}

	tcs := []struct {
		name string
		// The start of the configuration the pool is drawn for
		args time.Time
		want curate.Shots
	}{
		{"a start after every decision reads the newest decision of each version", later.Add(time.Minute), curate.Shots{
			{Ref: ref("k-later"), At: later, Content: "lesson k-later"},
			{Ref: ref("k-kept"), At: written, Content: "lesson k-kept", Kept: true},
			{Ref: ref("k-reaffirmed"), At: written, Content: "lesson k-reaffirmed", Kept: true},
		}},
		{"a version retired after the start still shows as approved as drafted", written.Add(time.Minute), curate.Shots{
			{Ref: ref("k-later"), At: written, Content: "lesson k-later", Kept: true},
			{Ref: ref("k-legacy"), At: written, Content: "lesson k-legacy", Kept: true},
			{Ref: ref("k-kept"), At: written, Content: "lesson k-kept", Kept: true},
			{Ref: ref("k-reaffirmed"), At: written, Content: "lesson k-reaffirmed", Kept: true},
		}},
		{"a start before every decision reads none", written, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, curate.NewAnchor(all, nil, nil).Shots(tc.args))
		})
	}
}

// A version of a few-shot set that the person retired after the configuration started keeps its example and the judge still runs
func TestCuratorPassShotsAsOfStart(t *testing.T) {
	ctx := context.Background()
	b := newBench(t, nil, []knowledge.Knowledge{
		lesson("k-shot", 1, "Keep commits small", acme("acme"), knowledge.StatusApproved, "ann"),
		lesson("k-first", 1, "Name the failing test first", acme("acme"), knowledge.StatusCandidate, ""),
	}, nil)
	var prompts []string
	b.client = llmmock.NewMockClient(gomock.NewController(t))
	b.client.EXPECT().Complete(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
		prompts = append(prompts, req.Prompt)
		return llm.Response{Output: answer(0.9, 0.02), Model: "claude-sonnet-5-5"}, nil
	})
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	require.NoError(t, testkit.Err(b.stores.Ledger.Retire(ctx, "k-shot", 1, "ann")))
	b.correct(t, "r-k-second", trace.Labels{"repo": {"acme"}}, []feedback.Verdict{feedback.VerdictReject})
	second := lesson("k-second", 1, "Pin the base image", acme("acme"), knowledge.StatusCandidate, "")
	require.NoError(t, testkit.Err(b.stores.Ledger.Import(ctx, []knowledge.Knowledge{second})))
	prompts = nil

	again, err := b.curator().Pass(ctx, b.setting(t))

	require.NoError(t, err)
	assert.Equal(t, []string{"add k-first v1 shadow  1"}, decisionsOf(first.Decided))
	assert.Equal(t, []string{"add k-second v1 shadow  1"}, decisionsOf(again.Decided))
	require.Len(t, prompts, 3)
	assert.Contains(t, prompts[0], "- approved as drafted: Keep commits small\n")
	assert.NotContains(t, prompts[0], "retired: Keep commits small")
}

// A trace reads back as a decide or a knob event only when it is one that decodes
func TestRestoreDecideAndKnob(t *testing.T) {
	decide := trace.Trace{
		ID: "d1", Name: trace.NameDecide, Ref: "r-1", Time: written,
		Input: json.RawMessage(`{"kind":"drop","id":"k-1","version":1,"anchor":1}`), Output: json.RawMessage(`{"tier":0,"action":"drop"}`),
		Draw: &trace.Draw{Drawn: true},
	}
	knob := trace.Trace{ID: "n1", Name: trace.NameKnob, Time: written, Input: json.RawMessage(`{"cycle_id":"2026-09-21","seed":"5eed"}`)}
	type want struct {
		decide, knob bool
	}
	tcs := []struct {
		name string
		args trace.Trace
		want want
	}{
		{"a decide", decide, want{true, false}},
		{"a knob event", knob, want{false, true}},
		{"a decide whose output does not decode", trace.Trace{Name: trace.NameDecide, Input: decide.Input, Output: json.RawMessage(`[`)}, want{false, false}},
		{"a knob event with no cycle", trace.Trace{Name: trace.NameKnob, Input: json.RawMessage(`{}`)}, want{false, false}},
		{"a trace of another name", trace.Trace{Name: trace.NameRun, Input: decide.Input, Output: decide.Output}, want{false, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, isDecide := curate.RestoreDecide(tc.args)
			_, isKnob := curate.RestoreKnob(tc.args)
			assert.Equal(t, tc.want, want{isDecide, isKnob})
		})
	}
	got, _ := curate.RestoreDecide(decide)
	assert.Equal(t, curate.Decided{
		ID: "d1", Time: written, Ref: "r-1", Change: curate.Change{Kind: curate.ChangeDrop, ID: "k-1", Version: 1, Anchor: 1},
		Decision: curate.Decision{Action: curate.ActionDrop}, Drawn: true,
	}, got)
}
