package loop_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestRunsReport(t *testing.T) {
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	run := func(id string, applied ...knowledge.Ref) trace.Trace {
		in, _ := json.Marshal(map[string][]knowledge.Ref{"applied": applied})
		return trace.Trace{ID: id, Name: trace.NameRun, Producer: "session", Input: in}
	}
	v1 := knowledge.Ref{ID: "git-c", Version: 1}
	verdict := func(id string, v feedback.Verdict, code feedback.ReasonCode, after time.Duration) feedback.Feedback {
		return feedback.Feedback{TraceID: id, Verdict: v, ReasonCode: code, Time: at.Add(after), Reviewer: feedback.ReviewerAuthor}
	}
	inferred := func(id string, v feedback.Verdict, code feedback.ReasonCode, after time.Duration) feedback.Feedback {
		fb := verdict(id, v, code, after)
		fb.Reviewer = feedback.ReviewerSession
		return fb
	}
	item := func() knowledge.Set {
		return knowledge.Set{{
			ID: "git-c", Version: 1, Kind: knowledge.KindJudgment, Content: "use git -C", Status: knowledge.StatusApproved,
			Run: &knowledge.RunScope{Producer: "session"}, Approver: "ann", ApprovedAt: at.Add(2 * time.Hour),
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"taught"}},
		}, {
			ID: "data", Version: 1, Kind: knowledge.KindMeaning, Content: "lag", Status: knowledge.StatusApproved, Approver: "ann",
		}}
	}
	traces := trace.Traces{
		run("taught"), run("a1", v1), run("a2", v1), run("a3", v1), run("a4", v1), run("a5"),
		{ID: "failed", Name: trace.NameRun, Error: "cancelled", Input: json.RawMessage(`{"applied":[{"id":"git-c","version":1}]}`)},
	}
	type args struct {
		verdicts feedback.Records
		items    knowledge.Set
	}
	tcs := []struct {
		name string
		args args
		want []loop.RunItem
	}{
		{
			"followed, repeat on the taught code and settle from the correction",
			args{feedback.Records{
				verdict("taught", feedback.VerdictEdit, feedback.ReasonOther, 0),
				verdict("a1", feedback.VerdictApprove, "", 3*time.Hour),
				verdict("a2", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
				verdict("a3", feedback.VerdictEdit, feedback.ReasonApproach, 3*time.Hour),
				verdict("a5", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
			}, item()},
			[]loop.RunItem{{ID: "git-c", Version: 1, Applied: 4, Judged: 3, Followed: 1, Repeat: 1, Settle: 2 * time.Hour}},
		},
		{
			"a later approve replaces a correction and a session verdict counts apart",
			args{feedback.Records{
				verdict("a1", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
				verdict("a1", feedback.VerdictApprove, "", 4*time.Hour),
				inferred("a2", feedback.VerdictApprove, "", 3*time.Hour),
			}, item()},
			[]loop.RunItem{{ID: "git-c", Version: 1, Applied: 4, Judged: 1, Followed: 1, InferredJudged: 1, InferredFollowed: 1}},
		},
		{
			"a person's verdict wins over a newer inferred one and an inferred correction teaches the code and the settle",
			args{feedback.Records{
				inferred("taught", feedback.VerdictReject, feedback.ReasonForm, time.Hour),
				verdict("a1", feedback.VerdictApprove, "", 3*time.Hour),
				inferred("a1", feedback.VerdictReject, feedback.ReasonForm, 4*time.Hour),
				inferred("a2", feedback.VerdictReject, feedback.ReasonForm, 3*time.Hour),
				inferred("a3", feedback.VerdictReject, feedback.ReasonFact, 3*time.Hour),
			}, item()},
			[]loop.RunItem{{ID: "git-c", Version: 1, Applied: 4, Judged: 1, Followed: 1, InferredJudged: 2, InferredRepeat: 1, Settle: time.Hour}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, loop.NewRuns(traces, tc.args.verdicts).Report(tc.args.items))
		})
	}
}

func TestRunsTotals(t *testing.T) {
	run := func(id string) trace.Trace {
		return trace.Trace{ID: id, Name: trace.NameRun, Producer: "session", Input: json.RawMessage(`{}`)}
	}
	traces := trace.Traces{run("r1"), run("r2"), run("r3"), run("r4"), {ID: "failed", Name: trace.NameRun, Error: "cancelled", Input: json.RawMessage(`{}`)}}
	verdict := func(id string, v feedback.Verdict, reviewer string) feedback.Feedback {
		return feedback.Feedback{TraceID: id, Verdict: v, ReasonCode: feedback.ReasonOther, Reviewer: reviewer}
	}
	scope := &knowledge.RunScope{Producer: "session"}
	item := func(id string, status knowledge.Status, compaction string) knowledge.Knowledge {
		return knowledge.Knowledge{ID: id, Version: 1, Kind: knowledge.KindJudgment, Content: id, Status: status, Run: scope, Compaction: compaction}
	}
	verdicts := feedback.Records{
		verdict("r1", feedback.VerdictApprove, feedback.ReviewerAuthor),
		verdict("r2", feedback.VerdictReject, feedback.ReviewerSession),
		verdict("r2", feedback.VerdictEdit, feedback.ReviewerAuthor),
		verdict("r3", feedback.VerdictReject, feedback.ReviewerSession),
		verdict("failed", feedback.VerdictReject, feedback.ReviewerAuthor),
	}
	items := knowledge.Set{
		item("approved", knowledge.StatusApproved, ""), item("draft", knowledge.StatusCandidate, ""),
		item("compacted", knowledge.StatusCandidate, "c1"),
		{ID: "data", Version: 1, Kind: knowledge.KindMeaning, Content: "lag", Status: knowledge.StatusApproved},
	}

	got := loop.NewRuns(traces, verdicts).Totals(items)

	assert.Equal(t, loop.Totals{Runs: 4, Judged: 2, Inferred: 1, Corrected: 2, Waiting: 1, Approved: 1}, got)
}

func TestRunsExtractions(t *testing.T) {
	run := func(id, plugin string) trace.Trace {
		in, _ := json.Marshal(map[string]any{"applied": []knowledge.Ref{}, "plugin": plugin})
		return trace.Trace{ID: id, Name: trace.NameRun, Producer: "session", Input: in}
	}
	extracted := func(ref, path, output string) trace.Trace {
		return trace.Trace{ID: "x-" + ref + path, Name: trace.NameExtract, Subject: path, Ref: ref, Output: json.RawMessage(output)}
	}
	traces := trace.Traces{
		run("r1", "0.7.0"), run("r2", ""),
		extracted("r1", "model", `{"attempts":[{"refusal":"critic","questions":["holds","states"]},{}],"conclusion":"proposed"}`),
		extracted("r1", "conversation", `{"attempts":[{"refusal":"code"}],"conclusion":"refused"}`),
		extracted("r2", "model", `{"attempts":[{"refusal":"model"}],"conclusion":"failed"}`),
		extracted("r3", "model", `not json`),
	}

	got := loop.NewRuns(traces, nil).Extractions(traces)

	assert.Equal(t, []loop.ExtractRow{
		{Version: "0.7.0", Path: "conversation", Extractions: 1, Conclusions: map[string]int{"refused": 1},
			Refusals: map[string]int{"code": 1}, Questions: map[string]int{}},
		{Version: "0.7.0", Path: "model", Extractions: 1, Conclusions: map[string]int{"proposed": 1},
			Refusals: map[string]int{"critic": 1}, Questions: map[string]int{"holds": 1, "states": 1}},
		{Version: "unknown", Path: "model", Extractions: 1, Conclusions: map[string]int{"failed": 1},
			Refusals: map[string]int{"model": 1}, Questions: map[string]int{}},
	}, got)
}

func TestRunsDrafts(t *testing.T) {
	at := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	run := func(id, plugin string) trace.Trace {
		in, _ := json.Marshal(map[string]any{"applied": []knowledge.Ref{}, "plugin": plugin})
		return trace.Trace{ID: id, Name: trace.NameRun, Producer: "session", Input: in}
	}
	traces := trace.Traces{
		run("r1", "0.7.0"), run("r2", "0.7.0"), run("r3", ""),
		{ID: "x1", Name: trace.NameExtract, Subject: "conversation", Ref: "r1", Output: json.RawMessage(`{"conclusion":"proposed","candidate":{"id":"a","version":1}}`)},
		{ID: "x2", Name: trace.NameExtract, Subject: "conversation", Ref: "r2", Output: json.RawMessage(`{"conclusion":"proposed","candidate":{"id":"b","version":1}}`)},
	}
	record := func(id string, status knowledge.Status, after time.Duration, drafted bool, cites string) knowledge.Knowledge {
		return knowledge.Knowledge{ID: id, Version: 1, Status: status, Time: at.Add(after), Drafted: drafted,
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{cites}}}
	}
	// Newest first as the store lists them
	items := knowledge.Set{
		record("a", knowledge.StatusRetired, 5*time.Hour, true, "r1"),
		record("b", knowledge.StatusRetired, 3*time.Hour, true, "r2"),
		record("a", knowledge.StatusApproved, 2*time.Hour, true, "r1"),
		record("c", knowledge.StatusCandidate, time.Hour, true, "r3"),
		record("hand", knowledge.StatusCandidate, time.Hour, false, "r3"),
		record("b", knowledge.StatusCandidate, time.Hour, true, "r2"),
		record("a", knowledge.StatusCandidate, 0, true, "r1"),
	}

	got := loop.NewRuns(traces, nil).Drafts(items, traces)

	assert.Equal(t, []loop.DraftRow{
		{Version: "0.7.0", Path: "conversation", Drafted: 2, Approved: 1, Dropped: 1, Waiting: 0, Decide: 2 * time.Hour},
		{Version: "unknown", Path: "unknown", Drafted: 1, Waiting: 1},
	}, got)
}

func TestRunsCritics(t *testing.T) {
	extracted := func(id, ref, path, output string) trace.Trace {
		return trace.Trace{ID: id, Name: trace.NameExtract, Subject: path, Ref: ref, Output: json.RawMessage(output)}
	}
	critique := `"critique":{"states":true,"holds":true,"fits":true}`
	traces := trace.Traces{
		extracted("x1", "approved", "model", `{"attempts":[{`+critique+`,"refusal":"critic"},{`+critique+`}]}`),
		extracted("x2", "dropped", "model", `{"attempts":[{`+critique+`}]}`),
		extracted("x3", "open", "conversation", `{"attempts":[{`+critique+`}]}`),
		extracted("x4", "approved", "conversation", `{"attempts":[{"refusal":"code"}]}`),
		{ID: "c1", Name: trace.NameClassify, Subject: "critic", Ref: "dropped", Output: json.RawMessage(
			`{"members":[{"name":"laya","answers":{"holds":{"yes":0.2},"states":{"yes":0.9}}},{"name":"claude","error":"timeout"}]}`)},
	}
	cites := func(id string, status knowledge.Status, run string) knowledge.Knowledge {
		return knowledge.Knowledge{ID: id, Version: 1, Status: status, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run}}}
	}
	// Newest first as the store lists them
	items := knowledge.Set{
		cites("a", knowledge.StatusRetired, "approved"), cites("a", knowledge.StatusApproved, "approved"),
		cites("b", knowledge.StatusRetired, "dropped"), cites("c", knowledge.StatusCandidate, "open"),
		cites("a", knowledge.StatusCandidate, "approved"), cites("b", knowledge.StatusCandidate, "dropped"),
	}

	got := loop.NewRuns(nil, nil).Critics(items, traces)

	assert.Equal(t, []loop.CriticRow{
		{Critic: "classifier laya", Judged: 1, Agree: 1},
		{Critic: "extract conversation", Judged: 1, Open: 1},
		{Critic: "extract model", Judged: 3, Agree: 1, FalsePass: 1, FalseRefuse: 1},
	}, got)
}

func TestRunsScopes(t *testing.T) {
	run := func(id, session, repo, dir, plugin string, applied ...knowledge.Ref) trace.Trace {
		in, _ := json.Marshal(map[string]any{"applied": applied, "plugin": plugin})
		return trace.Trace{ID: id, Name: trace.NameRun, Producer: "session", SessionID: session, Input: in,
			Labels: trace.Labels{"repo": {repo}, "dir": {dir}}}
	}
	traces := trace.Traces{
		run("r1", "s1", "nodloop", ".", "0.7.0", knowledge.Ref{ID: "repo-wide", Version: 1}),
		run("r2", "s2", "nodloop", ".", "0.7.0"),
		run("r3", "s2", "nodloop", "/tmp/scratchpad", "0.7.0"),
	}
	item := func(id string, status knowledge.Status, labels trace.Labels) knowledge.Knowledge {
		return knowledge.Knowledge{ID: id, Version: 1, Kind: knowledge.KindJudgment, Content: id, Status: status,
			Run: &knowledge.RunScope{Producer: "session", Labels: labels}, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"r2"}}}
	}
	items := knowledge.Set{
		item("repo-wide", knowledge.StatusApproved, trace.Labels{"repo": {"nodloop"}}),
		item("scratch", knowledge.StatusCandidate, trace.Labels{"dir": {"/tmp/scratchpad"}}),
		item("unused", knowledge.StatusApproved, trace.Labels{"repo": {"nodloop"}, "dir": {"."}}),
	}

	got := loop.NewRuns(traces, nil).Scopes(items, traces)

	assert.Equal(t, []loop.ScopeRow{{Version: "0.7.0", Items: 3, SingleSession: 1, NeverApplied: 1}}, got)
}

func TestRunsEffect(t *testing.T) {
	gitC := knowledge.Ref{ID: "git-c", Version: 1}
	run := func(id string, applied, withheld []knowledge.Ref) trace.Trace {
		in, _ := json.Marshal(map[string]any{"applied": applied, "withheld": withheld})
		return trace.Trace{ID: id, Name: trace.NameRun, Producer: "session", Input: in}
	}
	none := []knowledge.Ref{}
	traces := trace.Traces{
		run("taught", none, nil),
		run("a1", []knowledge.Ref{gitC}, nil), run("a2", []knowledge.Ref{gitC}, nil), run("a3", []knowledge.Ref{gitC}, nil),
		run("w1", none, []knowledge.Ref{gitC}), run("w2", none, []knowledge.Ref{gitC}),
		run("bare", none, nil),
	}
	reject := func(id string, code feedback.ReasonCode) feedback.Feedback {
		return feedback.Feedback{TraceID: id, Verdict: feedback.VerdictReject, ReasonCode: code, Reviewer: feedback.ReviewerSession}
	}
	verdicts := feedback.Records{
		reject("taught", feedback.ReasonForm),
		{TraceID: "a1", Verdict: feedback.VerdictApprove, Reviewer: feedback.ReviewerSession},
		reject("a2", feedback.ReasonFact),
		reject("w1", feedback.ReasonForm), reject("w2", feedback.ReasonForm),
		reject("bare", feedback.ReasonForm),
	}
	items := knowledge.Set{{ID: "git-c", Version: 1, Status: knowledge.StatusApproved, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"taught"}}}}

	got := loop.NewRuns(traces, verdicts).Effect(items)

	assert.Equal(t, []loop.EffectRow{
		{Arm: "applied", Runs: 3, Judged: 2, Corrected: 1, SameReason: 0},
		{Arm: "withheld", Runs: 2, Judged: 2, Corrected: 2, SameReason: 2},
	}, got)
}
