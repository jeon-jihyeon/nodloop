package loop_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
		return feedback.Feedback{
			TraceID: id, Verdict: v, ReasonCode: code, Time: at.Add(after), Reviewer: feedback.ReviewerAuthor, Proof: trace.ProofTerminal,
		}
	}
	named := func(id string, v feedback.Verdict, code feedback.ReasonCode, after time.Duration) feedback.Feedback {
		fb := verdict(id, v, code, after)
		fb.Proof = trace.ProofNone
		return fb
	}
	inferred := func(id string, v feedback.Verdict, code feedback.ReasonCode, after time.Duration) feedback.Feedback {
		fb := verdict(id, v, code, after)
		fb.Reviewer, fb.Proof = feedback.ReviewerSession, ""
		return fb
	}
	captured := func(id string, v feedback.Verdict, code feedback.ReasonCode, after time.Duration) feedback.Feedback {
		fb := inferred(id, v, code, after)
		fb.Reviewer = feedback.ReviewerNodloop
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
		{
			"a named verdict without a proof counts apart from a proven one",
			args{feedback.Records{
				verdict("a1", feedback.VerdictApprove, "", 3*time.Hour),
				named("a2", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
				named("a3", feedback.VerdictApprove, "", 3*time.Hour),
				{TraceID: "a4", Verdict: feedback.VerdictApprove, Time: at.Add(3 * time.Hour), Reviewer: "ci"},
			}, item()},
			[]loop.RunItem{{ID: "git-c", Version: 1, Applied: 4, Judged: 1, Followed: 1, NamedJudged: 3, NamedFollowed: 2, NamedRepeat: 1}},
		},
		{
			"a person's verdict wins over both inferred readers and a run they dispute counts nowhere",
			args{feedback.Records{
				inferred("a1", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
				captured("a1", feedback.VerdictReject, feedback.ReasonOther, 4*time.Hour),
				verdict("a1", feedback.VerdictApprove, "", 2*time.Hour),
				inferred("a2", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
				captured("a2", feedback.VerdictApprove, "", 4*time.Hour),
				captured("a3", feedback.VerdictReject, feedback.ReasonOther, 3*time.Hour),
			}, item()},
			[]loop.RunItem{{ID: "git-c", Version: 1, Applied: 4, Judged: 1, Followed: 1, InferredJudged: 1, InferredRepeat: 1}},
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
		verdict("r1", feedback.VerdictApprove, feedback.ReviewerAuthor).WithProof(trace.ProofKey),
		verdict("r2", feedback.VerdictReject, feedback.ReviewerSession),
		// A name with no proof such as one of a script
		verdict("r2", feedback.VerdictEdit, feedback.ReviewerAuthor),
		verdict("r3", feedback.VerdictReject, feedback.ReviewerSession),
		verdict("failed", feedback.VerdictReject, feedback.ReviewerAuthor),
		// An inferred reject of r4 a person withdrew counts as no verdict
		{TraceID: "r4", Time: time.Unix(2, 0), Verdict: feedback.VerdictWithdraw, Reviewer: "ann"},
		{TraceID: "r4", Time: time.Unix(1, 0), Verdict: feedback.VerdictReject, ReasonCode: feedback.ReasonScope, Reviewer: feedback.ReviewerSession},
	}
	items := knowledge.Set{
		item("approved", knowledge.StatusApproved, ""), item("draft", knowledge.StatusCandidate, ""),
		item("compacted", knowledge.StatusCandidate, "c1"),
		{ID: "data", Version: 1, Kind: knowledge.KindMeaning, Content: "lag", Status: knowledge.StatusApproved},
	}

	got := loop.NewRuns(traces, verdicts).Totals(items)

	assert.Equal(t, loop.Totals{Runs: 4, Judged: 1, Named: 1, Inferred: 1, Corrected: 2, Waiting: 1, Approved: 1}, got)
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
	critic := trace.Trace{ID: "c1", Name: trace.NameClassify, Subject: "critic", Ref: "dropped", Output: json.RawMessage(
		`{"members":[{"name":"laya","answers":{"holds":{"yes":0.2},"states":{"yes":0.9}}},{"name":"claude","error":"timeout"}]}`)}
	reaction := trace.Trace{ID: "c2", Name: trace.NameClassify, Subject: "reaction", Ref: "approved", Output: json.RawMessage(
		`{"members":[{"name":"laya","answers":{"corrects":{"yes":0.1},"approves":{"yes":0.1}}}]}`)}
	cites := func(id string, status knowledge.Status, run string) knowledge.Knowledge {
		return knowledge.Knowledge{ID: id, Version: 1, Status: status, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run}}}
	}
	// Newest first as the store lists them
	items := knowledge.Set{
		cites("a", knowledge.StatusRetired, "approved"), cites("a", knowledge.StatusApproved, "approved"),
		cites("b", knowledge.StatusRetired, "dropped"), cites("c", knowledge.StatusCandidate, "open"),
		cites("a", knowledge.StatusCandidate, "approved"), cites("b", knowledge.StatusCandidate, "dropped"),
	}

	tcs := []struct {
		name string
		args trace.Traces
		want []loop.CriticRow
	}{
		{
			"every critique counts against how a person decided the candidate of its run",
			trace.Traces{
				extracted("x1", "approved", "model", `{"attempts":[{`+critique+`,"refusal":"critic"},{`+critique+`}]}`),
				extracted("x2", "dropped", "model", `{"attempts":[{`+critique+`}]}`),
				extracted("x3", "open", "conversation", `{"attempts":[{`+critique+`}]}`),
				extracted("x4", "approved", "conversation", `{"attempts":[{"refusal":"code"}]}`),
				critic, reaction,
			},
			[]loop.CriticRow{
				{Critic: "classifier laya", Judged: 1, Agree: 1},
				{Critic: "extract conversation", Judged: 1, Open: 1},
				{Critic: "extract model", Judged: 3, Agree: 1, FalsePass: 1, FalseRefuse: 1},
			},
		},
		{"a classify trace of the reaction point is no critic judgment", trace.Traces{reaction}, []loop.CriticRow{}},
		{"an extract output that does not decode is skipped", trace.Traces{extracted("x5", "approved", "model", `not json`)}, []loop.CriticRow{}},
		{"no trace has no row", nil, []loop.CriticRow{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, loop.NewRuns(nil, nil).Critics(items, tc.args))
		})
	}
}

func TestRunsReactions(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sureReject := `{"corrects":{"yes":0.95},"approves":{"yes":0.05}}`
	sureApprove := `{"corrects":{"yes":0.05},"approves":{"yes":0.9}}`
	sureNeither := `{"corrects":{"yes":0.1},"approves":{"yes":0.1}}`
	sureBoth := `{"corrects":{"yes":0.9},"approves":{"yes":0.9}}`
	unsure := `{"corrects":{"yes":0.6},"approves":{"yes":0.2}}`
	// A sure endpoint decides alone so the point acts on its answers
	// Otherwise the conversation member defers and the trace keeps no answers
	asked := func(subject, answers string, sure bool) trace.Trace {
		out := `{"members":[{"name":"laya","answers":` + answers + `}],"answers":` + answers + `}`
		if !sure {
			out = `{"members":[{"name":"laya","answers":` + answers + `},{"name":"claude","error":"deferred to the conversation"}]}`
		}
		return trace.Trace{ID: "c1", Name: trace.NameClassify, Subject: subject, Ref: "run", Time: at, Output: json.RawMessage(out)}
	}
	session := func(v feedback.Verdict, after time.Duration) feedback.Feedback {
		return feedback.Feedback{TraceID: "run", Time: at.Add(after), Verdict: v, Reviewer: feedback.ReviewerSession}
	}
	row := func(r loop.ReactionRow) []loop.ReactionRow {
		r.Critic, r.Asked = "reaction laya", 1
		return []loop.ReactionRow{r}
	}
	type args struct {
		trace trace.Trace
		// Newest first as the store lists them
		verdicts feedback.Records
	}
	tcs := []struct {
		name string
		args args
		want []loop.ReactionRow
	}{
		{
			"a sure call the conversation confirms with an edit agrees",
			args{asked("reaction", sureReject, true), feedback.Records{session(feedback.VerdictEdit, time.Minute), session(feedback.VerdictReject, time.Second)}},
			row(loop.ReactionRow{Sure: 1, Agree: 1}),
		},
		{
			"a sure call the conversation overrides disagrees",
			args{asked("reaction", sureReject, true), feedback.Records{session(feedback.VerdictApprove, time.Minute), session(feedback.VerdictReject, time.Second)}},
			row(loop.ReactionRow{Sure: 1, Disagree: 1}),
		},
		{
			"a sure answer of neither on a run the conversation corrected missed the correction",
			args{asked("reaction", sureNeither, true), feedback.Records{session(feedback.VerdictReject, time.Minute)}},
			row(loop.ReactionRow{Sure: 1, MissedCorrection: 1}),
		},
		{
			"a sure answer of neither on a run the conversation approved missed the approval",
			args{asked("reaction", sureNeither, true), feedback.Records{session(feedback.VerdictApprove, time.Minute)}},
			row(loop.ReactionRow{Sure: 1, MissedApproval: 1}),
		},
		{
			"a sure answer of both calls nothing and missed the correction",
			args{asked("reaction", sureBoth, true), feedback.Records{session(feedback.VerdictReject, time.Minute)}},
			row(loop.ReactionRow{Sure: 1, MissedCorrection: 1}),
		},
		{
			"an unsure answer the conversation decided is deferred",
			args{asked("reaction", unsure, false), feedback.Records{session(feedback.VerdictReject, time.Minute)}},
			row(loop.ReactionRow{Deferred: 1}),
		},
		{
			"a verdict the point recorded itself counts only in asked",
			args{asked("reaction", sureApprove, true), feedback.Records{session(feedback.VerdictApprove, time.Second)}},
			row(loop.ReactionRow{}),
		},
		{
			"a session verdict before the point asked is the conversation's",
			args{asked("reaction", sureApprove, true), feedback.Records{session(feedback.VerdictApprove, -time.Minute)}},
			row(loop.ReactionRow{Sure: 1, Agree: 1}),
		},
		{
			"a run without a session verdict counts only in asked",
			args{asked("reaction", sureReject, true), feedback.Records{{TraceID: "run", Time: at, Verdict: feedback.VerdictApprove, Reviewer: "ann"}}},
			row(loop.ReactionRow{}),
		},
		{
			"a classify trace of the critic point is no reaction",
			args{asked("critic", sureReject, true), feedback.Records{session(feedback.VerdictReject, time.Minute)}},
			[]loop.ReactionRow{},
		},
		{
			"an output that does not decode is skipped",
			args{trace.Trace{ID: "c1", Name: trace.NameClassify, Subject: "reaction", Ref: "run", Output: json.RawMessage(`[`)}, nil},
			[]loop.ReactionRow{},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := loop.NewRuns(nil, tc.args.verdicts).Reactions(trace.Traces{tc.args.trace})

			assert.Equal(t, tc.want, got)
		})
	}
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
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// drawn false leaves the draw out as a run recorded without a key does
	run := func(id, producer string, applied, withheld []knowledge.Ref, drawn bool) trace.Trace {
		in, _ := json.Marshal(knowledge.RunInput{Applied: applied, Withheld: withheld, Drawn: drawn, Share: 0.5, Scheme: 1})
		return trace.Trace{ID: id, Name: trace.NameRun, Producer: producer, Input: in}
	}
	none := []knowledge.Ref{}
	traces := trace.Traces{
		run("taught", "session", none, nil, true),
		run("a1", "session", []knowledge.Ref{gitC}, nil, true), run("a2", "session", []knowledge.Ref{gitC}, nil, true),
		run("a3", "session", []knowledge.Ref{gitC}, nil, true),
		run("w1", "session", none, []knowledge.Ref{gitC}, true), run("w2", "session", none, []knowledge.Ref{gitC}, true),
		run("bare", "session", none, nil, true),
		run("legacy", "session", []knowledge.Ref{gitC}, nil, false), run("old withheld", "session", none, []knowledge.Ref{gitC}, false),
		run("s1", "stream-bot", []knowledge.Ref{gitC}, nil, true), run("s2", "stream-bot", none, []knowledge.Ref{gitC}, true),
		run("u1", "cli-bot", []knowledge.Ref{gitC}, nil, false),
	}
	reject := func(id string, code feedback.ReasonCode) feedback.Feedback {
		return feedback.Feedback{TraceID: id, Verdict: feedback.VerdictReject, ReasonCode: code, Reviewer: feedback.ReviewerSession}
	}
	verdicts := feedback.Records{
		reject("taught", feedback.ReasonForm),
		{TraceID: "a1", Verdict: feedback.VerdictApprove, Reviewer: feedback.ReviewerSession, Audit: true},
		// A name with no proof on the applied arm and a proven person on the withheld arm beside the inferred verdicts
		{TraceID: "a2", Verdict: feedback.VerdictReject, ReasonCode: feedback.ReasonFact, Reviewer: "ci"},
		{TraceID: "w1", Verdict: feedback.VerdictReject, ReasonCode: feedback.ReasonForm, Reviewer: "ann", Proof: trace.ProofKey},
		reject("w2", feedback.ReasonForm),
		reject("bare", feedback.ReasonForm), reject("legacy", feedback.ReasonForm),
		reject("s2", feedback.ReasonFact),
	}
	outcomes := feedback.Outcomes{{TraceID: "a3", Result: feedback.ResultConfirmed}, {TraceID: "w2", Result: feedback.ResultRefuted}}
	items := knowledge.Set{{ID: "git-c", Version: 1, Status: knowledge.StatusApproved, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"taught"}}}}
	type want struct {
		producer, arm                                                                            string
		runs, judged, named, inferred, corrected, sameReason, audit, confirmed, refuted, undrawn int
		warnings                                                                                 []loop.EffectWarning
	}
	small := []loop.EffectWarning{loop.WarnInsufficient}
	tcs := []struct {
		name string
		args trace.Traces
		want []want
	}{
		{"each producer has both arms of its drawn runs in producer order", traces, []want{
			{"cli-bot", "applied", 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, small},
			{"cli-bot", "withheld", 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, small},
			{"session", "applied", 3, 2, 1, 1, 1, 0, 1, 1, 0, 2, small},
			{"session", "withheld", 2, 2, 0, 1, 2, 2, 0, 0, 1, 2, small},
			{"stream-bot", "applied", 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, small},
			{"stream-bot", "withheld", 1, 1, 0, 1, 1, 0, 0, 0, 0, 0, small},
		}},
		{"no run with items to receive has both arms at zero without a producer", traces[:1], []want{
			{"", "applied", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, []loop.EffectWarning{}},
			{"", "withheld", 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, []loop.EffectWarning{}},
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []want
			for _, row := range loop.NewRuns(tc.args, verdicts).Effect(items, outcomes, nil, since) {
				assert.Equal(t, since, row.Since)
				got = append(got, want{
					row.Producer, string(row.Arm), row.Runs, row.Judged, row.Named, row.Inferred, row.Corrected, row.SameReason, row.AuditJudged,
					row.Confirmed, row.Refuted, row.Undrawn, row.Warnings,
				})
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

// Drawn runs of one producer under a share of 0.2 with their verdicts
// The first run takes firstShare and every perUnit runs of an arm share one unit
// A perUnit of 0 leaves every run without a unit
func effectRuns(applied, withheld effectArm, firstShare float64, perUnit int) (trace.Traces, feedback.Records) {
	gitC := []knowledge.Ref{{ID: "git-c", Version: 1}}
	var traces trace.Traces
	var verdicts feedback.Records
	for i, a := range []effectArm{applied, withheld} {
		for n := range a.runs {
			id := fmt.Sprintf("%d-%d", i, n)
			in := knowledge.RunInput{Applied: gitC, Drawn: true, Share: 0.2, Scheme: 1}
			if i == 1 {
				in.Applied, in.Withheld = []knowledge.Ref{}, gitC
			}
			if len(traces) == 0 {
				in.Share = firstShare
			}
			if perUnit > 0 {
				in.Unit = fmt.Sprintf("%d-unit-%d", i, n/perUnit)
			}
			raw, _ := json.Marshal(in)
			traces = append(traces, trace.Trace{ID: id, Name: trace.NameRun, Producer: "bot", Input: raw})
			switch {
			case n < a.corrected:
				verdicts = append(verdicts, feedback.Feedback{TraceID: id, Verdict: feedback.VerdictReject, Reviewer: "ann"})
			case n < a.judged:
				verdicts = append(verdicts, feedback.Feedback{TraceID: id, Verdict: feedback.VerdictApprove, Reviewer: "ann"})
			}
		}
	}
	return traces, verdicts
}

type effectArm struct {
	runs, judged, corrected int
}

// The value with its interval or nil without a value
func withInterval(v, low, high *float64) []float64 {
	if v == nil {
		return nil
	}
	return []float64{*v, *low, *high}
}

// The rates and the checks of the arms over drawn runs of one producer under a share of 0.2
func TestRunsEffectStatistics(t *testing.T) {
	type args struct {
		applied, withheld effectArm
		// The share of the first run and 0.2 for the rest
		firstShare float64
		// Runs per unit and 0 for runs without a unit
		perUnit int
	}
	type want struct {
		srmP                      float64
		units                     [2]int
		rates                     [2][]float64
		difference                []float64
		warnings                  []loop.EffectWarning
		insufficient              bool
		expectedApplied, withheld float64
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"arms as drawn give rates with Wilson intervals and a Newcombe difference",
			args{effectArm{160, 128, 32}, effectArm{40, 32, 16}, 0.2, 0},
			want{
				1, [2]int{160, 40}, [2][]float64{{0.25, 0.18301284861358558, 0.33155562952316864}, {0.5, 0.33630882869327483, 0.6636911713067252}},
				[]float64{-0.25, -0.42686740235168846, -0.06711719525694063}, []loop.EffectWarning{}, false, 0.8, 0.2,
			},
		},
		{
			"a split unlike the share warns and leaves the difference out",
			args{effectArm{180, 144, 36}, effectArm{20, 20, 10}, 0.2, 0},
			want{
				0.000406952017444959, [2]int{180, 20},
				[2][]float64{{0.25, 0.18639506191735566, 0.32659675665416266}, {0.5, 0.2992980081982123, 0.7007019918017877}},
				nil, []loop.EffectWarning{loop.WarnSRM, loop.WarnJudgedRate, loop.WarnInsufficient}, true, 0.8, 0.2,
			},
		},
		{
			"runs drawn under two shares warn of mixed shares",
			args{effectArm{160, 128, 32}, effectArm{40, 32, 16}, 0.3, 0},
			want{
				0.9859091865781427, [2]int{160, 40}, [2][]float64{{0.25, 0.18301284861358558, 0.33155562952316864}, {0.5, 0.33630882869327483, 0.6636911713067252}},
				[]float64{-0.25, -0.42686740235168846, -0.06711719525694063}, []loop.EffectWarning{loop.WarnMixedShares}, false, 0.7995, 0.2005,
			},
		},
		{
			"the same split in units of 20 runs tests 9 units against 1 and keeps the difference",
			args{effectArm{180, 144, 36}, effectArm{20, 20, 10}, 0.2, 20},
			want{
				0.42919530044034915, [2]int{9, 1},
				[2][]float64{{0.25, 0.0726952786198401, 0.5863219950508585}, {0.5, 0.2992980081982123, 0.7007019918017877}},
				[]float64{-0.25, -0.5178026395256421, 0.14165517214533824}, []loop.EffectWarning{loop.WarnJudgedRate, loop.WarnInsufficient}, true, 0.8, 0.2,
			},
		},
		{
			"units whose runs fare alike widen the rates by their design effect",
			args{effectArm{160, 160, 40}, effectArm{40, 40, 20}, 0.2, 20},
			want{
				1, [2]int{8, 2}, [2][]float64{{0.25, 0.071479212752109, 0.5907245696898311}, {0.5, 0.09453120573423074, 0.9054687942657693}},
				[]float64{-0.25, -0.6930289094437783, 0.2796207846314732}, []loop.EffectWarning{}, false, 0.8, 0.2,
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			traces, verdicts := effectRuns(tc.args.applied, tc.args.withheld, tc.args.firstShare, tc.args.perUnit)

			rows := loop.NewRuns(traces, verdicts).Effect(nil, nil, nil, time.Time{})

			require.Len(t, rows, 2)
			for i, row := range rows {
				assert.Equal(t, tc.want.units[i], row.Units)
				assert.InDeltaSlice(t, tc.want.rates[i], withInterval(row.Rate, row.RateLow, row.RateHigh), 1e-9)
				assert.InDelta(t, tc.want.srmP, *row.SRMP, 1e-4)
				assert.Equal(t, tc.want.warnings, row.Warnings)
				assert.Equal(t, tc.want.insufficient, row.Insufficient)
				assert.InDeltaSlice(t, tc.want.difference, withInterval(row.Difference, row.DifferenceLow, row.DifferenceHigh), 1e-9)
			}
			assert.InDelta(t, tc.want.expectedApplied, rows[0].ExpectedShare, 1e-9)
			assert.InDelta(t, tc.want.withheld, rows[1].ExpectedShare, 1e-9)
		})
	}
}
