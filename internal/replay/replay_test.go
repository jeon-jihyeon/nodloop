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

// Records a run of the repo with the output and the verdict
// An empty verdict records none
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
// The lesson idv cites the corrected run and a run the records no longer hold
// The lesson guard acts through a veto
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
	scope := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"plant"}}}
	for _, k := range []knowledge.Knowledge{
		{
			ID: "idv", Kind: knowledge.KindMeaning, Content: "Every IDV fault is a valve fault", Author: "author", Run: scope,
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{s.corrected, "gone"}},
		},
		{
			ID: "guard", Kind: knowledge.KindJudgment, Content: "never run cmd", Author: "author", Run: scope,
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{s.corrected}},
			Veto: &knowledge.Veto{
				Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: `^cmd\b`}}, Example: map[string]any{"command": "cmd now"},
			},
		},
	} {
		_, _, err := st.Ledger.Propose(context.Background(), k)
		require.NoError(t, err)
	}
	return s
}

// Answers every run the prompt shows with breaks false
func answerAll(_ context.Context, req llm.Request) (llm.Response, error) {
	var answers []string
	for _, line := range strings.Split(req.Prompt, "\n") {
		if id, ok := strings.CutPrefix(line, "### run "); ok {
			answers = append(answers, fmt.Sprintf(`{"run":%q,"breaks":false,"why":"x"}`, id))
		}
	}
	return llm.Response{CostUSD: 0.01, Output: json.RawMessage(`{"cases":[` + strings.Join(answers, ",") + `]}`)}, nil
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
		{Run: s.near, Expect: replay.ExpectKeeps, Breaks: false, Why: "it already says valve"},
		{Run: s.far, Expect: replay.ExpectKeeps, Breaks: true, Why: "IDV 11 would become a valve"},
	}}, got, "cases come in the order of their run ids")
	assert.False(t, got.Passed(), "a lesson that would change an approved output reaches too far")
	assert.Contains(t, prompt, "[idv v1 meaning] Every IDV fault is a valve fault")
	assert.NotContains(t, prompt, "IDV 14 there is a pump", "a run outside the scope is no case")
	assert.NotContains(t, prompt, "a later answer", "a run after the version may have followed it so it is no case")
	assert.NotContains(t, prompt, "keeps", "the judge never sees what a case expects")
	assert.NotContains(t, prompt, "gone", "a cited run the records no longer hold is no case")
	assert.Less(t, strings.Index(prompt, s.near), strings.Index(prompt, s.far), "the outputs come in the order of their run ids")
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
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, req llm.Request) (llm.Response, error) {
		prompt = req.Prompt
		return answerAll(ctx, req)
	})

	_, err = replay.New(s.st.Ledger, s.st.Traces, s.st.Feedback, client, "", s.st.Clock.Now).Replay(ctx, "idv", 0)

	require.NoError(t, err)
	assert.NotContains(t, prompt, "received the lesson", "a run after the approval may have followed the lesson")
	assert.Contains(t, prompt, "IDV 11 is a temperature drift")
}

// Only the newest approved runs of the scope are judged so one model call holds them
func TestReplayerReplayCapsApprovedRuns(t *testing.T) {
	st := testkit.Open(t)
	corrected := record(t, st, "plant", "wrong", feedback.VerdictReject)
	var approved []string
	for i := range 12 {
		approved = append(approved, record(t, st, "plant", fmt.Sprintf("answer %d", i), feedback.VerdictApprove))
	}
	_, _, err := st.Ledger.Propose(context.Background(), knowledge.Knowledge{
		ID: "wide", Kind: knowledge.KindMeaning, Content: "x", Author: "author",
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"plant"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{corrected}},
	})
	require.NoError(t, err)
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(answerAll)

	got, err := replay.New(st.Ledger, st.Traces, st.Feedback, client, "", st.Clock.Now).Replay(context.Background(), "wide", 0)

	require.NoError(t, err)
	var runs []string
	for _, c := range got.Cases {
		runs = append(runs, c.Run)
	}
	assert.Equal(t, append([]string{corrected}, approved[2:]...), runs, "the two oldest approved runs are left out")
}

// Fails every append so a test sees the error of recording the judgment
type unrecorded struct {
	replay.TraceStore
}

func (unrecorded) Append(context.Context, trace.Trace) error {
	return assert.AnError
}

// Adds the record loose without a run scope as a ledger written before 0.6.0 may hold
type withLoose struct {
	replay.KnowledgeStore
}

func (w withLoose) All(ctx context.Context) (knowledge.Set, error) {
	all, err := w.KnowledgeStore.All(ctx)
	return append(all, knowledge.Knowledge{ID: "loose", Version: 1, Kind: knowledge.KindMeaning, Status: knowledge.StatusCandidate}), err
}

func TestReplayerReplayFails(t *testing.T) {
	type args struct {
		id      string
		version int
		// Answers the request of the judge
		judge func(ctx context.Context, req llm.Request) (llm.Response, error)
		// The stores the replayer reads and records through
		stores func(testkit.Stores) (replay.KnowledgeStore, replay.TraceStore)
	}
	type want struct {
		err error
		// Replay traces recorded with the failure
		failed int
	}
	omit := func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{CostUSD: 0.01, Output: json.RawMessage(`{"cases":[]}`)}, nil
	}
	undecodable := func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{CostUSD: 0.01, Output: json.RawMessage(`[`)}, nil
	}
	refused := func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{}, assert.AnError
	}
	same := func(st testkit.Stores) (replay.KnowledgeStore, replay.TraceStore) { return st.Ledger, st.Traces }
	broken := func(st testkit.Stores) (replay.KnowledgeStore, replay.TraceStore) {
		return st.Ledger, unrecorded{st.Traces}
	}
	loose := func(st testkit.Stores) (replay.KnowledgeStore, replay.TraceStore) {
		return withLoose{st.Ledger}, st.Traces
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an unknown id has no current version", args{"nope", 0, answerAll, same}, want{replay.ErrNoCurrent, 0}},
		{"an unknown version is not found", args{"idv", 2, answerAll, same}, want{knowledge.ErrNotFound, 0}},
		{"an item without a run scope reaches no output", args{"loose", 0, answerAll, loose}, want{replay.ErrNoScope, 0}},
		{"a veto acts through the guard", args{"guard", 0, answerAll, same}, want{replay.ErrNoScope, 0}},
		{"a failed model call answers its error", args{"idv", 0, refused, same}, want{assert.AnError, 0}},
		{"an answer left out refuses the judgment", args{"idv", 0, omit, same}, want{replay.ErrJudgment, 1}},
		{"an answer that does not decode refuses the judgment", args{"idv", 0, undecodable, same}, want{replay.ErrJudgment, 1}},
		{"a judgment that is not recorded fails", args{"idv", 0, answerAll, broken}, want{assert.AnError, 0}},
		{"a refused judgment that is not recorded fails with both errors", args{"idv", 0, undecodable, broken}, want{replay.ErrJudgment, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := seed(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(tc.args.judge).AnyTimes()
			ledger, traces := tc.args.stores(s.st)
			r := replay.New(ledger, traces, s.st.Feedback, client, "", s.st.Clock.Now)

			got, err := r.Replay(context.Background(), tc.args.id, tc.args.version)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, replay.Result{}, got)
			recorded, err := s.st.Traces.List(context.Background(), trace.Filter{Name: trace.NameReplay})
			require.NoError(t, err)
			require.Len(t, recorded, tc.want.failed)
			for _, tr := range recorded {
				assert.NotEmpty(t, tr.Error, "the trace names why the judgment failed")
				assert.InDelta(t, 0.01, tr.Usage.CostUSD, 1e-9, "the cost of the model call stays in the records")
			}
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
