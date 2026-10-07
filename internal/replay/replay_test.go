package replay_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/replay"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Records a run of the repo with the output and the verdict, or none when verdict is empty
func record(t *testing.T, st testkit.Stores, repo, output string, verdict feedback.Verdict) string {
	t.Helper()
	ctx := context.Background()
	out, err := json.Marshal(output)
	require.NoError(t, err)
	run, err := trace.NewRun("session", "", trace.Labels{"repo": {repo}}, json.RawMessage(`{"applied":[]}`), out, st.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, st.Traces.Append(ctx, run))
	if verdict == "" {
		return run.ID
	}
	reason := ""
	if verdict == feedback.VerdictReject {
		reason = "IDV 14 is a valve, not a sensor"
	}
	fb, err := feedback.New(run.ID, verdict, "", reason, nil, "ann", st.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, st.Feedback.Append(ctx, fb))
	return run.ID
}

// A corrected run, two approved runs of the repo, one of another repo and one without a verdict
// The lesson cites the corrected run
type seeded struct {
	st                        testkit.Stores
	corrected, near, far, off string
}

func seed(t *testing.T) seeded {
	t.Helper()
	st := testkit.Open(t)
	s := seeded{st: st}
	s.corrected = record(t, st, "plant", "IDV 14 reads as a sensor fault", feedback.VerdictReject)
	s.near = record(t, st, "plant", "IDV 14 is the cooling valve", feedback.VerdictApprove)
	s.far = record(t, st, "plant", "IDV 11 is a temperature drift", feedback.VerdictApprove)
	s.off = record(t, st, "other", "IDV 14 there is a pump", feedback.VerdictApprove)
	record(t, st, "plant", "no verdict yet", "")
	_, _, err := st.Ledger.Propose(context.Background(), knowledge.Knowledge{
		ID: "idv", Kind: knowledge.KindMeaning, Content: "Every IDV fault is a valve fault", Author: "author",
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"plant"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{s.corrected}},
	})
	require.NoError(t, err)
	return s
}

func TestReplayerReplay(t *testing.T) {
	s := seed(t)
	record(t, s.st, "plant", "a later answer that followed the lesson", feedback.VerdictApprove)
	client := llmmock.NewMockClient(gomock.NewController(t))
	var prompt string
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
		prompt = req.Prompt
		assert.Equal(t, replay.Rules, req.System)
		return llm.Response{CostUSD: 0.02, Output: json.RawMessage(fmt.Sprintf(`{"cases":[`+
			`{"run":%q,"breaks":true,"why":"it called the valve a sensor"},`+
			`{"run":%q,"breaks":false,"why":"it already says valve"},`+
			`{"run":%q,"breaks":true,"why":"IDV 11 would become a valve"}]}`, s.corrected, s.near, s.far))}, nil
	})
	r := replay.New(s.st.Ledger, s.st.Traces, s.st.Feedback, client, "", s.st.Clock.Now)

	got, err := r.Replay(context.Background(), "idv", 0)

	require.NoError(t, err)
	assert.Equal(t, replay.Result{ID: "idv", Version: 1, Missed: 0, Overreach: 1, Cases: []replay.Case{
		{Run: s.corrected, Expect: replay.ExpectBreaks, Breaks: true, Why: "it called the valve a sensor"},
		{Run: s.far, Expect: replay.ExpectKeeps, Breaks: true, Why: "IDV 11 would become a valve"},
		{Run: s.near, Expect: replay.ExpectKeeps, Breaks: false, Why: "it already says valve"},
	}}, got)
	assert.False(t, got.Passed(), "a lesson that would change an approved output reaches too far")
	assert.Contains(t, prompt, "[idv v1 meaning] Every IDV fault is a valve fault")
	assert.NotContains(t, prompt, "IDV 14 there is a pump", "a run outside the scope is no case")
	assert.NotContains(t, prompt, "a later answer", "a run after the version may have followed it so it is no case")
	assert.NotContains(t, prompt, "keeps", "the judge never sees what a case expects")
	traces, err := s.st.Traces.List(context.Background(), trace.Filter{Name: trace.NameReplay})
	require.NoError(t, err)
	require.Len(t, traces, 1)
	assert.Equal(t, "idv", traces[0].Subject)
	assert.InDelta(t, 0.02, traces[0].Usage.CostUSD, 1e-9)
	var recorded replay.Result
	require.NoError(t, json.Unmarshal(traces[0].Output, &recorded))
	assert.Equal(t, got, recorded)
}

// A reaffirm writes a newer record of the version and never moves the time runs began to receive it
func TestReplayerReplayAfterReaffirm(t *testing.T) {
	s := seed(t)
	ctx := context.Background()
	_, err := s.st.Ledger.Approve(ctx, "idv", 1, "ann")
	require.NoError(t, err)
	record(t, s.st, "plant", "an answer that received the lesson", feedback.VerdictApprove)
	_, err = s.st.Ledger.Reaffirm(ctx, "idv", 1, "ann")
	require.NoError(t, err)
	client := llmmock.NewMockClient(gomock.NewController(t))
	var prompt string
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
		prompt = req.Prompt
		return llm.Response{Output: json.RawMessage(fmt.Sprintf(`{"cases":[`+
			`{"run":%q,"breaks":true,"why":"x"},{"run":%q,"breaks":false,"why":"x"},{"run":%q,"breaks":false,"why":"x"}]}`, s.corrected, s.near, s.far))}, nil
	})

	_, err = replay.New(s.st.Ledger, s.st.Traces, s.st.Feedback, client, "", s.st.Clock.Now).Replay(ctx, "idv", 0)

	require.NoError(t, err)
	assert.NotContains(t, prompt, "received the lesson", "a run after the approval may have followed the lesson")
	assert.Contains(t, prompt, "IDV 11 is a temperature drift")
}

func TestReplayerReplayFails(t *testing.T) {
	type args struct {
		id string
		// The judge answers every case but this run
		omit string
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"an unknown id is not found", args{id: "nope"}, knowledge.ErrNotFound},
		{"an answer left out refuses the judgment", args{id: "idv", omit: "corrected"}, replay.ErrJudgment},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := seed(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
				var answers []string
				for name, id := range map[string]string{"corrected": s.corrected, "near": s.near, "far": s.far} {
					if name != tc.args.omit && strings.Contains(req.Prompt, id) {
						answers = append(answers, fmt.Sprintf(`{"run":%q,"breaks":false,"why":"x"}`, id))
					}
				}
				return llm.Response{Output: json.RawMessage(`{"cases":[` + strings.Join(answers, ",") + `]}`)}, nil
			}).AnyTimes()
			r := replay.New(s.st.Ledger, s.st.Traces, s.st.Feedback, client, "", s.st.Clock.Now)

			_, err := r.Replay(context.Background(), tc.args.id, 0)

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// A lesson whose run nobody corrected anymore and whose scope holds no approved run has nothing to judge
func TestReplayerReplayWithoutCases(t *testing.T) {
	st := testkit.Open(t)
	run := record(t, st, "plant", "answer", feedback.VerdictReject)
	_, _, err := st.Ledger.Propose(context.Background(), knowledge.Knowledge{
		ID: "lone", Kind: knowledge.KindMeaning, Content: "x", Author: "author",
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"plant"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run}},
	})
	require.NoError(t, err)
	fb, err := feedback.New(run, feedback.VerdictWithdraw, "", "", nil, "ann", st.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, st.Feedback.Append(context.Background(), fb))
	client := llmmock.NewMockClient(gomock.NewController(t))

	_, err = replay.New(st.Ledger, st.Traces, st.Feedback, client, "", st.Clock.Now).Replay(context.Background(), "lone", 0)

	assert.ErrorIs(t, err, replay.ErrNoCases)
}
