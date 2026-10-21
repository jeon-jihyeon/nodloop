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
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

var errModel = errors.New("model down")

// A compactable fixture folder with duplicate lessons
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

// Approves the compaction as sam with its newest coverage as every caller hands it to the ledger
func approve(s testkit.Stores, c *compact.Compactor, id string) (knowledge.Compaction, error) {
	ctx := context.Background()
	cov, err := c.Coverage(ctx, id)
	if err != nil {
		return knowledge.Compaction{}, err
	}
	return s.Ledger.ApproveCompaction(ctx, id, "sam", cov)
}

func TestFolderCorrectionsUseReconciledVerdicts(t *testing.T) {
	type verdict struct {
		reviewer string
		verdict  feedback.Verdict
		seconds  int
	}
	tcs := []struct {
		name string
		args []verdict
		want []feedback.Verdict
	}{
		{"a person wins over a later inferred correction",
			[]verdict{{"ann", feedback.VerdictApprove, 1}, {"session", feedback.VerdictReject, 2}},
			[]feedback.Verdict{feedback.VerdictApprove}},
		{"a disputed inferred verdict supplies no correction",
			[]verdict{{"session", feedback.VerdictReject, 1}, {"nodloop", feedback.VerdictApprove, 2}}, nil},
		{"a late older append never replaces a newer verdict",
			[]verdict{{"ann", feedback.VerdictApprove, 2}, {"ann", feedback.VerdictReject, 1}},
			[]feedback.Verdict{feedback.VerdictApprove}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, c, proposed := runFolder(t)
			id := proposed.Replaced[0].Evidence.FeedbackTraceIDs[0]
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for _, v := range tc.args {
				require.NoError(t, s.Feedback.Append(ctx, feedback.Feedback{
					TraceID: id, Time: base.Add(time.Duration(v.seconds) * time.Second),
					Reviewer: v.reviewer, Verdict: v.verdict,
				}.Record()))
			}

			folder, err := c.Folder(ctx, "a")

			require.NoError(t, err)
			var got []feedback.Verdict
			for _, correction := range folder.Corrections {
				got = append(got, correction.Verdict)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCompactorReadFailures(t *testing.T) {
	type args struct {
		method  string
		store   string
		allowed int
	}
	tcs := []struct {
		name string
		args args
	}{
		{"folder initial items", args{"folder", "items", 0}},
		{"folder compactable items", args{"folder", "items", 1}},
		{"folder verdicts", args{"folder", "verdicts", 0}},
		{"proposal folder", args{"propose", "items", 0}},
		{"proposal runs", args{"propose", "traces", 0}},
		{"check compaction", args{"check", "items", 0}},
		{"record compaction", args{"record", "items", 0}},
		{"coverage traces", args{"coverage", "traces", 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, _, proposed := runFolder(t)
			items := &testkit.FlakyKnowledge{Store: s.Knowledge, Reads: testkit.Reads{Allowed: 99, Err: assert.AnError}}
			verdicts := &testkit.FlakyFeedback{Store: s.Feedback, Reads: testkit.Reads{Allowed: 99, Err: assert.AnError}}
			traces := &testkit.FlakyTraces{Store: s.Traces, Lists: testkit.Reads{Allowed: 99, Err: assert.AnError}}
			switch tc.args.store {
			case "items":
				items.Allowed = tc.args.allowed
			case "verdicts":
				verdicts.Allowed = tc.args.allowed
			case "traces":
				traces.Lists.Allowed = tc.args.allowed
			}
			ledger := knowledge.NewLedger(items, nil, s.Clock.Now, nil)
			c := compact.New(ledger, traces, verdicts, traces, s.Clock.Now)
			var err error

			switch tc.args.method {
			case "folder":
				_, err = c.Folder(ctx, "a")
			case "propose":
				_, err = c.Propose(ctx, "a", compact.Draft{}, "ann")
			case "check":
				_, err = c.Check(ctx, llmmock.NewMockClient(gomock.NewController(t)), proposed.ID, "")
			case "record":
				_, err = c.Record(ctx, proposed.ID, nil)
			case "coverage":
				_, err = c.Coverage(ctx, proposed.ID)
			}

			assert.ErrorIs(t, err, assert.AnError)
		})
	}
}

func TestCompactorCorruptCoverage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, c, proposed := runFolder(t)
	require.NoError(t, s.Traces.Append(ctx, trace.Trace{
		ID: "check-invalid", Name: trace.NameCheck, SessionID: proposed.ID,
		Time: s.Clock.Now(), Input: json.RawMessage(`{}`), Output: json.RawMessage(`"invalid coverage"`),
	}.Record()))

	_, err := c.Coverage(ctx, proposed.ID)

	assert.ErrorIs(t, err, compact.ErrCoverageInvalid)
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
			_, err := approve(s, c, proposed.ID)
			require.ErrorIs(t, err, compact.ErrNoCoverage, "nothing approves before a check")

			_, err = c.Record(ctx, proposed.ID, tc.args)
			require.ErrorIs(t, err, tc.want.recordErr)
			got, err := approve(s, c, proposed.ID)

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

// Refusals for invalid compaction scope and membership
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

// One model call answers the coverage and it is recorded for the approval with what the call cost
// A call that fails and an answer code refuses record nothing so the approval waits for a check that holds
func TestCompactRunsCheck(t *testing.T) {
	ctx := context.Background()
	covered := `{"items":[{"old":"a","covered_by":["a"]},{"old":"b","covered_by":["a"]}]}`
	type args struct {
		output string
		// The call fails with it
		callErr error
		// Every append of a check fails with it
		appendErr error
	}
	// A check trace as the test reads it back
	type check struct {
		model    string
		usage    record.Usage
		duration int64
	}
	type want struct {
		err        error
		checks     []check
		approveErr error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a check is recorded with the model that answered and lets the compaction through",
			args{covered, nil, nil},
			want{nil, []check{{"claude-haiku-4-5", record.Usage{InputTokens: 12, OutputTokens: 3, CostUSD: 0.01}, 2000}}, nil},
		},
		{"a failed call records nothing", args{"", errModel, nil}, want{errModel, []check{}, compact.ErrNoCoverage}},
		{"an answer that does not decode is refused", args{`{"items":"all"}`, nil, nil}, want{compact.ErrCoverageInvalid, []check{}, compact.ErrNoCoverage}},
		{
			"an answer naming an item outside the compaction is refused",
			args{`{"items":[{"old":"z","covered_by":["a"]}]}`, nil, nil},
			want{compact.ErrCoverageInvalid, []check{}, compact.ErrNoCoverage},
		},
		{"a check that fails to append is refused", args{covered, nil, assert.AnError}, want{assert.AnError, []check{}, compact.ErrNoCoverage}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, _, proposed := runFolder(t)
			checks := &testkit.FlakyTraces{Store: s.Traces, Appends: testkit.Reads{Err: tc.args.appendErr}}
			c := compact.New(s.Ledger, s.Traces, s.Feedback, checks, s.Clock.Now)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
				assert.Equal(t, compact.CoverageRules, req.System)
				assert.True(t, strings.Contains(req.Prompt, "## Old items") && strings.Contains(req.Prompt, "Replaces: a, b"), req.Prompt)
				return llm.Response{
					Output:      json.RawMessage(tc.args.output),
					InputTokens: 12, OutputTokens: 3, CostUSD: 0.01, Duration: 2 * time.Second, Model: "claude-haiku-4-5",
				}, tc.args.callErr
			})

			_, err := c.Check(ctx, client, proposed.ID, "haiku")

			found, listErr := s.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameCheck)})
			require.NoError(t, listErr)
			got := make([]check, 0, len(found))
			for _, tr := range found {
				require.NoError(t, schema.Validate(string(trace.NameCheck), tr))
				got = append(got, check{tr.Model, tr.Usage, tr.DurationMS})
			}
			_, approveErr := approve(s, c, proposed.ID)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.checks, got)
			assert.ErrorIs(t, approveErr, tc.want.approveErr)
		})
	}
}

// One compact trace per model draft with measured execution metadata
// A fixable refusal is sent back once and a folder with a pending compaction asks nothing
func TestCompactDraftTrace(t *testing.T) {
	ctx := context.Background()
	draft := `{"items":[{"id":"a","kind":"judgment","content":"Use git -C <dir> and never cd before git","from":["a","b"],` +
		`"producer":"session","labels":{"repo":["nodloop"]}}]}`
	// Two judgments of one place that the ledger refuses as an overlap the model can fix
	overlap := `{"items":[{"id":"a","kind":"judgment","content":"Use git -C <dir>","from":["a"],"producer":"session","labels":{"repo":["nodloop"]}},` +
		`{"id":"b","kind":"judgment","content":"Never cd before git","from":["b"],"producer":"session","labels":{"repo":["nodloop"]}}]}`
	// One answer of the model in the order of the calls
	type answer struct {
		res llm.Response
		err error
	}
	answered := func(output string) answer {
		return answer{res: llm.Response{Output: json.RawMessage(output), InputTokens: 40, OutputTokens: 9, Model: "claude-sonnet-5"}}
	}
	down := answer{err: errModel}
	type args struct {
		answers []answer
		// The compaction the folder waits for and empty for none
		pending string
		// Every append of a compact trace fails with it
		appendErr error
	}
	// A compact trace as the test reads it back
	type traced struct {
		subject string
		// Its session names the compaction the draft proposed
		session bool
		model   string
		usage   record.Usage
		failed  bool
	}
	type want struct {
		err    error
		traces []traced
	}
	once := record.Usage{InputTokens: 40, OutputTokens: 9}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a proposed draft names its compaction with the model that answered",
			args{[]answer{answered(draft)}, "", nil},
			want{nil, []traced{{"a", true, "claude-sonnet-5", once, false}}},
		},
		{
			"a failed call keeps the model asked and the error",
			args{[]answer{down}, "", nil},
			want{errModel, []traced{{"a", false, "sonnet", record.Usage{}, true}}},
		},
		{
			"an answer that does not decode is refused at once",
			args{[]answer{answered(`{"items":"none"}`)}, "", nil},
			want{compact.ErrDraftInvalid, []traced{{"a", false, "claude-sonnet-5", once, true}}},
		},
		{
			"a fixable refusal is sent back once and the second draft proposed with the usage of both calls",
			args{[]answer{answered(overlap), answered(draft)}, "", nil},
			want{nil, []traced{{"a", true, "claude-sonnet-5", record.Usage{InputTokens: 80, OutputTokens: 18}, false}}},
		},
		{
			"a second call that fails keeps its error",
			args{[]answer{answered(overlap), down}, "", nil},
			want{errModel, []traced{{"a", false, "claude-sonnet-5", once, true}}},
		},
		{
			"a second refusal is returned as it is",
			args{[]answer{answered(overlap), answered(overlap)}, "", nil},
			want{knowledge.ErrCompactionOverlap, []traced{{"a", false, "claude-sonnet-5", record.Usage{InputTokens: 80, OutputTokens: 18}, true}}},
		},
		{"a folder with a pending compaction is refused before any call", args{nil, "c-pending", nil}, want{knowledge.ErrCompactionPending, []traced{}}},
		{"a trace that fails to append joins the error of the draft", args{[]answer{answered(draft)}, "", assert.AnError}, want{assert.AnError, []traced{}}},
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
			checks := &testkit.FlakyTraces{Store: s.Traces, Appends: testkit.Reads{Err: tc.args.appendErr}}
			c := compact.New(s.Ledger, s.Traces, s.Feedback, checks, s.Clock.Now)
			f, err := c.Folder(ctx, "a")
			require.NoError(t, err)
			f.Pending = tc.args.pending
			client := llmmock.NewMockClient(gomock.NewController(t))
			calls := 0
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
				a := tc.args.answers[calls]
				calls++
				return a.res, a.err
			}).Times(len(tc.args.answers))

			proposed, err := c.Draft(ctx, client, f, "sonnet", "")

			found, listErr := s.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameCompact)})
			require.NoError(t, listErr)
			got := make([]traced, 0, len(found))
			for _, tr := range found {
				require.NoError(t, schema.Validate(string(trace.NameCompact), tr))
				got = append(got, traced{tr.Subject, tr.SessionID != "" && tr.SessionID == proposed.ID, tr.Model, tr.Usage, tr.Error != ""})
			}
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.traces, got)
		})
	}
}
