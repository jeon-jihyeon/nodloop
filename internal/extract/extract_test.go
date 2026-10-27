package extract_test

import (
	"cmp"
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

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/classify/classifymock"
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

// The text of the candidate dropped a person retired
const bullets = "Answer in short bullets"

// A session run of repo nodloop and dir cmd in host claude-code that a person edited
// Approved and waiting and retired versions reached by the fixture run
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
	_, _, err = s.Ledger.Propose(ctx, knowledge.Knowledge{
		ID: "dropped", Kind: knowledge.KindMeaning, Content: bullets, Author: "claude", Drafted: true,
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"r-earlier"}},
	})
	require.NoError(t, err)
	require.NoError(t, testkit.Err(s.Ledger.Retire(ctx, "dropped", 1, "ann")))
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
		// The candidate is a retired version proposed again
		again     bool
		candidate string
		version   int
		labels    trace.Labels
		// The candidate text which a corroborate keeps as written
		content string
		status  knowledge.Status
		drafted bool
		// The candidate evidence holds the run
		cites bool
		// Runs the candidate evidence holds
		runs int
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
		view := result{corroborated: got.Corroborated(), again: got.ProposedAgain()}
		if got.Related != nil {
			view.related = got.Related.ID
		}
		if c := got.Candidate; c != nil {
			view.candidate, view.version, view.labels, view.content = c.ID, c.Version, c.Run.Labels, c.Content
			view.status, view.drafted, view.cites = c.Status, c.Drafted, slices.Contains(c.Evidence.FeedbackTraceIDs, runID)
			view.runs = len(c.Evidence.FeedbackTraceIDs)
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
			want{result: waiting(result{candidate: "k-generated", version: 1, labels: nodloop, content: lesson, runs: 1})}},
		{"an add that keeps the host is scoped without it", args{draft: draft(extract.RelationAdd, "", lesson, "repo", "host"), critique: pass},
			want{result: waiting(result{candidate: "k-generated", version: 1, labels: nodloop, content: lesson, runs: 1})}},
		{"an add without keys reaches every run of the producer", args{draft: draft(extract.RelationAdd, "", lesson), critique: pass},
			want{result: waiting(result{candidate: "k-generated", version: 1, content: lesson, runs: 1})}},
		{"an update proposes the next version with the scope of the item", args{draft: draft(extract.RelationUpdate, "git-c", lesson, "dir"), critique: pass},
			want{result: waiting(result{related: "git-c", candidate: "git-c", version: 2, labels: nodloop, content: lesson, runs: 1})}},
		{
			"an update may sharpen the item with one number",
			args{draft: draft(extract.RelationUpdate, "git-c", lesson+" and cd at most 1 level"), critique: pass},
			want{result: waiting(result{related: "git-c", candidate: "git-c", version: 2, labels: nodloop, content: lesson + " and cd at most 1 level", runs: 1})},
		},
		{
			"an update that adds a case to the item is refused",
			args{draft: draft(extract.RelationUpdate, "git-c", lesson+" and status -0.34 maps to 2"), critique: pass},
			want{err: extract.ErrCaseList},
		},
		{
			"an update of a candidate proposes its next version with its scope and its evidence",
			args{draft: draft(extract.RelationUpdate, "quote", quoted+" or a glob"), critique: pass},
			want{result: waiting(result{related: "quote", candidate: "quote", version: 2, labels: nodloop, content: quoted + " or a glob", runs: 2})},
		},
		{"a duplicate proposes nothing and names the item", args{draft: draft(extract.RelationDuplicate, "git-c", lesson), critique: pass},
			want{result: result{related: "git-c"}}},
		{
			"a duplicate of a candidate corroborates it",
			args{draft: draft(extract.RelationDuplicate, "quote", "Quote paths that hold spaces"), critique: pass},
			want{result: waiting(result{related: "quote", corroborated: true, candidate: "quote", version: 1, labels: nodloop, content: quoted, runs: 2})},
		},
		{
			"a duplicate of a retired version proposes it again with its text and scope and the run joined",
			args{draft: draft(extract.RelationDuplicate, "dropped", "Keep answers to a few bullets"), critique: pass},
			want{result: waiting(result{related: "dropped", again: true, candidate: "dropped", version: 2, labels: nodloop, content: bullets, runs: 2})},
		},
		{
			"a conflict proposes the next version of the item with the run alone as evidence",
			args{draft: draft(extract.RelationConflict, "git-c", "Change into the directory before git"), critique: pass},
			want{result: waiting(result{related: "git-c", candidate: "git-c", version: 2, labels: nodloop, content: "Change into the directory before git", runs: 1})},
		},
		{
			"a conflict with a candidate proposes its next version with the run alone as evidence",
			args{draft: draft(extract.RelationConflict, "quote", "Never quote a path in a shell command"), critique: pass},
			want{result: waiting(result{related: "quote", candidate: "quote", version: 2, labels: nodloop, content: "Never quote a path in a shell command", runs: 1})},
		},
		{
			"an update of a retired version is refused",
			args{draft: draft(extract.RelationUpdate, "dropped", bullets+" of three lines"), critique: pass},
			want{err: extract.ErrRelationInvalid, hint: "only a duplicate names a retired version"},
		},
		{
			"a conflict with a retired version is refused",
			args{draft: draft(extract.RelationConflict, "dropped", "Answer in long paragraphs"), critique: pass},
			want{err: extract.ErrRelationInvalid, hint: "only a duplicate names a retired version"},
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
		{"empty content is refused", args{draft: draft(extract.RelationAdd, "", "  "), critique: pass},
			want{err: extract.ErrNotLesson}},
		{"content beyond the character cap is refused", args{draft: draft(extract.RelationAdd, "", strings.Repeat("a", 301)), critique: pass},
			want{err: extract.ErrNotLesson}},
		{"a carriage return is refused", args{draft: draft(extract.RelationAdd, "", "Use git\rwith -C"), critique: pass},
			want{err: extract.ErrNotLesson}},
		{"a Unicode line separator is refused", args{draft: draft(extract.RelationAdd, "", "Use git\u2028with -C"), critique: pass},
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

// A reaction resolves only for a successful run whose verdict still corrects it
// Only a successful run with a current correction can resolve
func TestExtractorReaction(t *testing.T) {
	type args struct {
		// The trace the reaction reads and the corrected run when empty
		run string
		// Verdicts of ann recorded on the corrected run after its edit
		later []feedback.Verdict
		// Reads of the verdicts and of the ledger allowed before each fails
		verdictReads, ledgerReads int
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a corrected run resolves", args{verdictReads: 1, ledgerReads: 1}, nil},
		{"an approve after the correction teaches nothing", args{later: []feedback.Verdict{feedback.VerdictApprove}, verdictReads: 1, ledgerReads: 1},
			extract.ErrNotCorrected},
		{"a withdrawn correction teaches nothing", args{later: []feedback.Verdict{feedback.VerdictWithdraw}, verdictReads: 1, ledgerReads: 1},
			extract.ErrNotCorrected},
		{"an unknown run fails", args{run: "t-unknown", verdictReads: 1, ledgerReads: 1}, record.ErrTraceNotFound},
		{"a trace that is no run fails", args{run: "t-classify", verdictReads: 1, ledgerReads: 1}, trace.ErrNotRun},
		{"a failed run fails", args{run: "t-failed", verdictReads: 1, ledgerReads: 1}, trace.ErrFailedRun},
		{"a verdict read that fails fails the reaction", args{ledgerReads: 1}, assert.AnError},
		{"a ledger read that fails fails the reaction", args{verdictReads: 1}, assert.AnError},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, _, runID := corrected(t)
			require.NoError(t, s.Traces.Append(ctx, trace.Trace{ID: "t-classify", Name: trace.NameClassify, Time: s.Clock.Now()}.Record()))
			failed, err := trace.NewRun("session", "", nil, nil, json.RawMessage(`"x"`), s.Clock.Now())
			require.NoError(t, err)
			failed.ID, failed.Error = "t-failed", "timeout"
			require.NoError(t, s.Traces.Append(ctx, failed.Record()))
			for _, v := range tc.args.later {
				fb, err := feedback.New(runID, v, "", "", nil, "ann", s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Feedback.Append(ctx, fb.Record()))
			}
			verdicts := &testkit.FlakyFeedback{Store: s.Feedback, Reads: testkit.Reads{Allowed: tc.args.verdictReads, Err: assert.AnError}}
			items := &testkit.FlakyKnowledge{Store: s.Knowledge, Reads: testkit.Reads{Allowed: tc.args.ledgerReads, Err: assert.AnError}}
			e := extract.New(knowledge.NewLedger(items, nil, s.Clock.Now, nil), s.Traces, verdicts, s.Clock.Now)

			got, err := e.Reaction(ctx, cmp.Or(tc.args.run, runID))

			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, tc.want == nil, got.Run.ID == runID, "only a resolved reaction holds the run")
		})
	}
}

// A run that reaches nothing says so under each list and a retired version names who retired it and why
func TestReactionString(t *testing.T) {
	run := trace.Trace{ID: "r1", Producer: "session", Labels: trace.Labels{"repo": {"o/svc"}}, Output: json.RawMessage(`"x"`)}
	withdrawn := knowledge.Knowledge{
		ID: "k1", Version: 2, Kind: knowledge.KindJudgment, Status: knowledge.StatusRetired, Approver: knowledge.ApproverNodloop, Reason: knowledge.ReasonWords,
		Content: "Quote paths", Run: &knowledge.RunScope{Producer: "session"},
	}
	cuedItem := knowledge.Knowledge{
		ID: "k2", Version: 1, Kind: knowledge.KindJudgment, Status: knowledge.StatusApproved, Approver: "ann",
		Content: "One line titles", Run: &knowledge.RunScope{Producer: "session"},
		Cue: &knowledge.Cue{Tool: "Bash", Command: "git commit"},
	}
	head := "# Run r1 of session\n\nLabels: runs of session. repo=o/svc\n\n## Output\n\nx\n\n## Verdict\n\n"
	tcs := []struct {
		name string
		args extract.Reaction
		want string
	}{
		{"a run that reaches nothing says none under each list", extract.Reaction{Run: run, Verdict: feedback.Feedback{Verdict: feedback.VerdictReject}},
			head + "reject\n\n## Calls\n\nThe turn recorded no call\n\n## Approved items this run reaches\n\nnone\n\n" +
				"## Waiting candidates this run reaches\n\nnone\n\n## Retired versions this run reaches\n\nnone\n"},
		{
			"a retired version names who retired it and why",
			extract.Reaction{
				Run: run, Verdict: feedback.Feedback{Verdict: feedback.VerdictReject, ReasonCode: feedback.ReasonFact, Reason: "wrong"},
				Retired: knowledge.Set{withdrawn},
			},
			head + "reject, fact: wrong\n\n## Calls\n\nThe turn recorded no call\n\n" +
				"## Approved items this run reaches\n\nnone\n\n" +
				"## Waiting candidates this run reaches\n\nnone\n\n" +
				"## Retired versions this run reaches\n\n[k1 v2 judgment retired by nodloop for words] Quote paths\nScope: runs of session\n",
		},
		{
			"the calls of the turn and the cue of an item are listed",
			extract.Reaction{
				Run: run, Verdict: feedback.Feedback{Verdict: feedback.VerdictReject},
				Calls:   call.Shapes{{Tool: "Bash", Command: "git commit"}, {Tool: "Edit", File: "*_test.go"}},
				Reached: knowledge.Set{cuedItem},
			},
			head + "reject\n\n## Calls\n\nThe calls of the turn: tool Bash command git commit; tool Edit file *_test.go\n\n" +
				"## Approved items this run reaches\n\n[k2 v1 judgment approved by ann] One line titles\nScope: runs of session\n" +
				"Cue: before each Bash call running git commit\n\n## Waiting candidates this run reaches\n\nnone\n\n" +
				"## Retired versions this run reaches\n\nnone\n",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.String())
		})
	}
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
		// Prompts limited to the correction and reached versions with their decision provenance
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
		// The model the trace names and empty when no call answered
		model    string
		proposed bool
		prompts  prompts
	}
	const haiku = "claude-haiku-4-5"
	// A draft the critic passes whose kind the ledger refuses
	unknownKind := `{"relation":"add","kind":"rule","content":"Run git with -C <dir> instead of changing into it","keys":["repo"]}`
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
				strings.Contains(req.Prompt, "## Approved items this run reaches\n\n[git-c v1 judgment approved by ann]") &&
				strings.Contains(req.Prompt, "## Waiting candidates this run reaches\n\n[quote v1 judgment candidate]") &&
				strings.Contains(req.Prompt, "## Retired versions this run reaches\n\n[dropped v1 meaning retired by ann for person] "+bullets) &&
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
			want{nil, extract.ConclusionProposed, []extract.Refusal{""}, usage(2, 0.5), haiku, true, drafted(1)}},
		{"a duplicate of a waiting candidate corroborates it", args{replies(duplicate, passed)},
			want{nil, extract.ConclusionCorroborated, []extract.Refusal{""}, usage(2, 0.5), haiku, true, drafted(1)}},
		{"a draft code refuses is sent back once", args{replies(twoSentences, good, passed)},
			want{nil, extract.ConclusionProposed, []extract.Refusal{extract.RefusalCode, ""}, usage(3, 0.75), haiku, true, drafted(2)}},
		{"a draft the critic refuses is sent back once", args{replies(good, refused, good, passed)},
			want{nil, extract.ConclusionProposed, []extract.Refusal{extract.RefusalCritic, ""}, usage(4, 1), haiku, true, drafted(2)}},
		{"a second critic refusal is returned", args{replies(good, refused, good, refused)},
			want{extract.ErrCriticRefused, extract.ConclusionRefused, []extract.Refusal{extract.RefusalCritic, extract.RefusalCritic}, usage(4, 1), haiku, false,
				drafted(2)}},
		{"a second code refusal is returned", args{replies(twoSentences, twoSentences)},
			want{extract.ErrNotLesson, extract.ConclusionRefused, []extract.Refusal{extract.RefusalCode, extract.RefusalCode}, usage(2, 0.5), haiku, false,
				drafted(2)}},
		{"a draft the ledger refuses is returned at once", args{replies(unknownKind, passed)},
			want{knowledge.ErrKindUnknown, extract.ConclusionRefused, []extract.Refusal{extract.RefusalLedger}, usage(2, 0.5), haiku, false, drafted(1)}},
		{"a draft that does not decode fails", args{replies(`not json`)},
			want{extract.ErrDraftInvalid, extract.ConclusionFailed, []extract.Refusal{extract.RefusalModel}, usage(1, 0.25), haiku, false, drafted(1)}},
		{"a failed draft call keeps the cost it was charged and names no model", args{[]reply{{err: maxTurns}}},
			want{maxTurns, extract.ConclusionFailed, []extract.Refusal{extract.RefusalModel}, usage(0, 0.5), "", false, drafted(1)}},
		{"a failed resent draft call ends the extraction", args{append(replies(twoSentences), reply{err: maxTurns})},
			want{maxTurns, extract.ConclusionFailed, []extract.Refusal{extract.RefusalCode, extract.RefusalModel}, usage(1, 0.75), haiku, false, drafted(2)}},
		{"a failed critic call keeps the cost it was charged", args{append(replies(good), reply{err: maxTurns})},
			want{maxTurns, extract.ConclusionFailed, []extract.Refusal{extract.RefusalModel}, usage(1, 0.75), haiku, false, drafted(1)}},
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
			assert.Equal(t, make([]string, len(rec.Attempts)), classifiesOf(rec), "the claude critic records no classify trace")
			assert.Equal(t, tc.want.proposed, got.Candidate != nil)
			assert.Equal(t, tc.want.prompts, promptsOf(reqs))
			assert.Equal(t, record.Usage(tc.want.usage), tr.Usage)
			assert.Equal(t, tc.want.model, tr.Model, "the trace names the model that answered")
			assert.Positive(t, tr.DurationMS)
			assert.Zero(t, tr.DurationMS%testkit.ClockStep.Milliseconds(), "the duration comes from the testkit clock")
			assert.Equal(t, llm.Response{}, meter.Take(), "the extraction took the meter")
		})
	}
}

// A run whose reaction does not resolve calls no model and records no extract trace
func TestExtractorExtractUnresolved(t *testing.T) {
	type args struct {
		// The trace extracted and the corrected run when empty
		run string
		// Verdicts of ann recorded on the corrected run after its edit
		later []feedback.Verdict
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a run approved after its correction", args{later: []feedback.Verdict{feedback.VerdictApprove}}, extract.ErrNotCorrected},
		{"an unknown run", args{run: "t-unknown"}, record.ErrTraceNotFound},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, e, runID := corrected(t)
			for _, v := range tc.args.later {
				fb, err := feedback.New(runID, v, "", "", nil, "ann", s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Feedback.Append(ctx, fb.Record()))
			}
			// No call is expected so any model call fails the test
			meter := llm.NewMeter(llmmock.NewMockClient(gomock.NewController(t)))

			_, err := e.Extract(ctx, extract.NewClaudeDrafter(meter, ""), extract.NewClaudeCritic(meter, ""), cmp.Or(tc.args.run, runID), "")

			assert.ErrorIs(t, err, tc.want)
			traces, err := s.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameExtract)})
			require.NoError(t, err)
			assert.Empty(t, traces)
		})
	}
}

// The one extract trace of the run with its path checked
// It names the verdict it read by the record_id every transport sends the verdict under
func recorded(t *testing.T, s testkit.Stores, path extract.Path, runID string) (extract.Record, record.Trace) {
	t.Helper()
	ctx := context.Background()
	traces, err := s.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameExtract)})
	require.NoError(t, err)
	require.Len(t, traces, 1)
	assert.Equal(t, string(path), traces[0].Subject)
	assert.Equal(t, runID, traces[0].Ref)
	require.NoError(t, schema.Validate(string(trace.NameExtract), traces[0]))
	var in extract.TraceInput
	require.NoError(t, json.Unmarshal(traces[0].Input, &in))
	verdicts, err := s.Feedback.List(ctx, record.FeedbackFilter{TraceID: runID})
	require.NoError(t, err)
	require.Len(t, verdicts, 1)
	ix, err := schema.Published.Index()
	require.NoError(t, err)
	kind, err := ix.Kind("verdict")
	require.NoError(t, err)
	verdict, err := kind.Record(verdicts[0])
	require.NoError(t, err)
	assert.Equal(t, verdict.ID, in.VerdictID, "the trace names the verdict by its record_id")
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

// The classify trace each attempt names in order and empty for one that asked no plan
func classifiesOf(rec extract.Record) []string {
	out := make([]string, 0, len(rec.Attempts))
	for _, a := range rec.Attempts {
		out = append(out, a.Classify)
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
		// The candidate record names this extract trace as the one that proposed it
		named bool
	}
	gitC := &extract.Related{ID: "git-c", Version: 1, Status: knowledge.StatusApproved}
	quote := &extract.Related{ID: "quote", Version: 1, Status: knowledge.StatusCandidate}
	dropped := &extract.Related{ID: "dropped", Version: 1, Status: knowledge.StatusRetired}
	dupQuote := extract.Draft{Relation: extract.RelationDuplicate, RelatesTo: "quote", Kind: knowledge.KindJudgment, Content: quoted}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an add is proposed", args{draft: extract.Draft{Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: lesson, Keys: []string{"repo"}}, critique: pass},
			want{conclusion: extract.ConclusionProposed, candidate: &knowledge.Ref{ID: "k-generated", Version: 1}, named: true}},
		{"a duplicate is recorded as one", args{draft: extract.Draft{Relation: extract.RelationDuplicate, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson}, critique: pass},
			want{conclusion: extract.ConclusionDuplicate, related: gitC}},
		{"a duplicate of a candidate is recorded as corroborated and keeps the extraction that proposed it", args{draft: dupQuote, critique: pass},
			want{conclusion: extract.ConclusionCorroborated, candidate: &knowledge.Ref{ID: "quote", Version: 1}, related: quote}},
		{"a duplicate of a retired version is recorded as proposed again", args{draft: extract.Draft{Relation: extract.RelationDuplicate, RelatesTo: "dropped", Kind: knowledge.KindMeaning, Content: bullets}, critique: pass},
			want{conclusion: extract.ConclusionProposed, candidate: &knowledge.Ref{ID: "dropped", Version: 2}, related: dropped, named: true}},
		{"a conflict is recorded as one with the next version", args{draft: extract.Draft{Relation: extract.RelationConflict, RelatesTo: "git-c", Kind: knowledge.KindJudgment, Content: lesson}, critique: pass},
			want{conclusion: extract.ConclusionConflict, candidate: &knowledge.Ref{ID: "git-c", Version: 2}, related: gitC, named: true}},
		{"a conflict with a candidate names it", args{draft: extract.Draft{Relation: extract.RelationConflict, RelatesTo: "quote", Kind: knowledge.KindJudgment, Content: lesson}, critique: pass},
			want{conclusion: extract.ConclusionConflict, candidate: &knowledge.Ref{ID: "quote", Version: 2}, related: quote, named: true}},
		{"a candidate retired since the reaction is a ledger refusal", args{draft: dupQuote, critique: pass, retired: []string{"quote"}},
			want{conclusion: extract.ConclusionRefused, refusal: extract.RefusalLedger}},
		{"a kind outside the set is a ledger refusal", args{draft: extract.Draft{Relation: extract.RelationAdd, Kind: "rule", Content: lesson}, critique: pass},
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

			got, _ := e.Propose(ctx, r, tc.args.draft, tc.args.critique, "")

			rec, tr := recorded(t, s, extract.PathConversation, runID)
			assert.Equal(t, record.Usage{}, tr.Usage, "the conversation called no model here")
			require.Len(t, rec.Attempts, 1)
			assert.Equal(t, tc.want.conclusion, rec.Conclusion)
			assert.Equal(t, tc.want.refusal, rec.Attempts[0].Refusal)
			assert.Equal(t, tc.want.questions, rec.Attempts[0].Questions)
			assert.Empty(t, rec.Attempts[0].Classify, "the conversation answers its own critic")
			assert.Equal(t, tc.want.candidate, rec.Candidate)
			assert.Equal(t, tc.want.related, rec.Related)
			assert.Equal(t, tc.want.named, got.Candidate != nil && got.Candidate.Extract == tr.ID)
		})
	}
}

// The excerpt a draft quotes reaches the record only when it quotes the output or the edit within 200 runes
// One that quotes neither leaves the record without one and the candidate is still proposed
func TestProposeExcerpt(t *testing.T) {
	long := strings.Repeat("the cache key misses the tenant ", 8)
	multibyte := strings.Repeat("🙂", 200)
	type args struct {
		excerpt string
		// The output of a run of its own and the fixture run when empty
		output string
	}
	type want struct {
		excerpt  string
		proposed bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a part of the output", args{"cd repo && git status", ""}, want{"cd repo && git status", true}},
		{"a part of the edited output", args{"git -C repo status", ""}, want{"git -C repo status", true}},
		{"a part across a line break with its white space folded", args{"git  status\nI changed into", ""},
			want{"git status I changed into", true}},
		{"a quote the texts lack", args{"rm -rf repo", ""}, want{"", true}},
		{"a part of the output over 200 runes", args{long, long}, want{"", true}},
		{"a part of the output at 200 runes", args{long[:200], long}, want{strings.TrimSpace(long[:200]), true}},
		{"a multibyte excerpt at 200 runes", args{multibyte, multibyte}, want{multibyte, true}},
		{"a multibyte excerpt over 200 runes", args{multibyte + "🙂", multibyte + "🙂"}, want{"", true}},
		{"no excerpt", args{"", ""}, want{"", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, e, runID := corrected(t)
			if tc.args.output != "" {
				runID = rejectedRun(t, s, tc.args.output)
			}
			r, err := e.Reaction(ctx, runID)
			require.NoError(t, err)
			draft := extract.Draft{
				Relation: extract.RelationAdd, Kind: knowledge.KindJudgment,
				Content: "Run git with -C <dir> instead of changing into it",
				Excerpt: tc.args.excerpt,
			}

			got, err := e.Propose(ctx, r, draft, pass, "")

			require.NoError(t, err)
			rec, _ := recorded(t, s, extract.PathConversation, runID)
			assert.Equal(t, tc.want, want{rec.Excerpt, got.Candidate != nil})
			assert.Equal(t, tc.args.excerpt, rec.Attempts[0].Draft.Excerpt, "the attempt keeps the draft as written")
		})
	}
}

func TestProposeDuplicateExcerpt(t *testing.T) {
	type want struct {
		excerpt   string
		candidate bool
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"an approved duplicate carries no candidate excerpt", "git-c", want{}},
		{"a waiting duplicate keeps the quoted correction", "quote", want{"cd repo && git status", true}},
		{"a retired duplicate keeps the quoted correction", "dropped", want{"cd repo && git status", true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, e, runID := corrected(t)
			r, err := e.Reaction(ctx, runID)
			require.NoError(t, err)
			related := slices.Concat(r.Reached, r.Waiting, r.Retired)
			item, ok := related.Find(tc.args)
			require.True(t, ok)
			draft := extract.Draft{
				Relation: extract.RelationDuplicate, RelatesTo: item.ID, Kind: item.Kind,
				Content: item.Content, Excerpt: "cd repo && git status",
			}

			got, err := e.Propose(ctx, r, draft, pass, "")

			require.NoError(t, err)
			rec, _ := recorded(t, s, extract.PathConversation, runID)
			assert.Equal(t, tc.want, want{rec.Excerpt, got.Candidate != nil})
		})
	}
}

// A run of the repo with the output that a person rejected
func rejectedRun(t *testing.T, s testkit.Stores, output string) string {
	t.Helper()
	ctx := context.Background()
	out, err := json.Marshal(output)
	require.NoError(t, err)
	run, err := trace.NewRun("session", "", trace.Labels{"repo": {"nodloop"}}, nil, out, s.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, s.Traces.Append(ctx, run.Record()))
	fb, err := feedback.New(run.ID, feedback.VerdictReject, "", "the key misses the tenant", nil, "ann", s.Clock.Now())
	require.NoError(t, err)
	require.NoError(t, s.Feedback.Append(ctx, fb.Record()))
	return run.ID
}

// Each attempt the plan of the critic point judged names the classify trace that holds its answers
// An attempt code refused before any critic call names none
func TestExtractorExtractPlanCritic(t *testing.T) {
	good := `{"relation":"add","kind":"judgment","content":"Run git with -C <dir> instead of changing into it","keys":["repo"]}`
	twoSentences := `{"relation":"add","kind":"judgment","content":"Use git -C. Never cd first"}`
	type args struct {
		drafts []string
		// The status the endpoint answers and a sure pass when zero
		status int
		// What the built in member fails with when the endpoint fails
		builtin error
	}
	type want struct {
		err        error
		conclusion extract.Conclusion
		// Per attempt whether it names a classify trace
		named []bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a sure endpoint judges the draft", args{drafts: []string{good}}, want{conclusion: extract.ConclusionProposed, named: []bool{true}}},
		{"a draft code refused names no trace and the one resent names its own", args{drafts: []string{twoSentences, good}},
			want{conclusion: extract.ConclusionProposed, named: []bool{false, true}}},
		{"a critic call that failed names the trace that keeps the failure", args{drafts: []string{good}, status: http.StatusBadGateway, builtin: assert.AnError},
			want{err: assert.AnError, conclusion: extract.ConclusionFailed, named: []bool{true}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(cmp.Or(tc.args.status, http.StatusOK))
				_, _ = w.Write([]byte(`{"answers":{"states":{"noul":0.95},"holds":{"noul":0.9},"fits":{"noul":0.92}}}`))
			}))
			t.Cleanup(srv.Close)
			ctx := context.Background()
			s, e, runID := corrected(t)
			drafts := tc.args.drafts
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(len(drafts)).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
				next := drafts[0]
				drafts = drafts[1:]
				return llm.Response{Output: json.RawMessage(next)}, nil
			})
			builtin := classifymock.NewMockClassifier(gomock.NewController(t))
			if tc.args.status != 0 {
				builtin.EXPECT().Classify(gomock.Any(), gomock.Any()).Times(1).Return(nil, tc.args.builtin)
			}
			critic := classify.NewPlan(classify.PointCritic, classify.Member{Name: "laya", Classifier: classify.NewHTTP(classify.Endpoint{URL: srv.URL}, "", time.Second)},
				classify.Member{Name: "claude", Classifier: builtin}, s.Traces, func(text string) string { return text }, s.Clock.Now)

			_, err := e.Extract(ctx, extract.NewClaudeDrafter(llm.NewMeter(client), ""), critic, runID, "")

			assert.ErrorIs(t, err, tc.want.err)
			rec, _ := recorded(t, s, extract.PathModel, runID)
			assert.Equal(t, tc.want.conclusion, rec.Conclusion)
			classified, err := s.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameClassify)})
			require.NoError(t, err)
			named, ids := classifiesOf(rec), oldestFirst(classified)
			assert.Equal(t, tc.want.named, namesOf(named))
			assert.Equal(t, ids, slices.DeleteFunc(named, func(id string) bool { return id == "" }), "the attempts name the classify traces in their order")
		})
	}
}

func TestExtractorTriage(t *testing.T) {
	type args struct {
		triage string
		bare   bool
	}
	type want struct {
		triage classify.Triage
		// The triage names the classify trace of the attempt
		named      bool
		classified int
	}
	valid := `,"triage":{"nature":"preference","mixed":false,"reversibility":"H","protected":"none",` +
		`"lasting":true,"faithful":true,"conflict":false}`
	preference := classify.Triage{
		Policy: "1", Source: "missing", Nature: "preference", Reversibility: "H", Protected: "none", Lasting: true, Faithful: true,
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"typed metadata follows the deciding critic", args{triage: valid}, want{preference, true, 1}},
		{"a bare critic uses the existing extraction provenance", args{triage: valid, bare: true}, want{preference, false, 0}},
		{"legacy metadata does not call the fallback", args{}, want{classified: 1}},
		{"invalid metadata does not call the fallback", args{triage: `,"triage":{"nature":"preference"}`}, want{classified: 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, e, runID := corrected(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(1).Return(llm.Response{Output: json.RawMessage(
				`{"relation":"add","kind":"judgment","content":"Run git with -C <dir> instead of changing into it","keys":["repo"]}`)}, nil)
			criticClient := llmmock.NewMockClient(gomock.NewController(t))
			criticClient.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(1).Return(llm.Response{Output: json.RawMessage(
				`{"states":true,"holds":true,"fits":true,"why":"ok"` + tc.args.triage + `}`)}, nil)
			fallback := classifymock.NewMockClassifier(gomock.NewController(t))
			var critic extract.Classifier = classify.NewPlan(classify.PointCritic,
				classify.Member{Name: "critic", Classifier: extract.NewClaudeCritic(criticClient, "")},
				classify.Member{Name: "fallback", Classifier: fallback}, s.Traces, func(text string) string { return text }, s.Clock.Now)
			if tc.args.bare {
				critic = extract.NewClaudeCritic(criticClient, "")
			}

			got, err := e.Extract(ctx, extract.NewClaudeDrafter(llm.NewMeter(client), ""), critic, runID, "")

			require.NoError(t, err)
			rec, _ := recorded(t, s, extract.PathModel, runID)
			require.Equal(t, extract.ConclusionProposed, rec.Conclusion)
			require.NotNil(t, got.Candidate)
			assert.Equal(t, &knowledge.Ref{ID: got.Candidate.ID, Version: got.Candidate.Version}, rec.Candidate)
			require.Len(t, rec.Attempts, 1)
			assert.Equal(t, got.Candidate.Content, rec.Attempts[0].Draft.Content)
			classified, err := s.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameClassify)})
			require.NoError(t, err)
			var triage classify.Triage
			if rec.Triage != nil {
				triage = *rec.Triage
			}
			named := triage.Classify != "" && triage.Classify == rec.Attempts[0].Classify
			triage.Classify = ""
			assert.Equal(t, tc.want, want{triage, named, len(classified)})
		})
	}
}

// Whether each id names a trace
func namesOf(ids []string) []bool {
	out := make([]bool, 0, len(ids))
	for _, id := range ids {
		out = append(out, id != "")
	}
	return out
}

// The ids of traces a store listed newest first in the order they were appended
func oldestFirst(traces []record.Trace) []string {
	out := make([]string, 0, len(traces))
	for _, tr := range slices.Backward(traces) {
		out = append(out, tr.ID)
	}
	return out
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
