package extract_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A session in the order Claude Code records it
// The earlier turn pushed and the corrected turn committed before its answer
// Then a stop hook continued it and the continuation wrote a test
// A later turn opened a pull request
// The corrected run is the answer of the middle turn and the approved item k1 of repo acme carries the cue given
func cuedTurn(t *testing.T, itemCue *knowledge.Cue) (testkit.Stores, *extract.Extractor, string) {
	t.Helper()
	ctx := context.Background()
	s := testkit.Open(t)
	start := s.Clock.Now()
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	labels := trace.Labels{"repo": {"acme"}, "host": {"claude-code"}}
	appendTrace := func(tr trace.Trace) {
		tr.SessionID = "s1"
		require.NoError(t, s.Traces.Append(ctx, tr.Record()))
	}
	appendTurn := func(seconds int) {
		appendTrace(
			trace.Trace{
				ID:    trace.NewID(at(seconds)),
				Name:  trace.NameTurn,
				Time:  at(seconds),
				Input: json.RawMessage(`{}`),
			},
		)
	}
	appendRun := func(seconds, from int, continued bool) string {
		in, err := knowledge.NewRunInput(nil, nil, "")
		require.NoError(t, err)
		in.Continued = continued
		input, err := json.Marshal(in)
		require.NoError(t, err)
		run, err := trace.NewRun("session", "", labels, input, json.RawMessage(`"Committed the fix"`), at(seconds))
		require.NoError(t, err)
		run, err = run.Took(at(seconds).Sub(at(from)))
		require.NoError(t, err)
		appendTrace(run)
		return run.ID
	}
	appendCalls := func(seconds, turn int, shapes call.Shapes) {
		input, err := json.Marshal(call.Input{At: at(seconds), Turn: at(turn), Calls: shapes})
		require.NoError(t, err)
		appendTrace(trace.Trace{ID: trace.NewID(at(seconds)), Name: trace.NameCalls, Time: at(seconds), Input: input})
	}
	appendTurn(0)
	appendCalls(1, 0, call.Shapes{{Tool: "Bash", Command: "git push"}})
	appendRun(2, 0, false)
	appendTurn(3)
	appendCalls(5, 3, call.Shapes{{Tool: "Bash", Command: "git commit"}})
	runID := appendRun(8, 3, false)
	appendCalls(9, 3, call.Shapes{{Tool: "Edit", File: "*_test.go"}})
	appendRun(10, 8, true)
	appendTurn(15)
	appendCalls(20, 15, call.Shapes{{Tool: "Bash", Command: "gh pr create"}})
	fb, err := feedback.New(
		runID,
		feedback.VerdictReject,
		feedback.ReasonForm,
		"the title runs past one line",
		nil,
		"ann",
		at(30),
	)
	require.NoError(t, err)
	require.NoError(t, s.Feedback.Append(ctx, fb.Record()))
	_, _, err = s.Ledger.Propose(ctx, knowledge.Knowledge{
		ID: "k1", Kind: knowledge.KindJudgment, Content: "Keep a commit title short", Author: "author", Cue: itemCue,
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"r-earlier"}},
	})
	require.NoError(t, err)
	require.NoError(t, testkit.Err(s.Ledger.Approve(ctx, "k1", 1, "ann")))
	return s, extract.New(s.Ledger, s.Traces, s.Feedback, s.Clock.Now), runID
}

// The reaction lists the calls of the turn of the corrected answer with those its continuation made after it
// The calls of the turns around it stay out
func TestReactionCalls(t *testing.T) {
	_, e, runID := cuedTurn(t, nil)
	r, err := e.Reaction(context.Background(), runID)
	require.NoError(t, err)
	assert.Equal(t, call.Shapes{{Tool: "Bash", Command: "git commit"}, {Tool: "Edit", File: "*_test.go"}}, r.Calls)
}

func TestProposeCue(t *testing.T) {
	commit := &knowledge.Cue{Tool: "Bash", Command: "git commit"}
	test := &knowledge.Cue{Tool: "Edit", File: "*_test.go"}
	type args struct {
		itemCue  *knowledge.Cue
		relation extract.Relation
		kind     knowledge.Kind
		cue      *knowledge.Cue
	}
	type want struct {
		cue *knowledge.Cue
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"an add takes a cue that fits a call of the turn",
			args{nil, extract.RelationAdd, knowledge.KindJudgment, commit},
			want{cue: commit},
		},
		{
			"a cue of a continued run of the turn fits",
			args{nil, extract.RelationAdd, knowledge.KindJudgment, test},
			want{cue: test},
		},
		{"an add without a cue proposes none", args{nil, extract.RelationAdd, knowledge.KindJudgment, nil}, want{}},
		{
			"a cue of an earlier turn is refused",
			args{nil, extract.RelationAdd, knowledge.KindJudgment, &knowledge.Cue{Tool: "Bash", Command: "git push"}},
			want{err: extract.ErrCueUnseen},
		},
		{
			"a cue of a later turn is refused",
			args{nil, extract.RelationAdd, knowledge.KindJudgment, &knowledge.Cue{Tool: "Bash", Command: "gh pr"}},
			want{err: extract.ErrCueUnseen},
		},
		{
			"a cue on a meaning is refused",
			args{nil, extract.RelationAdd, knowledge.KindMeaning, commit},
			want{err: extract.ErrCueUnseen},
		},
		{
			"a cue past the grammar is refused",
			args{
				nil,
				extract.RelationAdd,
				knowledge.KindJudgment,
				&knowledge.Cue{Tool: "Bash", Command: "git commit -m"},
			},
			want{err: extract.ErrCueUnseen},
		},
		{
			"an update keeps the cue of the item it names",
			args{commit, extract.RelationUpdate, knowledge.KindJudgment, test},
			want{cue: commit},
		},
		{
			"an update of an uncued item takes the draft cue",
			args{nil, extract.RelationUpdate, knowledge.KindJudgment, commit},
			want{cue: commit},
		},
		{
			"a conflict keeps the cue of the item it names",
			args{commit, extract.RelationConflict, knowledge.KindJudgment, nil},
			want{cue: commit},
		},
		{
			"a conflict drops a draft cue unchecked",
			args{
				commit,
				extract.RelationConflict,
				knowledge.KindJudgment,
				&knowledge.Cue{Tool: "Bash", Command: "git push"},
			},
			want{cue: commit},
		},
		{
			"an update of a cued item ignores a draft cue that fits no call",
			args{
				commit,
				extract.RelationUpdate,
				knowledge.KindJudgment,
				&knowledge.Cue{Tool: "Bash", Command: "git push"},
			},
			want{cue: commit},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			_, e, runID := cuedTurn(t, tc.args.itemCue)
			r, err := e.Reaction(ctx, runID)
			require.NoError(t, err)
			draft := extract.Draft{
				Relation: tc.args.relation,
				Kind:     tc.args.kind,
				Content:  "Keep a commit title to one line",
				Cue:      tc.args.cue,
			}
			if tc.args.relation != extract.RelationAdd {
				draft.RelatesTo = "k1"
			}

			got, err := e.Propose(ctx, r, draft, pass, "")

			assert.ErrorIs(t, err, tc.want.err)
			if tc.want.err != nil {
				return
			}
			require.NotNil(t, got.Candidate)
			assert.Equal(t, tc.want.cue, got.Candidate.Cue)
		})
	}
}

// A run of no session has no turn so a cue fits nothing
func TestProposeCueWithoutCalls(t *testing.T) {
	ctx := context.Background()
	s, e, _ := corrected(t)
	runID := rejectedRun(t, s, "Committed the fix")
	r, err := e.Reaction(ctx, runID)
	require.NoError(t, err)
	assert.Empty(t, r.Calls)
	draft := extract.Draft{
		Relation: extract.RelationAdd, Kind: knowledge.KindJudgment, Content: "Keep a commit title to one line",
		Cue: &knowledge.Cue{Tool: "Bash", Command: "git commit"},
	}
	_, err = e.Propose(ctx, r, draft, pass, "")
	assert.ErrorIs(t, err, extract.ErrCueUnseen)
}

// A call of a turn the person interrupted never joins the answer of the next turn
func TestReactionCallsInterrupted(t *testing.T) {
	ctx := context.Background()
	s := testkit.Open(t)
	start := s.Clock.Now()
	at := func(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }
	appendTrace := func(tr trace.Trace) {
		tr.SessionID = "s1"
		require.NoError(t, s.Traces.Append(ctx, tr.Record()))
	}
	labels := trace.Labels{"repo": {"acme"}}
	earlier, err := trace.NewRun("session", "", labels, nil, json.RawMessage(`"x"`), at(2))
	require.NoError(t, err)
	appendTrace(earlier)
	appendTrace(trace.Trace{ID: trace.NewID(at(3)), Name: trace.NameTurn, Time: at(3), Input: json.RawMessage(`{}`)})
	input, err := json.Marshal(call.Input{At: at(4), Calls: call.Shapes{{Tool: "Write", File: "*.md"}}})
	require.NoError(t, err)
	appendTrace(trace.Trace{ID: trace.NewID(at(4)), Name: trace.NameCalls, Time: at(4), Input: input})
	appendTrace(trace.Trace{ID: trace.NewID(at(6)), Name: trace.NameTurn, Time: at(6), Input: json.RawMessage(`{}`)})
	answer, err := trace.NewRun("session", "", labels, nil, json.RawMessage(`"x"`), at(10))
	require.NoError(t, err)
	appendTrace(answer)
	fb, err := feedback.New(answer.ID, feedback.VerdictReject, "", "wrong", nil, "ann", at(11))
	require.NoError(t, err)
	require.NoError(t, s.Feedback.Append(ctx, fb.Record()))
	r, err := extract.New(s.Ledger, s.Traces, s.Feedback, s.Clock.Now).Reaction(ctx, answer.ID)
	require.NoError(t, err)
	assert.Empty(t, r.Calls)
}

// An update keeps the kind of the version it names so a cue on an update of a meaning is a refusal the drafter can fix
func TestProposeCueOnAnUpdateOfAMeaning(t *testing.T) {
	ctx := context.Background()
	s, e, runID := cuedTurn(t, nil)
	_, _, err := s.Ledger.Propose(ctx, knowledge.Knowledge{
		ID: "m1", Kind: knowledge.KindMeaning, Content: "A title is the first line of a commit", Author: "author",
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"acme"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"r-earlier"}},
	})
	require.NoError(t, err)
	require.NoError(t, testkit.Err(s.Ledger.Approve(ctx, "m1", 1, "ann")))
	r, err := e.Reaction(ctx, runID)
	require.NoError(t, err)
	draft := extract.Draft{
		Relation: extract.RelationUpdate, RelatesTo: "m1", Kind: knowledge.KindJudgment,
		Content: "A title is the first line of a commit message",
		Cue:     &knowledge.Cue{Tool: "Bash", Command: "git commit"},
	}
	_, err = e.Propose(ctx, r, draft, pass, "")
	assert.ErrorIs(t, err, extract.ErrCueUnseen)
}

// A corrupt line older than the corrected run leaves the calls it could read and never stops the extraction
func TestReactionCallsCorruptLine(t *testing.T) {
	s, e, runID := cuedTurn(t, nil)
	path := filepath.Join(s.Dir, "traces.jsonl")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append([]byte("{broken\n"), content...), 0o600))
	r, err := e.Reaction(context.Background(), runID)
	require.NoError(t, err)
	assert.Equal(t, call.Shapes{{Tool: "Bash", Command: "git commit"}, {Tool: "Edit", File: "*_test.go"}}, r.Calls)
}
