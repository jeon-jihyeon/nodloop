package eval_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/eval"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/jsonl"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestSeed(t *testing.T) {
	type args struct {
		opts     eval.RunOptions
		response llm.Response
		// Model failure per event
		// Every model call returns the response with the failure of its event
		failures map[string]error
	}
	type review struct {
		subject, model, err string
		tags                []string
	}
	type want struct {
		reviews []review
		log     string
		err     error
	}
	noAction := llm.Response{
		Output:  json.RawMessage(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`),
		CostUSD: 0.01,
	}
	seed := []string{string(eval.ConditionSeed)}
	maxTurns := &llm.ResultError{Subtype: "error_max_turns"}
	eventRe := regexp.MustCompile(`# Event (tq-\d+)\n`)
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"every seed event is reviewed under the seed tag with the model",
			args{opts: eval.RunOptions{SessionID: "s", Model: "haiku", Parallel: 1}, response: noAction},
			want{
				reviews: []review{
					{"tq-001", "haiku", "", seed}, {"tq-002", "haiku", "", seed}, {"tq-005", "haiku", "", seed},
					{"tq-006", "haiku", "", seed}, {"tq-009", "haiku", "", seed}, {"tq-010", "haiku", "", seed},
					{"tq-013", "haiku", "", seed}, {"tq-014", "haiku", "", seed}, {"tq-017", "haiku", "", seed},
					{"tq-018", "haiku", "", seed}, {"tq-021", "haiku", "", seed}, {"tq-022", "haiku", "", seed},
				},
				log: "tq-001\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-002\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-005\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-006\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-009\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-010\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-013\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-014\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-017\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-018\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-021\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-022\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n",
			},
		},
		{
			"events narrow the run and each review logs one line",
			args{opts: eval.RunOptions{SessionID: "s", Events: []string{"tq-005", "tq-001"}, Parallel: 1}, response: noAction},
			want{
				reviews: []review{{"tq-001", "", "", seed}, {"tq-005", "", "", seed}},
				log: "tq-001\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-005\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n",
			},
		},
		{
			"a model failure keeps the failed trace and the run goes on",
			args{
				opts:     eval.RunOptions{SessionID: "s", Events: []string{"tq-001", "tq-002"}, Parallel: 1},
				failures: map[string]error{"tq-001": maxTurns, "tq-002": maxTurns},
			},
			want{
				reviews: []review{
					{"tq-001", "", "claude result error_max_turns", seed}, {"tq-002", "", "claude result error_max_turns", seed},
				},
				log: "tq-001\tseed\tfailed: claude result error_max_turns\n" +
					"tq-002\tseed\tfailed: claude result error_max_turns\n",
			},
		},
		{
			"one failed review leaves the reviews after it to complete",
			args{
				opts:     eval.RunOptions{SessionID: "s", Events: []string{"tq-001", "tq-002", "tq-005"}, Parallel: 1},
				response: noAction,
				failures: map[string]error{"tq-002": maxTurns},
			},
			want{
				reviews: []review{
					{"tq-001", "", "", seed}, {"tq-002", "", "claude result error_max_turns", seed}, {"tq-005", "", "", seed},
				},
				log: "tq-001\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n" +
					"tq-002\tseed\tfailed: claude result error_max_turns\n" +
					"tq-005\tseed\tno_action\tforced=false\t$0.0100\t1000ms\n",
			},
		},
		{
			"a holdout event in a seed run is refused",
			args{opts: eval.RunOptions{SessionID: "s", Events: []string{"tq-003"}}, response: noAction},
			want{err: eval.ErrEventOutsideSet},
		},
		{
			"a condition list in a seed run is refused before any review",
			args{
				opts:     eval.RunOptions{SessionID: "s", Conditions: []eval.Condition{eval.ConditionExamples}},
				response: noAction,
			},
			want{err: eval.ErrSeedConditions},
		},
		{
			"a negative parallel is refused before any review",
			args{opts: eval.RunOptions{SessionID: "s", Parallel: -1}, response: noAction},
			want{err: eval.ErrNegativeParallel},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					return tc.args.response, tc.args.failures[eventRe.FindStringSubmatch(req.Prompt)[1]]
				}).AnyTimes()
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), diagnose.Limits{}, client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			var log bytes.Buffer
			opts := tc.args.opts
			opts.Log = &log
			traces, err := eval.New(s.Source, d, s.Traces, s.Feedback, s.Ledger).Seed(ctx, opts)
			var got []review
			for _, tr := range traces {
				got = append(got, review{tr.Subject, tr.Model, tr.Error, tr.Tags})
			}
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.reviews, got)
			assert.Equal(t, tc.want.log, log.String())
		})
	}
}

// Reviews stop without cutting one in flight into a false model failure
// 1. a panic ends the run with that error and reviews in flight finish
// 2. a cancelled context is the error of the run and never a failed trace
func TestSeedStops(t *testing.T) {
	type args struct {
		events   []string
		parallel int
		// The model call of one run given the cancel of its context
		complete func(cancel context.CancelFunc, req llm.Request) (llm.Response, error)
	}
	type want struct {
		subjects []string
		err      error
	}
	noAction := llm.Response{
		Output: json.RawMessage(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`),
	}
	// Only the in flight row uses them
	started, panicking := make(chan struct{}), make(chan struct{})
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a panic ends the run before the next review starts",
			args{[]string{"tq-001", "tq-002", "tq-005"}, 1, func(context.CancelFunc, llm.Request) (llm.Response, error) {
				panic("boom")
			}},
			want{err: eval.ErrReviewPanicked},
		},
		{
			"a review in flight when another panics finishes and is returned",
			args{[]string{"tq-001", "tq-002"}, 2, func(_ context.CancelFunc, req llm.Request) (llm.Response, error) {
				if strings.Contains(req.Prompt, "# Event tq-002") {
					close(started)
					<-panicking
					return noAction, nil
				}
				<-started
				close(panicking)
				panic("boom")
			}},
			want{[]string{"tq-002"}, eval.ErrReviewPanicked},
		},
		{
			"a cancelled context ends the run with its error",
			args{[]string{"tq-001", "tq-002"}, 1, func(cancel context.CancelFunc, _ llm.Request) (llm.Response, error) {
				cancel()
				return llm.Response{}, context.Canceled
			}},
			want{err: context.Canceled},
		},
		{
			"a context cancelled after a review succeeded ends the run with its error and the finished review",
			args{[]string{"tq-001", "tq-002"}, 1, func(cancel context.CancelFunc, _ llm.Request) (llm.Response, error) {
				cancel()
				return noAction, nil
			}},
			want{[]string{"tq-001"}, context.Canceled},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			s := testkit.Open(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, req llm.Request) (llm.Response, error) { return tc.args.complete(cancel, req) },
			).AnyTimes()
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), diagnose.Limits{}, client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			opts := eval.RunOptions{SessionID: "s", Events: tc.args.events, Parallel: tc.args.parallel}
			traces, err := eval.New(s.Source, d, s.Traces, s.Feedback, s.Ledger).Seed(runCtx, opts)
			var got []string
			for _, tr := range traces {
				got = append(got, tr.Subject)
			}
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.subjects, got)
		})
	}
}

// A run that fails before recording returns its own error and never an older trace of the same review
func TestSeedWithoutClient(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Trace
	}{
		{
			"an older successful trace is not taken for the failure",
			trace.Trace{ID: "t0", Output: json.RawMessage(`{"status":"no_action"}`)},
		},
		{"an older failed trace is not taken for the failure", trace.Trace{ID: "t0", Error: "old"}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			old := tc.args
			old.Name, old.SessionID, old.Subject = trace.NameDiagnose, "s", "tq-001"
			old.Tags = []string{string(eval.ConditionSeed)}
			require.NoError(t, s.Traces.Append(ctx, old))
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), diagnose.Limits{}, nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			opts := eval.RunOptions{SessionID: "s", Events: []string{"tq-001"}}
			traces, err := eval.New(s.Source, d, s.Traces, s.Feedback, s.Ledger).Seed(ctx, opts)
			assert.ErrorIs(t, err, eval.ErrNoFailedTrace)
			assert.ErrorIs(t, err, diagnose.ErrNoClient)
			assert.Equal(t, []trace.Trace{}, traces)
		})
	}
}

// The label source is a temp dir holding one directory entry
// A labels file that is a directory cannot be read and any other entry leaves the set without labels
func TestSeedLabels(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"seed on a set without labels is refused", "other", eval.ErrNoLabels},
		{"seed fails when the labels cannot be read", "labels.jsonl", syscall.EISDIR},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, tc.args), 0o700))
			src, err := evidencefile.New(dir)
			require.NoError(t, err)
			traces, err := eval.New(src, nil, s.Traces, s.Feedback, s.Ledger).Seed(ctx, eval.RunOptions{SessionID: "s"})
			assert.ErrorIs(t, err, tc.want)
			assert.Nil(t, traces)
		})
	}
}

// A holdout trace t1 of session s already exists when each run starts
func TestHoldout(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	type args struct {
		opts      eval.RunOptions
		feedback  []feedback.Feedback
		knowledge []knowledge.Knowledge
	}
	type review struct {
		subject string
		tags    []string
	}
	type want struct {
		reviews []review
		err     error
	}
	off := []eval.Condition{eval.ConditionBaseline}
	leak := func(traceID string) []knowledge.Knowledge {
		return []knowledge.Knowledge{{
			ID: "k-leak", Version: 1, Kind: knowledge.KindJudgment, Content: "leak",
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{traceID}},
			Basis:    knowledge.BasisStated, Status: knowledge.StatusApproved, Approver: "a", Author: "a", Time: at,
		}}
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"events narrow the run and keep the label order",
			args{opts: eval.RunOptions{SessionID: "s", Conditions: off, Events: []string{"tq-004", "tq-003"}, Parallel: 2}},
			want{reviews: []review{{"tq-003", []string{"feedback:off"}}, {"tq-004", []string{"feedback:off"}}}},
		},
		{
			"every holdout condition runs by default in report order",
			args{opts: eval.RunOptions{SessionID: "s", Events: []string{"tq-003"}}},
			want{reviews: []review{
				{"tq-003", []string{"feedback:off"}}, {"tq-003", []string{"feedback:on"}},
				{"tq-003", []string{"knowledge:on"}}, {"tq-003", []string{"knowledge:all"}},
			}},
		},
		{
			"a seed event in a holdout run is refused",
			args{opts: eval.RunOptions{SessionID: "s", Conditions: off, Events: []string{"tq-001"}}},
			want{err: eval.ErrEventOutsideSet},
		},
		{
			"an unknown condition is refused",
			args{opts: eval.RunOptions{SessionID: "s", Conditions: []eval.Condition{"bogus"}}},
			want{err: eval.ErrUnknownCondition},
		},
		{
			"the seed condition is refused in a holdout run",
			args{opts: eval.RunOptions{SessionID: "s", Conditions: []eval.Condition{eval.ConditionSeed}}},
			want{err: eval.ErrUnknownCondition},
		},
		{
			"feedback on a holdout trace is refused",
			args{
				opts:     eval.RunOptions{SessionID: "s", Conditions: off, Events: []string{"tq-003"}},
				feedback: []feedback.Feedback{{TraceID: "t1", Verdict: feedback.VerdictApprove}},
			},
			want{err: eval.ErrHoldoutFeedback},
		},
		{
			"approved knowledge citing a holdout trace is refused",
			args{opts: eval.RunOptions{SessionID: "s", Conditions: off, Events: []string{"tq-003"}}, knowledge: leak("t1")},
			want{err: eval.ErrHoldoutKnowledge},
		},
		{
			"feedback on a holdout trace of another session is refused",
			args{
				opts:     eval.RunOptions{SessionID: "other", Conditions: off, Events: []string{"tq-003"}},
				feedback: []feedback.Feedback{{TraceID: "t1", Verdict: feedback.VerdictEdit}},
			},
			want{err: eval.ErrHoldoutFeedback},
		},
		{
			"feedback on a holdout event left out by events is refused",
			args{
				opts:     eval.RunOptions{SessionID: "s", Conditions: off, Events: []string{"tq-004"}},
				feedback: []feedback.Feedback{{TraceID: "t1", Verdict: feedback.VerdictReject}},
			},
			want{err: eval.ErrHoldoutFeedback},
		},
		{
			"approved knowledge citing a holdout trace of another session is refused",
			args{opts: eval.RunOptions{SessionID: "other", Conditions: off, Events: []string{"tq-003"}}, knowledge: leak("t1")},
			want{err: eval.ErrHoldoutKnowledge},
		},
		{
			"feedback and knowledge on other traces let the run go on",
			args{
				opts:      eval.RunOptions{SessionID: "s", Conditions: off, Events: []string{"tq-003"}},
				feedback:  []feedback.Feedback{{TraceID: "t0", Verdict: feedback.VerdictReject}},
				knowledge: leak("t0"),
			},
			want{reviews: []review{{"tq-003", []string{"feedback:off"}}}},
		},
		{
			"a negative parallel is refused before any review",
			args{opts: eval.RunOptions{SessionID: "s", Conditions: off, Events: []string{"tq-003"}, Parallel: -2}},
			want{err: eval.ErrNegativeParallel},
		},
	}
	noAction := llm.Response{
		Output: json.RawMessage(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`),
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(noAction, nil).AnyTimes()
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), diagnose.Limits{}, client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			held := trace.Trace{
				ID: "t1", Name: trace.NameDiagnose, SessionID: "s", Subject: "tq-003", Tags: []string{"feedback:off"},
			}
			require.NoError(t, s.Traces.Append(ctx, held))
			for _, fb := range tc.args.feedback {
				require.NoError(t, s.Feedback.Append(ctx, fb))
			}
			require.NoError(t, s.Ledger.Import(ctx, tc.args.knowledge))
			traces, err := eval.New(s.Source, d, s.Traces, s.Feedback, s.Ledger).Holdout(ctx, tc.args.opts)
			var got []review
			for _, tr := range traces {
				got = append(got, review{tr.Subject, tr.Tags})
			}
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.reviews, got)
		})
	}
}

// The label source is a temp dir holding one directory entry
// A labels file that is a directory cannot be read and any other entry leaves the set without labels
func TestHoldoutLabels(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"holdout on a set without labels is refused", "other", eval.ErrNoLabels},
		{"holdout fails when the labels cannot be read", "labels.jsonl", syscall.EISDIR},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, tc.args), 0o700))
			src, err := evidencefile.New(dir)
			require.NoError(t, err)
			traces, err := eval.New(src, nil, s.Traces, s.Feedback, s.Ledger).Holdout(ctx, eval.RunOptions{SessionID: "s"})
			assert.ErrorIs(t, err, tc.want)
			assert.Nil(t, traces)
		})
	}
}

// tq-007 and tq-008 share their change context and metric so a correction on one would be offered to the other
// The model rejects every diagnose trace already recorded so feedback on a holdout review arrives while the run goes on
func TestHoldoutFeedbackDuringTheRun(t *testing.T) {
	type review struct {
		subject  string
		tags     []string
		examples bool
	}
	type want struct {
		reviews []review
		err     error
	}
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	noAction := llm.Response{
		Output: json.RawMessage(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`),
	}
	tcs := []struct {
		name string
		args eval.RunOptions
		want want
	}{
		{
			"no review takes a holdout review rejected during the run as an example",
			eval.RunOptions{
				SessionID: "s", Conditions: []eval.Condition{eval.ConditionBaseline, eval.ConditionExamples},
				Events: []string{"tq-007", "tq-008"}, Parallel: 1,
			},
			want{reviews: []review{
				{"tq-007", []string{"feedback:off"}, false}, {"tq-007", []string{"feedback:on"}, false},
				{"tq-008", []string{"feedback:off"}, false}, {"tq-008", []string{"feedback:on"}, false},
			}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			var prompts []string
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					recorded, err := s.Traces.List(ctx, trace.Filter{Name: trace.NameDiagnose})
					assert.NoError(t, err)
					for _, tr := range recorded {
						rejected := feedback.Feedback{TraceID: tr.ID, Time: at, Verdict: feedback.VerdictReject, Reason: "wrong"}
						assert.NoError(t, s.Feedback.Append(ctx, rejected))
					}
					prompts = append(prompts, req.Prompt)
					return noAction, nil
				}).Times(len(tc.want.reviews))
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), diagnose.Limits{}, client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			traces, err := eval.New(s.Source, d, s.Traces, s.Feedback, s.Ledger).Holdout(ctx, tc.args)
			var got []review
			for i, tr := range traces {
				got = append(got, review{tr.Subject, tr.Tags, strings.Contains(prompts[i], "## Examples")})
			}
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.reviews, got)
		})
	}
}

func TestReportTable(t *testing.T) {
	const header = "| condition | events | status acc | hold acc | hold precision | citation p | citation r " +
		"| required checks | first check | knowledge hit | misapplied | revised | forced holds | failures | annotated " +
		"| approve:edit:reject | edit rate | mean edit width | mean cost usd | mean input tokens | mean output tokens " +
		"| p50 ms | p95 ms |\n" +
		"|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n"
	const against = "\nAgainst feedback:off\n\n| condition | fixed | regressed | weakened |\n|---|---|---|---|\n"
	tcs := []struct {
		name string
		args eval.Report
		want string
	}{
		{
			"a row prints ratios with two decimals and not applicable as a dash",
			eval.Report{Summaries: []eval.Summary{{
				Condition: eval.ConditionBaseline, Events: 3, StatusAccuracy: 2.0 / 3, HoldRecall: 1, HoldPrecision: 0.5,
				CitationPrecision: 1, CitationRecall: 0.5, RequiredChecks: 0.75, FirstCheck: -1, KnowledgeHit: -1,
				Misapplications: 2, Revisions: 1, ForcedHolds: 1, Failures: 1,
				Annotated: 3, Approvals: 1, Edits: 1, Rejections: 1, EditRate: 2.0 / 3, MeanEditWidth: 3,
				MeanCostUSD: 0.2, MeanInputTokens: 400.0 / 3, MeanOutputTokens: 10, P50DurationMS: 200, P95DurationMS: 300,
			}}},
			header + "| feedback:off | 3 | 0.67 | 1.00 | 0.50 | 1.00 | 0.50 | 0.75 | - | - | 2 | 1 | 1 | 1 | 3 | 1:1:1 " +
				"| 0.67 | 3.00 | 0.2000 | 133 | 10 | 200 | 300 |\n\nAgainst feedback:off: not compared\n",
		},
		{
			"a compared condition without movement prints dashes",
			eval.Report{AgainstBaseline: map[eval.Condition]eval.Comparison{eval.ConditionExamples: {}}},
			header + against + "| feedback:on | - | - | - |\n",
		},
		{
			"compared conditions print in report order with the events that moved",
			eval.Report{AgainstBaseline: map[eval.Condition]eval.Comparison{
				eval.ConditionKnowledge:    {Fixed: []string{"tq-003", "tq-007"}, Weakened: []string{"tq-015"}},
				eval.ConditionExamples:     {Regressed: []string{"tq-011"}},
				eval.ConditionKnowledgeAll: {},
			}},
			header + against + "| feedback:on | - | tq-011 | - |\n| knowledge:on | tq-003, tq-007 | - | tq-015 |\n" +
				"| knowledge:all | - | - | - |\n",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Table())
		})
	}
}

// Seed then holdout with a reviewer that answers every event as its label expects
// One seed correction and the demo knowledge give every holdout condition something to inject
func TestSeedThenHoldoutReport(t *testing.T) {
	type args struct {
		parallel int
		// Index of the seed trace the reviewer rejects
		rejected int
	}
	// Cost and durations are left out since parallel reviews interleave on the shared clock
	type summary struct {
		condition                                                         eval.Condition
		events                                                            int
		statusAccuracy, citationPrecision, citationRecall, requiredChecks float64
		knowledgeHit, editRate                                            float64
		annotated, misapplications, failures, forcedHolds                 int
	}
	type want struct {
		summaries []summary
		against   map[eval.Condition]eval.Comparison
	}
	none := eval.Comparison{Fixed: []string{}, Regressed: []string{}, Weakened: []string{}}
	eventRe := regexp.MustCompile(`# Event (tq-\d+)`)
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a reviewer that answers every label scores full marks and knowledge:all misapplies the rest",
			args{parallel: 3, rejected: 4},
			want{
				summaries: []summary{
					{
						condition: eval.ConditionSeed, events: 12, statusAccuracy: 1, citationPrecision: 1, citationRecall: 1,
						requiredChecks: 1, editRate: 1, annotated: 1,
					},
					{
						condition: eval.ConditionBaseline, events: 12, statusAccuracy: 1, citationPrecision: 1, citationRecall: 1,
						requiredChecks: 1, editRate: -1,
					},
					{
						condition: eval.ConditionExamples, events: 12, statusAccuracy: 1, citationPrecision: 1, citationRecall: 1,
						requiredChecks: 1, editRate: -1,
					},
					{
						condition: eval.ConditionKnowledge, events: 12, statusAccuracy: 1, citationPrecision: 1, citationRecall: 1,
						requiredChecks: 1, knowledgeHit: 1, editRate: -1,
					},
					{
						condition: eval.ConditionKnowledgeAll, events: 12, statusAccuracy: 1, citationPrecision: 1,
						citationRecall: 1, requiredChecks: 1, knowledgeHit: 1, editRate: -1, misapplications: 15,
					},
				},
				against: map[eval.Condition]eval.Comparison{
					eval.ConditionExamples: none, eval.ConditionKnowledge: none, eval.ConditionKnowledgeAll: none,
				},
			},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			labels, err := s.Source.Labels(ctx)
			require.NoError(t, err)
			byEvent := map[string]evidence.Label{}
			for _, l := range labels {
				byEvent[l.EventID] = l
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					m := eventRe.FindStringSubmatch(req.Prompt)
					if !assert.Len(t, m, 2, "prompt without event id:\n%s", req.Prompt) {
						return llm.Response{}, assert.AnError
					}
					l := byEvent[m[1]]
					d := diagnose.Diagnosis{
						Status: diagnose.StatusNoAction, Observations: []string{}, Causes: []diagnose.Cause{},
						Checks: []diagnose.Check{}, OpenQuestions: []string{},
					}
					switch {
					case l.IsHold():
						d.Status, d.HoldReasons = diagnose.StatusHold, []string{"labeled hold"}
					case len(l.Paragraphs) > 0:
						cause := diagnose.Cause{Summary: "cause"}
						for _, id := range l.Paragraphs {
							cause.ParagraphIDs = append(cause.ParagraphIDs, string(id))
						}
						d.Status, d.Causes = diagnose.StatusReadyForReview, []diagnose.Cause{cause}
					}
					for _, id := range l.RequiredChecks {
						d.Checks = append(d.Checks, diagnose.Check{Step: "check", Purpose: "p", ParagraphIDs: []string{string(id)}})
					}
					b, err := json.Marshal(d)
					assert.NoError(t, err)
					return llm.Response{Output: b, CostUSD: 0.01, InputTokens: 50}, nil
				}).AnyTimes()
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), diagnose.Limits{}, client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			r := eval.New(s.Source, d, s.Traces, s.Feedback, s.Ledger)

			seeds, err := r.Seed(ctx, eval.RunOptions{SessionID: "s", Parallel: tc.args.parallel})
			require.NoError(t, err)
			require.Len(t, seeds, 12)
			fb, err := feedback.New(seeds[tc.args.rejected].ID, feedback.VerdictReject, "wrong cause", nil, "", s.Clock.Now())
			require.NoError(t, err)
			require.NoError(t, s.Feedback.Append(ctx, fb))
			demo, err := jsonl.Open[knowledge.Knowledge](testkit.DemoDir(t), "knowledge.jsonl")
			require.NoError(t, err)
			records, err := demo.All()
			require.NoError(t, err)
			require.NoError(t, s.Ledger.Import(ctx, records))
			holdout, err := r.Holdout(ctx, eval.RunOptions{SessionID: "s", Parallel: tc.args.parallel})
			require.NoError(t, err)
			require.Len(t, holdout, 48)

			rep, err := r.Report(ctx, "s")
			require.NoError(t, err)
			var got []summary
			for _, sum := range rep.Summaries {
				got = append(got, summary{
					condition: sum.Condition, events: sum.Events, statusAccuracy: sum.StatusAccuracy,
					citationPrecision: sum.CitationPrecision, citationRecall: sum.CitationRecall,
					requiredChecks: sum.RequiredChecks, knowledgeHit: sum.KnowledgeHit, editRate: sum.EditRate,
					annotated: sum.Annotated, misapplications: sum.Misapplications, failures: sum.Failures,
					forcedHolds: sum.ForcedHolds,
				})
			}
			assert.Equal(t, tc.want.summaries, got)
			assert.Equal(t, tc.want.against, rep.AgainstBaseline)
		})
	}
}
