package extract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const longLine = "I changed into the repository directory first and then ran git status there for you"

var pass = extract.Critique{States: true, Holds: true, Fits: true, Why: "ok"}

// A session run of repo nodloop and dir cmd that a person edited, and the approved item git-c it reaches
func corrected(t *testing.T) (testkit.Stores, *extract.Extractor, string) {
	t.Helper()
	ctx := context.Background()
	s := testkit.Open(t)
	output, err := json.Marshal("cd repo && git status\n" + longLine)
	require.NoError(t, err)
	run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}, "dir": {"cmd"}}, nil, output, s.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, s.Traces.Append(ctx, run))
	fb, err := feedback.New(run.ID, feedback.VerdictEdit, feedback.ReasonApproach, "use git -C", json.RawMessage(`"git -C repo status"`), "", s.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, s.Feedback.Append(ctx, fb))
	_, _, err = s.Ledger.Propose(ctx, knowledge.Knowledge{
		ID: "git-c", Kind: knowledge.KindJudgment, Content: "Prefer git -C over cd", Author: "author",
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run.ID}},
	})
	require.NoError(t, err)
	require.NoError(t, testkit.Err(s.Ledger.Approve(ctx, "git-c", 1, "ann")))
	return s, extract.New(s.Ledger, s.Traces, s.Feedback, s.Clock.Now), run.ID
}

func TestExtractorPropose(t *testing.T) {
	type want struct {
		err       error
		candidate string
		version   int
		labels    trace.Labels
		related   string
	}
	lesson := "Run git with -C <dir> instead of changing into the directory first"
	tcs := []struct {
		name     string
		draft    extract.Draft
		critique extract.Critique
		want     want
	}{
		{"an add proposes a new item scoped to the keys it kept", extract.Draft{
			Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: lesson, Keys: []string{"repo"},
		}, pass, want{candidate: "k-generated", version: 1, labels: trace.Labels{"repo": {"nodloop"}}}},
		{"an add without keys reaches every run of the producer", extract.Draft{
			Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: lesson,
		}, pass, want{candidate: "k-generated", version: 1}},
		{"an update proposes the next version with the scope of the item", extract.Draft{
			Relation: extract.RelationUpdate, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson, Keys: []string{"dir"},
		}, pass, want{candidate: "git-c", version: 2, labels: trace.Labels{"repo": {"nodloop"}}, related: "git-c"}},
		{"an update may sharpen the item with one number", extract.Draft{
			Relation: extract.RelationUpdate, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson + " and cd at most 1 level",
		}, pass, want{candidate: "git-c", version: 2, labels: trace.Labels{"repo": {"nodloop"}}, related: "git-c"}},
		{"an update that adds a case to the item is refused", extract.Draft{
			Relation: extract.RelationUpdate, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson + " and status -0.34 maps to 2",
		}, pass, want{err: extract.ErrCaseList}},
		{"a duplicate proposes nothing and names the item", extract.Draft{
			Relation: extract.RelationDuplicate, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson,
		}, pass, want{related: "git-c"}},
		{"a conflict proposes nothing and names the item", extract.Draft{
			Relation: extract.RelationConflict, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: "Change into the directory before git",
		}, pass, want{related: "git-c"}},
		{"an add that names an item is refused", extract.Draft{
			Relation: extract.RelationAdd, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson,
		}, pass, want{err: extract.ErrRelationInvalid}},
		{"a duplicate of an item the run never reached is refused", extract.Draft{
			Relation: extract.RelationDuplicate, RelatesTo: "other", Kind: knowledge.KindJudgment, Content: lesson,
		}, pass, want{err: extract.ErrRelationInvalid}},
		{"an unknown relation is refused", extract.Draft{
			Relation: "merge", Kind: knowledge.KindJudgment, Content: lesson,
		}, pass, want{err: extract.ErrRelationInvalid}},
		{"two sentences are refused", extract.Draft{
			Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: "Use git -C. Never cd first",
		}, pass, want{err: extract.ErrNotLesson}},
		{"a content that copies the output is refused", extract.Draft{
			Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: "Never say " + longLine,
		}, pass, want{err: extract.ErrNotLesson}},
		{"a key the run does not carry is refused", extract.Draft{
			Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: lesson, Keys: []string{"task"},
		}, pass, want{err: extract.ErrKeyUnknown}},
		{"a critique with a false answer is refused", extract.Draft{
			Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: lesson,
		}, extract.Critique{States: true, Holds: false, Fits: true, Why: "only this run"}, want{err: extract.ErrCriticRefused}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			_, e, runID := corrected(t)
			r, err := e.Reaction(ctx, runID)
			require.NoError(t, err)

			got, err := e.Propose(ctx, r, tc.draft, tc.critique, "")

			require.ErrorIs(t, err, tc.want.err)
			if tc.want.err != nil {
				return
			}
			if tc.want.related == "" {
				assert.Nil(t, got.Related)
			} else {
				require.NotNil(t, got.Related)
				assert.Equal(t, tc.want.related, got.Related.ID)
			}
			if tc.want.candidate == "" {
				assert.Nil(t, got.Candidate)
				return
			}
			require.NotNil(t, got.Candidate)
			assert.Equal(t, tc.want.candidate, got.Candidate.ID)
			assert.Equal(t, tc.want.version, got.Candidate.Version)
			assert.Equal(t, tc.want.labels, got.Candidate.Run.Labels)
			assert.Equal(t, knowledge.StatusCandidate, got.Candidate.Status)
			assert.True(t, got.Candidate.Drafted)
			assert.Contains(t, got.Candidate.Evidence.FeedbackTraceIDs, runID)
		})
	}
}

// A run without a correction teaches nothing
func TestExtractorReactionNotCorrected(t *testing.T) {
	ctx := context.Background()
	s, e, runID := corrected(t)
	fb, err := feedback.New(runID, feedback.VerdictApprove, "", "", nil, "", s.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, s.Feedback.Append(ctx, fb))

	_, err = e.Reaction(ctx, runID)

	assert.ErrorIs(t, err, extract.ErrNotCorrected)
}

func TestExtractorExtract(t *testing.T) {
	good := `{"relation":"add","kind":"judgment","content":"Run git with -C <dir> instead of changing into it","keys":["repo"]}`
	twoSentences := `{"relation":"add","kind":"judgment","content":"Use git -C. Never cd first"}`
	refused := `{"states":true,"holds":false,"fits":true,"why":"only this run"}`
	passed := `{"states":true,"holds":true,"fits":true,"why":"ok"}`
	type want struct {
		err error
		// The extract trace the extraction recorded
		conclusion extract.Conclusion
		refusals   []extract.Refusal
	}
	tcs := []struct {
		name    string
		answers []string
		want    want
	}{
		{"a draft the critic passes is proposed", []string{good, passed}, want{nil, extract.ConclusionProposed, []extract.Refusal{""}}},
		{"a draft code refuses is sent back once", []string{twoSentences, good, passed},
			want{nil, extract.ConclusionProposed, []extract.Refusal{extract.RefusalCode, ""}}},
		{"a draft the critic refuses is sent back once", []string{good, refused, good, passed},
			want{nil, extract.ConclusionProposed, []extract.Refusal{extract.RefusalCritic, ""}}},
		{"a second critic refusal is returned", []string{good, refused, good, refused},
			want{extract.ErrCriticRefused, extract.ConclusionRefused, []extract.Refusal{extract.RefusalCritic, extract.RefusalCritic}}},
		{"a second code refusal is returned", []string{twoSentences, twoSentences},
			want{extract.ErrNotLesson, extract.ConclusionRefused, []extract.Refusal{extract.RefusalCode, extract.RefusalCode}}},
		{"a draft that does not decode fails", []string{`not json`},
			want{extract.ErrDraftInvalid, extract.ConclusionFailed, []extract.Refusal{extract.RefusalModel}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, e, runID := corrected(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			calls := 0
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(len(tc.answers)).DoAndReturn(
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					if req.System == extract.Rules {
						assert.Contains(t, req.Prompt, "## Edited output")
						assert.Contains(t, req.Prompt, "[git-c v1 judgment]")
					}
					if calls > 0 && req.System == extract.Rules {
						assert.True(t, strings.Contains(req.Prompt, "## Refused"), req.Prompt)
					}
					calls++
					return llm.Response{Output: json.RawMessage(tc.answers[calls-1])}, nil
				})

			got, err := e.Extract(ctx, extract.NewClaudeDrafter(client, ""), extract.NewClaudeCritic(client, ""), runID, "")

			require.ErrorIs(t, err, tc.want.err)
			rec := recorded(t, s, extract.PathModel, runID)
			assert.Equal(t, tc.want.conclusion, rec.Conclusion)
			assert.Equal(t, tc.want.refusals, refusalsOf(rec))
			assert.Equal(t, tc.want.err == nil, got.Candidate != nil)
		})
	}
}

// The one extract trace of the run with its path checked
func recorded(t *testing.T, s testkit.Stores, path extract.Path, runID string) extract.Record {
	t.Helper()
	traces, err := s.Traces.List(context.Background(), trace.Filter{Name: trace.NameExtract})
	require.NoError(t, err)
	require.Len(t, traces, 1)
	assert.Equal(t, string(path), traces[0].Subject)
	assert.Equal(t, runID, traces[0].Ref)
	var rec extract.Record
	require.NoError(t, json.Unmarshal(traces[0].Output, &rec))
	return rec
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
	type want struct {
		conclusion extract.Conclusion
		refusal    extract.Refusal
		questions  []string
	}
	tcs := []struct {
		name     string
		draft    extract.Draft
		critique extract.Critique
		want     want
	}{
		{"an add is proposed", extract.Draft{Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: lesson, Keys: []string{"repo"}},
			pass, want{extract.ConclusionProposed, "", nil}},
		{"a duplicate is recorded as one", extract.Draft{Relation: extract.RelationDuplicate, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson},
			pass, want{extract.ConclusionDuplicate, "", nil}},
		{"a conflict is recorded as one", extract.Draft{Relation: extract.RelationConflict, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson},
			pass, want{extract.ConclusionConflict, "", nil}},
		{"a code refusal names the code", extract.Draft{Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: "Use git -C. Never cd first"},
			pass, want{extract.ConclusionRefused, extract.RefusalCode, nil}},
		{"a critic refusal names the questions", extract.Draft{Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: lesson},
			extract.Critique{States: false, Holds: false, Fits: true, Why: "adds a fix"}, want{extract.ConclusionRefused, extract.RefusalCritic, []string{"holds", "states"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, e, runID := corrected(t)
			r, err := e.Reaction(ctx, runID)
			require.NoError(t, err)

			_, _ = e.Propose(ctx, r, tc.draft, tc.critique, "")

			rec := recorded(t, s, extract.PathConversation, runID)
			require.Len(t, rec.Attempts, 1)
			assert.Equal(t, tc.want.conclusion, rec.Conclusion)
			assert.Equal(t, tc.want.refusal, rec.Attempts[0].Refusal)
			assert.Equal(t, tc.want.questions, rec.Attempts[0].Questions)
		})
	}
}

// A classifier critic refuses on a yes under one half and the refusal names its probabilities
func TestExtractorExtractClassifierCritic(t *testing.T) {
	good := `{"relation":"add","kind":"judgment","content":"Run git with -C <dir> instead of changing into it","keys":["repo"]}`
	var state string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			State string `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		state = req.State
		_, _ = w.Write([]byte(`{"answers":{"states":{"noul":0.9},"holds":{"noul":0.2},"fits":{"noul":0.8}}}`))
	}))
	t.Cleanup(srv.Close)
	_, e, runID := corrected(t)
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(2).Return(llm.Response{Output: json.RawMessage(good)}, nil)
	critic := classify.NewHTTP(classify.Endpoint{URL: srv.URL}, "", time.Second)

	_, err := e.Extract(context.Background(), extract.NewClaudeDrafter(client, ""), critic, runID, "")

	require.ErrorIs(t, err, extract.ErrCriticRefused)
	assert.Contains(t, err.Error(), "holds false: fits 0.80, holds 0.20, states 0.90")
	assert.True(t, strings.HasPrefix(state, "## Draft lesson\n\n{\"relation\":\"add\""), "a truncating endpoint still reads the draft")
}
