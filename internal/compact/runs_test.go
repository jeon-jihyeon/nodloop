package compact_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

var errModel = errors.New("model down")

// Two approved run items of the repo nodloop that say one thing twice, a compactor and its proposal
func runFolder(t *testing.T) (testkit.Stores, *compact.Compactor, knowledge.Compaction) {
	t.Helper()
	ctx := context.Background()
	s := testkit.Open(t)
	at := s.Clock.Now()
	run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}}, nil, []byte("cd repo && git status"), at)
	require.NoError(t, err)
	require.NoError(t, s.Traces.Append(ctx, run.Record()))
	scope := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	for id, content := range map[string]string{"a": "Use git -C <dir> instead of cd <dir> && git", "b": "Never cd into a directory before a git command"} {
		_, _, err := s.Ledger.Propose(ctx, knowledge.Knowledge{
			ID: id, Kind: knowledge.KindJudgment, Content: content, Run: scope, Author: "author",
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run.ID}},
		})
		require.NoError(t, err)
		require.NoError(t, testkit.Err(s.Ledger.Approve(ctx, id, 1, "ann")))
	}
	c := compact.New(s.Ledger, s.Traces, s.Feedback, s.Traces, s.Clock.Now)
	f, err := c.Folder(ctx, "a")
	require.NoError(t, err)
	require.Len(t, f.Items, 2)
	assert.Contains(t, f.String(), "## Coverage")
	proposed, err := c.Propose(ctx, "a", compact.Draft{Items: []compact.Item{{
		ID: "a", Kind: knowledge.KindJudgment, Content: "Use git -C <dir> and never cd into a directory before a git command",
		From: []string{"a", "b"}, Producer: "session", Labels: map[string][]string{"repo": {"nodloop"}},
	}}}, "")
	require.NoError(t, err)
	return s, c, proposed
}

func TestCompactRuns(t *testing.T) {
	type want struct {
		recordErr  error
		approveErr error
	}
	tcs := []struct {
		name string
		args []compact.CoverageAnswer
		want want
	}{
		{"a full coverage lets the compaction through", []compact.CoverageAnswer{
			{Old: "a", CoveredBy: []string{"a"}}, {Old: "b", CoveredBy: []string{"a"}},
		}, want{}},
		{"an old item no new item states is refused", []compact.CoverageAnswer{
			{Old: "a", CoveredBy: []string{"a"}},
		}, want{approveErr: knowledge.ErrCoverageNotPassed}},
		{"a lost fact is refused", []compact.CoverageAnswer{
			{Old: "a", CoveredBy: []string{"a"}}, {Old: "b", CoveredBy: []string{"a"}, Lost: []string{"the word never"}},
		}, want{approveErr: knowledge.ErrCoverageNotPassed}},
		{"an old item answered twice is refused when recorded", []compact.CoverageAnswer{
			{Old: "a", CoveredBy: []string{"a"}}, {Old: "b", CoveredBy: []string{"a"}}, {Old: "b", CoveredBy: []string{"a"}, Lost: []string{"x"}},
		}, want{recordErr: compact.ErrCoverageInvalid, approveErr: compact.ErrNoCoverage}},
		{"an id outside the compaction is refused when recorded", []compact.CoverageAnswer{
			{Old: "a", CoveredBy: []string{"z"}},
		}, want{recordErr: compact.ErrCoverageInvalid, approveErr: compact.ErrNoCoverage}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, c, proposed := runFolder(t)
			_, err := c.Approve(ctx, proposed.ID, "sam")
			require.ErrorIs(t, err, compact.ErrNoCoverage, "nothing approves before a check")

			_, err = c.Record(ctx, proposed.ID, tc.args)
			require.ErrorIs(t, err, tc.want.recordErr)
			got, err := c.Approve(ctx, proposed.ID, "sam")

			require.ErrorIs(t, err, tc.want.approveErr)
			if tc.want.approveErr != nil {
				return
			}
			assert.Len(t, got.Items, 1)
			all, err := s.Ledger.All(ctx)
			require.NoError(t, err)
			for _, k := range all.Current() {
				if k.ID == "b" {
					assert.Equal(t, knowledge.StatusRetired, k.Status)
				}
			}
		})
	}
}

// A draft that reaches runs an old item never reached, two drafts of one kind for the same runs, an unnamed old item and an unrecorded label are refused
func TestCompactRunsRefusals(t *testing.T) {
	ctx := context.Background()
	both := []string{"a", "b"}
	repo := map[string][]string{"repo": {"nodloop"}}
	tcs := []struct {
		name string
		args []compact.Item
		want error
	}{
		{"a draft without the repo label widens", []compact.Item{
			{Kind: knowledge.KindJudgment, Content: "x", From: both, Producer: "session"},
		}, knowledge.ErrCompactionInvalid},
		{"a draft without a producer reaches none of the runs the old items reached", []compact.Item{
			{Kind: knowledge.KindJudgment, Content: "x", From: both, Labels: repo},
		}, compact.ErrReachLost},
		{"a draft narrower than the old items loses the runs they reached", []compact.Item{
			{Kind: knowledge.KindJudgment, Content: "x", From: both, Producer: "session", Labels: map[string][]string{"repo": {"nodloop"}, "task": {"commit"}}},
		}, compact.ErrReachLost},
		{"two judgments for the same runs overlap", []compact.Item{
			{ID: "a", Kind: knowledge.KindJudgment, Content: "x", From: []string{"a"}, Producer: "session", Labels: repo},
			{ID: "b", Kind: knowledge.KindJudgment, Content: "y", From: []string{"b"}, Producer: "session", Labels: repo},
		}, knowledge.ErrCompactionOverlap},
		{"an old item left unnamed loses the runs it reached", []compact.Item{
			{Kind: knowledge.KindJudgment, Content: "x", From: []string{"a"}, Producer: "session", Labels: repo},
		}, compact.ErrReachLost},
		{"a label no run carries is refused", []compact.Item{
			{Kind: knowledge.KindJudgment, Content: "x", From: both, Producer: "session", Labels: map[string][]string{"repo": {"nodlop"}}},
		}, knowledge.ErrScopeUnobserved},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			at := s.Clock.Now()
			run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}}, nil, []byte("x"), at)
			require.NoError(t, err)
			require.NoError(t, s.Traces.Append(ctx, run.Record()))
			commit, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}, "task": {"commit"}}, nil, []byte("x"), at)
			require.NoError(t, err)
			require.NoError(t, s.Traces.Append(ctx, commit.Record()))
			for _, id := range []string{"a", "b"} {
				_, _, err := s.Ledger.Propose(ctx, knowledge.Knowledge{
					ID: id, Kind: knowledge.KindJudgment, Content: id, Author: "author",
					Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}},
					Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run.ID}},
				})
				require.NoError(t, err)
				require.NoError(t, testkit.Err(s.Ledger.Approve(ctx, id, 1, "ann")))
			}
			c := compact.New(s.Ledger, s.Traces, s.Feedback, s.Traces, s.Clock.Now)

			_, err = c.Propose(ctx, "a", compact.Draft{Items: tc.args}, "")

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// One model call answers the coverage and it is recorded for the approval
func TestCompactRunsCheck(t *testing.T) {
	ctx := context.Background()
	s, c, proposed := runFolder(t)
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
		assert.Equal(t, compact.CoverageRules, req.System)
		assert.True(t, strings.Contains(req.Prompt, "## Old items") && strings.Contains(req.Prompt, "Replaces: a, b"), req.Prompt)
		return llm.Response{
			Output:      json.RawMessage(`{"items":[{"old":"a","covered_by":["a"]},{"old":"b","covered_by":["a"]}]}`),
			InputTokens: 12, OutputTokens: 3, CostUSD: 0.01, Duration: 2 * time.Second, Model: "claude-haiku-4-5",
		}, nil
	})

	cov, err := c.Check(ctx, client, proposed.ID, "haiku")

	require.NoError(t, err)
	assert.Len(t, cov.Items, 2)
	_, err = c.Approve(ctx, proposed.ID, "sam")
	assert.NoError(t, err)
	checks, err := s.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameCheck)})
	require.NoError(t, err)
	require.Len(t, checks, 1)
	assert.Equal(t, "claude-haiku-4-5", checks[0].Model)
	assert.Equal(t, record.Usage{InputTokens: 12, OutputTokens: 3, CostUSD: 0.01}, checks[0].Usage)
	assert.Equal(t, int64(2000), checks[0].DurationMS)
	assert.NoError(t, schema.Validate(string(trace.NameCheck), checks[0]))
}

// Every draft that reaches the model leaves a compact trace with its usage, wall time and error
func TestCompactDraftTrace(t *testing.T) {
	ctx := context.Background()
	draft := `{"items":[{"id":"a","kind":"judgment","content":"Use git -C <dir> and never cd before git","from":["a","b"],` +
		`"producer":"session","labels":{"repo":["nodloop"]}}]}`
	type want struct {
		err     error
		session bool
		model   string
		usage   record.Usage
		failed  bool
	}
	tcs := []struct {
		name string
		args func(context.Context, llm.Request) (llm.Response, error)
		want want
	}{
		{"a proposed draft names its compaction with the model that answered", func(context.Context, llm.Request) (llm.Response, error) {
			return llm.Response{Output: json.RawMessage(draft), InputTokens: 40, OutputTokens: 9, Model: "claude-sonnet-5"}, nil
		}, want{session: true, model: "claude-sonnet-5", usage: record.Usage{InputTokens: 40, OutputTokens: 9}}},
		{"a failed call keeps the model asked and the error", func(context.Context, llm.Request) (llm.Response, error) {
			return llm.Response{}, errModel
		}, want{err: errModel, model: "sonnet", failed: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			at := s.Clock.Now()
			run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}}, nil, []byte("x"), at)
			require.NoError(t, err)
			require.NoError(t, s.Traces.Append(ctx, run.Record()))
			for id, content := range map[string]string{"a": "Use git -C <dir>", "b": "Never cd before git"} {
				_, _, err := s.Ledger.Propose(ctx, knowledge.Knowledge{
					ID: id, Kind: knowledge.KindJudgment, Content: content, Author: "author",
					Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}},
					Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run.ID}},
				})
				require.NoError(t, err)
				require.NoError(t, testkit.Err(s.Ledger.Approve(ctx, id, 1, "ann")))
			}
			c := compact.New(s.Ledger, s.Traces, s.Feedback, s.Traces, s.Clock.Now)
			f, err := c.Folder(ctx, "a")
			require.NoError(t, err)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(tc.args)

			proposed, err := c.Draft(ctx, client, f, "sonnet", "")

			require.ErrorIs(t, err, tc.want.err)
			found, err := s.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameCompact)})
			require.NoError(t, err)
			require.Len(t, found, 1)
			assert.Equal(t, "a", found[0].Subject)
			assert.Equal(t, tc.want.session, found[0].SessionID != "" && found[0].SessionID == proposed.ID)
			assert.Equal(t, tc.want.model, found[0].Model)
			assert.Equal(t, tc.want.usage, found[0].Usage)
			assert.Equal(t, tc.want.failed, found[0].Error != "")
			assert.NoError(t, schema.Validate(string(trace.NameCompact), found[0]))
		})
	}
}
