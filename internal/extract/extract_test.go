package extract_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const longLine = "I changed into the repository directory first and then ran git status there for you"

var pass = extract.Critique{States: true, Holds: true, Fits: true, Why: "ok"}

// The text of the waiting candidate quote
const quoted = "Quote a path with spaces in a shell command"

// A session run of repo nodloop and dir cmd in host claude-code that a person edited
// It reaches the approved item git-c and the waiting candidate quote
// The waiting candidate far of another repository never reaches it
func corrected(t *testing.T) (testkit.Stores, *extract.Extractor, string) {
	t.Helper()
	ctx := context.Background()
	s := testkit.Open(t)
	output, err := json.Marshal("cd repo && git status\n" + longLine)
	require.NoError(t, err)
	run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}, "dir": {"cmd"}, "host": {"claude-code"}}, nil, output, s.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, s.Traces.Append(ctx, run.Record()))
	fb, err := feedback.New(run.ID, feedback.VerdictEdit, feedback.ReasonApproach, "use git -C", json.RawMessage(`"git -C repo status"`), "ann", s.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, s.Feedback.Append(ctx, fb.Record()))
	_, _, err = s.Ledger.Propose(ctx, knowledge.Knowledge{
		ID: "git-c", Kind: knowledge.KindJudgment, Content: "Prefer git -C over cd", Author: "author",
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run.ID}},
	})
	require.NoError(t, err)
	require.NoError(t, testkit.Err(s.Ledger.Approve(ctx, "git-c", 1, "ann")))
	for id, repo := range map[string]string{"quote": "nodloop", "far": "other"} {
		_, _, err = s.Ledger.Propose(ctx, knowledge.Knowledge{
			ID: id, Kind: knowledge.KindJudgment, Content: quoted, Author: "claude", Drafted: true,
			Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {repo}}},
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"r-earlier"}},
		})
		require.NoError(t, err)
	}
	return s, extract.New(s.Ledger, s.Traces, s.Feedback, s.Clock.Now), run.ID
}

func TestExtractorPropose(t *testing.T) {
	type args struct {
		draft    extract.Draft
		critique extract.Critique
		// Retired before the reaction is read so the run reaches none of them
		retired []string
	}
	type result struct {
		related string
		// The candidate is the waiting one with the run joined to its evidence
		corroborated bool
		candidate    string
		version      int
		labels       trace.Labels
		// The candidate text which a corroborate keeps as written
		content string
		status  knowledge.Status
		drafted bool
		// The candidate evidence holds the run
		cites bool
	}
	type want struct {
		err error
		// In the error text the redraft reads as what to fix
		hint   string
		result result
	}
	// A result whose candidate waits for a person with the run in its evidence
	waiting := func(r result) result {
		r.status, r.drafted, r.cites = knowledge.StatusCandidate, true, true
		return r
	}
	// The fields of a result the cases compare
	// A result without a related item or a candidate leaves those fields zero
	viewOf := func(got extract.Result, runID string) result {
		view := result{corroborated: got.Corroborated()}
		if got.Related != nil {
			view.related = got.Related.ID
		}
		if c := got.Candidate; c != nil {
			view.candidate, view.version, view.labels, view.content = c.ID, c.Version, c.Run.Labels, c.Content
			view.status, view.drafted, view.cites = c.Status, c.Drafted, slices.Contains(c.Evidence.FeedbackTraceIDs, runID)
		}
		return view
	}
	lesson := "Run git with -C <dir> instead of changing into the directory first"
	nodloop := trace.Labels{"repo": {"nodloop"}}
	draft := func(relation extract.Relation, relatesTo, content string, keys ...string) extract.Draft {
		return extract.Draft{Relation: relation, RelatesTo: relatesTo, Kind: knowledge.KindJudgment, Content: content, Keys: keys}
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an add proposes a new item scoped to the keys it kept", args{draft: draft(extract.RelationAdd, "", lesson, "repo"), critique: pass},
			want{result: waiting(result{candidate: "k-generated", version: 1, labels: nodloop, content: lesson})}},
		{"an add that keeps the host is scoped without it", args{draft: draft(extract.RelationAdd, "", lesson, "repo", "host"), critique: pass},
			want{result: waiting(result{candidate: "k-generated", version: 1, labels: nodloop, content: lesson})}},
		{"an add without keys reaches every run of the producer", args{draft: draft(extract.RelationAdd, "", lesson), critique: pass},
			want{result: waiting(result{candidate: "k-generated", version: 1, content: lesson})}},
		{"an update proposes the next version with the scope of the item", args{draft: draft(extract.RelationUpdate, "git-c", lesson, "dir"), critique: pass},
			want{result: waiting(result{related: "git-c", candidate: "git-c", version: 2, labels: nodloop, content: lesson})}},
		{
			"an update may sharpen the item with one number",
			args{draft: draft(extract.RelationUpdate, "git-c", lesson+" and cd at most 1 level"), critique: pass},
			want{result: waiting(result{related: "git-c", candidate: "git-c", version: 2, labels: nodloop, content: lesson + " and cd at most 1 level"})},
		},
		{
			"an update that adds a case to the item is refused",
			args{draft: draft(extract.RelationUpdate, "git-c", lesson+" and status -0.34 maps to 2"), critique: pass},
			want{err: extract.ErrCaseList},
		},
		{
			"an update of a candidate proposes its next version with its scope",
			args{draft: draft(extract.RelationUpdate, "quote", quoted+" or a glob"), critique: pass},
			want{result: waiting(result{related: "quote", candidate: "quote", version: 2, labels: nodloop, content: quoted + " or a glob"})},
		},
		{"a duplicate proposes nothing and names the item", args{draft: draft(extract.RelationDuplicate, "git-c", lesson), critique: pass},
			want{result: result{related: "git-c"}}},
		{
			"a duplicate of a candidate corroborates it",
			args{draft: draft(extract.RelationDuplicate, "quote", "Quote paths that hold spaces"), critique: pass},
			want{result: waiting(result{related: "quote", corroborated: true, candidate: "quote", version: 1, labels: nodloop, content: quoted})},
		},
		{
			"a conflict proposes nothing and names the item",
			args{draft: draft(extract.RelationConflict, "git-c", "Change into the directory before git"), critique: pass},
			want{result: result{related: "git-c"}},
		},
		{
			"a conflict with a candidate proposes nothing and names it",
			args{draft: draft(extract.RelationConflict, "quote", "Never quote a path in a shell command"), critique: pass},
			want{result: result{related: "quote"}},
		},
		{"a duplicate of a candidate the run never reached is refused", args{draft: draft(extract.RelationDuplicate, "far", quoted), critique: pass},
			want{err: extract.ErrRelationInvalid, hint: "which is no approved item or waiting candidate the run reaches"}},
		{"an add that names an item is refused", args{draft: draft(extract.RelationAdd, "git-c", lesson), critique: pass},
			want{err: extract.ErrRelationInvalid, hint: "add names git-c"}},
		{"a duplicate of an item the run never reached is refused", args{draft: draft(extract.RelationDuplicate, "other", lesson), critique: pass},
			want{err: extract.ErrRelationInvalid, hint: "which is no approved item or waiting candidate the run reaches"}},
		{
			"a relation other than add on a run that reaches nothing names add",
			args{draft: draft(extract.RelationConflict, "", lesson), critique: pass, retired: []string{"git-c", "quote"}},
			want{err: extract.ErrRelationInvalid, hint: "so the relation is add"},
		},
		{"an unknown relation is refused", args{draft: draft("merge", "", lesson), critique: pass}, want{err: extract.ErrRelationInvalid}},
		{"two sentences are refused", args{draft: draft(extract.RelationAdd, "", "Use git -C. Never cd first"), critique: pass},
			want{err: extract.ErrNotLesson}},
		{"a content that copies the output is refused", args{draft: draft(extract.RelationAdd, "", "Never say "+longLine), critique: pass},
			want{err: extract.ErrNotLesson}},
		{"a key the run does not carry is refused and names the keys it carries", args{draft: draft(extract.RelationAdd, "", lesson, "task"), critique: pass},
			want{err: extract.ErrKeyUnknown, hint: "keys lists only dir or host or repo or nothing"}},
		{
			"a critique with a false answer is refused",
			args{draft: draft(extract.RelationAdd, "", lesson), critique: extract.Critique{States: true, Holds: false, Fits: true, Why: "only this run"}},
			want{err: extract.ErrCriticRefused},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, e, runID := corrected(t)
			for _, id := range tc.args.retired {
				require.NoError(t, testkit.Err(s.Ledger.Retire(ctx, id, 1, "ann")))
			}
			r, err := e.Reaction(ctx, runID)
			require.NoError(t, err)

			got, err := e.Propose(ctx, r, tc.args.draft, tc.args.critique, "")

			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, fmt.Sprint(err), tc.want.hint)
			assert.Equal(t, tc.want.result, viewOf(got, runID))
		})
	}
}

// A run without a correction teaches nothing
func TestExtractorReactionNotCorrected(t *testing.T) {
	ctx := context.Background()
	s, e, runID := corrected(t)
	fb, err := feedback.New(runID, feedback.VerdictApprove, "", "", nil, "ann", s.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, s.Feedback.Append(ctx, fb.Record()))

	_, err = e.Reaction(ctx, runID)

	assert.ErrorIs(t, err, extract.ErrNotCorrected)
}

// The reaction reads the reconciled verdict: a person's wins and a run the conversation and the capture dispute teaches nothing
func TestExtractorReactionVerdict(t *testing.T) {
	type verdict struct {
		reviewer string
		verdict  feedback.Verdict
	}
	type want struct {
		reviewer string
		err      error
	}
	tcs := []struct {
		name string
		// Recorded oldest first a minute apart
		args []verdict
		want want
	}{
		{"a person's approve wins over a later inferred correction",
			[]verdict{{"ann", feedback.VerdictApprove}, {feedback.ReviewerSession, feedback.VerdictReject}}, want{err: extract.ErrNotCorrected}},
		{"a person's correction wins over a later inferred approve",
			[]verdict{{"ann", feedback.VerdictReject}, {feedback.ReviewerNodloop, feedback.VerdictApprove}}, want{reviewer: "ann"}},
		{"readers that agree teach with the record of the conversation",
			[]verdict{{feedback.ReviewerNodloop, feedback.VerdictReject}, {feedback.ReviewerSession, feedback.VerdictReject}},
			want{reviewer: feedback.ReviewerSession}},
		{"a run the readers dispute teaches nothing",
			[]verdict{{feedback.ReviewerSession, feedback.VerdictReject}, {feedback.ReviewerNodloop, feedback.VerdictApprove}},
			want{err: extract.ErrNotCorrected}},
		{"the capture alone teaches", []verdict{{feedback.ReviewerNodloop, feedback.VerdictReject}}, want{reviewer: feedback.ReviewerNodloop}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s := testkit.Open(t)
			run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}}, nil, json.RawMessage(`"x"`), s.Clock.Now())
			require.NoError(t, err)
			require.NoError(t, s.Traces.Append(ctx, run.Record()))
			for i, v := range tc.args {
				fb := feedback.Feedback{
					TraceID: run.ID, Time: s.Clock.Now().Add(time.Duration(i) * time.Minute), Verdict: v.verdict, Reason: "why", Reviewer: v.reviewer,
				}
				require.NoError(t, s.Feedback.Append(ctx, fb.Record()))
			}

			got, err := extract.New(s.Ledger, s.Traces, s.Feedback, s.Clock.Now).Reaction(ctx, run.ID)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.reviewer, got.Verdict.Reviewer)
		})
	}
}

func TestExtractorExtract(t *testing.T) {
	good := `{"relation":"add","kind":"judgment","content":"Run git with -C <dir> instead of changing into it","keys":["repo"]}`
	twoSentences := `{"relation":"add","kind":"judgment","content":"Use git -C. Never cd first"}`
	duplicate := `{"relation":"duplicate","relates_to":"quote","kind":"judgment","content":"Quote paths that hold spaces"}`
	refused := `{"states":true,"holds":false,"fits":true,"why":"only this run"}`
	passed := `{"states":true,"holds":true,"fits":true,"why":"ok"}`
	// What every answered call costs
	call := llm.Response{CostUSD: 0.25, InputTokens: 10, OutputTokens: 4, ThinkingTokens: 1, CacheRead: 100, CacheCreate: 50, Model: "claude-haiku-4-5"}
	usage := func(calls int, cost float64) trace.Usage {
		return trace.Usage{
			InputTokens: 10 * calls, OutputTokens: 4 * calls, ThinkingTokens: calls, CacheReadTokens: 100 * calls, CacheCreateTokens: 50 * calls,
			CostUSD: cost,
		}
	}
	// A model call that ends in error_max_turns and was charged
	maxTurns := &llm.ResultError{Subtype: "error_max_turns", CostUSD: 0.5}
	// One answer of the model in the order of the calls
	type reply struct {
		res llm.Response
		err error
	}
	type args struct {
		replies []reply
	}
	// The drafter prompts of the calls
	type prompts struct {
		drafts int
		// Prompts that hold the edit and every item and candidate the run reaches and no other
		reaching int
		// Prompts that send a refusal back
		refused int
	}
	type want struct {
		err error
		// The extract trace the extraction recorded
		conclusion extract.Conclusion
		refusals   []extract.Refusal
		usage      trace.Usage
		proposed   bool
		prompts    prompts
	}
	// Every draft reaches the same items and each draft after the first sends a refusal back
	drafted := func(n int) prompts { return prompts{drafts: n, reaching: n, refused: n - 1} }
	promptsOf := func(reqs []llm.Request) prompts {
		var p prompts
		for _, req := range reqs {
			if req.System != extract.Rules {
				continue
			}
			p.drafts++
			if strings.Contains(req.Prompt, "## Edited output") &&
				strings.Contains(req.Prompt, "## Approved items this run reaches\n\n[git-c v1 judgment approved]") &&
				strings.Contains(req.Prompt, "## Waiting candidates this run reaches\n\n[quote v1 judgment candidate]") &&
				!strings.Contains(req.Prompt, "[far ") {
				p.reaching++
			}
			if strings.Contains(req.Prompt, "## Refused") {
				p.refused++
			}
		}
		return p
	}
	replies := func(outputs ...string) []reply {
		out := make([]reply, 0, len(outputs))
		for _, o := range outputs {
			res := call
			res.Output = json.RawMessage(o)
			out = append(out, reply{res: res})
		}
		return out
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a draft the critic passes is proposed", args{replies(good, passed)},
			want{nil, extract.ConclusionProposed, []extract.Refusal{""}, usage(2, 0.5), true, drafted(1)}},
		{"a duplicate of a waiting candidate corroborates it", args{replies(duplicate, passed)},
			want{nil, extract.ConclusionCorroborated, []extract.Refusal{""}, usage(2, 0.5), true, drafted(1)}},
		{"a draft code refuses is sent back once", args{replies(twoSentences, good, passed)},
			want{nil, extract.ConclusionProposed, []extract.Refusal{extract.RefusalCode, ""}, usage(3, 0.75), true, drafted(2)}},
		{"a draft the critic refuses is sent back once", args{replies(good, refused, good, passed)},
			want{nil, extract.ConclusionProposed, []extract.Refusal{extract.RefusalCritic, ""}, usage(4, 1), true, drafted(2)}},
		{"a second critic refusal is returned", args{replies(good, refused, good, refused)},
			want{extract.ErrCriticRefused, extract.ConclusionRefused, []extract.Refusal{extract.RefusalCritic, extract.RefusalCritic}, usage(4, 1), false, drafted(2)}},
		{"a second code refusal is returned", args{replies(twoSentences, twoSentences)},
			want{extract.ErrNotLesson, extract.ConclusionRefused, []extract.Refusal{extract.RefusalCode, extract.RefusalCode}, usage(2, 0.5), false, drafted(2)}},
		{"a draft that does not decode fails", args{replies(`not json`)},
			want{extract.ErrDraftInvalid, extract.ConclusionFailed, []extract.Refusal{extract.RefusalModel}, usage(1, 0.25), false, drafted(1)}},
		{"a failed critic call keeps the cost it was charged", args{append(replies(good), reply{err: maxTurns})},
			want{maxTurns, extract.ConclusionFailed, []extract.Refusal{extract.RefusalModel}, usage(1, 0.75), false, drafted(1)}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, e, runID := corrected(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			var reqs []llm.Request
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(len(tc.args.replies)).DoAndReturn(
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					r := tc.args.replies[len(reqs)]
					reqs = append(reqs, req)
					return r.res, r.err
				})
			meter := llm.NewMeter(client)

			got, err := e.Extract(ctx, extract.NewClaudeDrafter(meter, ""), extract.NewClaudeCritic(meter, ""), runID, "")

			require.ErrorIs(t, err, tc.want.err)
			rec, tr := recorded(t, s, extract.PathModel, runID)
			assert.Equal(t, tc.want.conclusion, rec.Conclusion)
			assert.Equal(t, tc.want.refusals, refusalsOf(rec))
			assert.Equal(t, tc.want.proposed, got.Candidate != nil)
			assert.Equal(t, tc.want.prompts, promptsOf(reqs))
			assert.Equal(t, record.Usage(tc.want.usage), tr.Usage)
			if tc.want.usage.OutputTokens > 0 {
				assert.Equal(t, "claude-haiku-4-5", tr.Model, "the trace names the model that answered")
			}
			assert.Positive(t, tr.DurationMS)
			assert.Zero(t, tr.DurationMS%testkit.ClockStep.Milliseconds(), "the duration comes from the testkit clock")
			assert.Equal(t, llm.Response{}, meter.Take(), "the extraction took the meter")
		})
	}
}

// The one extract trace of the run with its path checked
func recorded(t *testing.T, s testkit.Stores, path extract.Path, runID string) (extract.Record, record.Trace) {
	t.Helper()
	traces, err := s.Traces.List(context.Background(), record.TraceFilter{Name: string(trace.NameExtract)})
	require.NoError(t, err)
	require.Len(t, traces, 1)
	assert.Equal(t, string(path), traces[0].Subject)
	assert.Equal(t, runID, traces[0].Ref)
	require.NoError(t, schema.Validate(string(trace.NameExtract), traces[0]))
	var rec extract.Record
	require.NoError(t, json.Unmarshal(traces[0].Output, &rec))
	return rec, traces[0]
}

func refusalsOf(rec extract.Record) []extract.Refusal {
	out := make([]extract.Refusal, 0, len(rec.Attempts))
	for _, a := range rec.Attempts {
		out = append(out, a.Refusal)
	}
	return out
}

// Every conversation proposal is recorded with how it ended and the critic questions answered false
func TestExtractorProposeRecord(t *testing.T) {
	lesson := "Run git with -C <dir> instead of changing into the directory first"
	type args struct {
		draft    extract.Draft
		critique extract.Critique
		// Retired after the reaction was read
		retired []string
	}
	type want struct {
		conclusion extract.Conclusion
		refusal    extract.Refusal
		questions  []string
		candidate  *knowledge.Ref
		related    *extract.Related
	}
	gitC := &extract.Related{ID: "git-c", Version: 1, Status: knowledge.StatusApproved}
	quote := &extract.Related{ID: "quote", Version: 1, Status: knowledge.StatusCandidate}
	dupQuote := extract.Draft{Relation: extract.RelationDuplicate, RelatesTo: "quote", Kind: knowledge.KindJudgment, Content: quoted}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an add is proposed", args{draft: extract.Draft{Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: lesson, Keys: []string{"repo"}}, critique: pass},
			want{conclusion: extract.ConclusionProposed, candidate: &knowledge.Ref{ID: "k-generated", Version: 1}}},
		{"a duplicate is recorded as one", args{draft: extract.Draft{Relation: extract.RelationDuplicate, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson}, critique: pass},
			want{conclusion: extract.ConclusionDuplicate, related: gitC}},
		{"a duplicate of a candidate is recorded as corroborated", args{draft: dupQuote, critique: pass},
			want{conclusion: extract.ConclusionCorroborated, candidate: &knowledge.Ref{ID: "quote", Version: 1}, related: quote}},
		{"a conflict is recorded as one", args{draft: extract.Draft{Relation: extract.RelationConflict, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson}, critique: pass},
			want{conclusion: extract.ConclusionConflict, related: gitC}},
		{"a conflict with a candidate names it", args{draft: extract.Draft{Relation: extract.RelationConflict, RelatesTo: "quote", Kind: knowledge.KindJudgment, Content: lesson}, critique: pass},
			want{conclusion: extract.ConclusionConflict, related: quote}},
		{"a candidate retired since the reaction is a ledger refusal", args{draft: dupQuote, critique: pass, retired: []string{"quote"}},
			want{conclusion: extract.ConclusionRefused, refusal: extract.RefusalLedger}},
		{"a code refusal names the code", args{draft: extract.Draft{Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: "Use git -C. Never cd first"}, critique: pass},
			want{conclusion: extract.ConclusionRefused, refusal: extract.RefusalCode}},
		{"a critic refusal names the questions", args{draft: extract.Draft{Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: lesson},
			critique: extract.Critique{States: false, Holds: false, Fits: true, Why: "adds a fix"}},
			want{conclusion: extract.ConclusionRefused, refusal: extract.RefusalCritic, questions: []string{"holds", "states"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, e, runID := corrected(t)
			r, err := e.Reaction(ctx, runID)
			require.NoError(t, err)
			for _, id := range tc.args.retired {
				require.NoError(t, testkit.Err(s.Ledger.Retire(ctx, id, 1, "ann")))
			}

			_, _ = e.Propose(ctx, r, tc.args.draft, tc.args.critique, "")

			rec, tr := recorded(t, s, extract.PathConversation, runID)
			assert.Equal(t, record.Usage{}, tr.Usage, "the conversation called no model here")
			require.Len(t, rec.Attempts, 1)
			assert.Equal(t, tc.want.conclusion, rec.Conclusion)
			assert.Equal(t, tc.want.refusal, rec.Attempts[0].Refusal)
			assert.Equal(t, tc.want.questions, rec.Attempts[0].Questions)
			assert.Equal(t, tc.want.candidate, rec.Candidate)
			assert.Equal(t, tc.want.related, rec.Related)
		})
	}
}

// A classifier critic refuses on a yes under one half and the refusal names its probabilities
func TestExtractorExtractClassifierCritic(t *testing.T) {
	good := `{"relation":"add","kind":"judgment","content":"Run git with -C <dir> instead of changing into it","keys":["repo"]}`
	type args struct {
		// The body the endpoint answers every request with
		answers string
		// Drafter calls the extraction makes
		drafts int
	}
	type want struct {
		err error
		// In the error text and empty for none
		hint     string
		proposed bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a yes under one half refuses twice and names the probabilities",
			args{`{"answers":{"states":{"noul":0.9},"holds":{"noul":0.2},"fits":{"noul":0.8}}}`, 2},
			want{extract.ErrCriticRefused, "holds false: fits 0.80, holds 0.20, states 0.90", false},
		},
		{
			"a yes from every answer passes the draft",
			args{`{"answers":{"states":{"noul":0.9},"holds":{"noul":0.7},"fits":{"noul":0.8}}}`, 1},
			want{nil, "", true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			states := make(chan string, tc.args.drafts)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					State string `json:"state"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				states <- req.State
				_, _ = w.Write([]byte(tc.args.answers))
			}))
			t.Cleanup(srv.Close)
			_, e, runID := corrected(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(tc.args.drafts).Return(llm.Response{Output: json.RawMessage(good)}, nil)
			critic := classify.NewHTTP(classify.Endpoint{URL: srv.URL}, "", time.Second)

			got, err := e.Extract(context.Background(), extract.NewClaudeDrafter(llm.NewMeter(client), ""), critic, runID, "")

			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, fmt.Sprint(err), tc.want.hint)
			assert.Equal(t, tc.want.proposed, got.Candidate != nil)
			assert.True(t, strings.HasPrefix(<-states, "## Draft lesson\n\n{\"relation\":\"add\""), "a truncating endpoint still reads the draft")
		})
	}
}
