package diagnose_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestDiagnosisJSON(t *testing.T) {
	tcs := []struct {
		name string
		args diagnose.Diagnosis
		want string
	}{
		{
			name: "ready review keeps every field and leaves hold reasons out",
			args: diagnose.Diagnosis{
				Status:       evidence.StatusReadyForReview,
				Observations: []string{"click_count source-a up"},
				Causes:       []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{"mai#1"}}},
				Checks: diagnose.Checks{
					{Step: "compare conversions", Purpose: "tell demand from junk", ParagraphIDs: []string{"mai#1"}},
				},
				OpenQuestions: []string{"campaign launched?"},
			},
			want: `{"status":"ready_for_review","observations":["click_count source-a up"],` +
				`"causes":[{"summary":"low quality traffic","paragraph_ids":["mai#1"]}],` +
				`"checks":[{"step":"compare conversions","purpose":"tell demand from junk","paragraph_ids":["mai#1"]}],` +
				`"open_questions":["campaign launched?"]}`,
		},
		{
			name: "hold review carries its reasons",
			args: diagnose.Diagnosis{Status: evidence.StatusHold, HoldReasons: []string{"gap"}},
			want: `{"status":"hold","observations":null,"causes":null,"checks":null,` +
				`"open_questions":null,"hold_reasons":["gap"]}`,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(tc.args)
			require.NoError(t, err)
			var back diagnose.Diagnosis
			require.NoError(t, json.Unmarshal(b, &back))

			assert.JSONEq(t, tc.want, string(b))
			assert.Equal(t, tc.args, back)
		})
	}
}

func TestChecksParagraphs(t *testing.T) {
	tcs := []struct {
		name string
		args diagnose.Checks
		want []string
	}{
		{"no checks cite nothing", nil, nil},
		{
			"ids of every check in order",
			diagnose.Checks{{ParagraphIDs: []string{"a#1", "b#1"}}, {}, {ParagraphIDs: []string{"a#1"}}},
			[]string{"a#1", "b#1", "a#1"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Paragraphs())
		})
	}
}

func TestKnowledgeApplied(t *testing.T) {
	tcs := []struct {
		name string
		args json.RawMessage
		want []knowledge.Ref
	}{
		{
			"diagnose input names its knowledge",
			json.RawMessage(`{"knowledge":[{"id":"k-1","version":2},{"id":"k-2","version":1}]}`),
			[]knowledge.Ref{{ID: "k-1", Version: 2}, {ID: "k-2", Version: 1}},
		},
		{"diagnose input without knowledge names nothing", json.RawMessage(`{"knowledge":[]}`), nil},
		{"malformed input names nothing", json.RawMessage(`[]`), nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, diagnose.KnowledgeApplied(tc.args))
		})
	}
}

// Every demo event keeps all four procedures because none has front matter
// The scoped front matter puts segment-concentration-review on click_count
// An event that observes conversion_count only loses it
func TestPrepare(t *testing.T) {
	const scoped = "---\nmetrics: [click_count]\n---\n"
	type args struct {
		// Opens segment-concentration-review
		frontMatter string
		event       string
		mode        diagnose.Mode
		session     diagnose.Session
	}
	type want struct {
		procedures []string
		// Runes of the procedure paragraph section
		procedureChars int
	}
	type input struct {
		Mode           diagnose.Mode `json:"mode"`
		PolicyVersion  string        `json:"policy_version"`
		PromptVersion  string        `json:"prompt_version"`
		Procedures     []string      `json:"procedures"`
		ParagraphIDs   []string      `json:"paragraph_ids"`
		ProcedureChars int           `json:"procedure_chars"`
	}
	type output struct {
		ChangeContext       evidence.Context              `json:"change_context"`
		Metrics             []string                      `json:"metrics"`
		KnowledgeCandidates []diagnose.KnowledgeCandidate `json:"knowledge_candidates"`
		ExampleCandidates   []diagnose.ExampleCandidate   `json:"example_candidates"`
	}
	all := want{procedures: demoProcedures, procedureChars: demoProcedureChars}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"interactive context carries the session and every demo procedure",
			args{
				event: "tq-005", mode: diagnose.ModeInteractive,
				session: diagnose.Session{ID: "s1", Tags: []string{"feedback:off"}},
			},
			all,
		},
		{"batch context without a session", args{event: "tq-005", mode: diagnose.ModeBatch}, all},
		{"a demo event that observes nothing keeps every procedure", args{event: "tq-001", mode: diagnose.ModeBatch}, all},
		{
			"a scoped procedure stays with an event that observes its metric",
			args{frontMatter: scoped, event: "tq-005", mode: diagnose.ModeBatch},
			all,
		},
		{
			"a scoped procedure leaves an event that observes none of its metrics",
			args{frontMatter: scoped, event: "tq-009", mode: diagnose.ModeBatch},
			want{procedures: demoProcedures[:3], procedureChars: 4181},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			policy := testkit.Policy(t)
			src := editedDemo(t, "segment-concentration-review.md", "", tc.args.frontMatter)
			d := diagnose.New(src, policy, nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)
			ev, err := src.Event(ctx, tc.args.event)
			require.NoError(t, err)
			observations, err := policy.Analyze(ev)
			require.NoError(t, err)
			procedures, err := src.Procedures(ctx)
			require.NoError(t, err)
			var ids []string
			for _, p := range procedures.Paragraphs() {
				if slices.Contains(tc.want.procedures, p.ID.Procedure()) {
					ids = append(ids, string(p.ID))
				}
			}

			got, err := d.Prepare(ctx, tc.args.event, tc.args.mode, tc.args.session)
			require.NoError(t, err)
			tr, err := s.Traces.Get(ctx, got.PendingID)
			require.NoError(t, err)
			var in input
			require.NoError(t, json.Unmarshal(tr.Input, &in))
			var out output
			require.NoError(t, json.Unmarshal(tr.Output, &out))
			traces, err := s.Traces.List(ctx, trace.Filter{})
			require.NoError(t, err)
			pending := traces.Pending()

			assert.Equal(t, diagnose.Context{
				PendingID:      tr.ID,
				EventID:        tc.args.event,
				Mode:           tc.args.mode,
				PolicyVersion:  "demo-1",
				PromptVersion:  "diagnose/v12",
				ChangeContext:  ev.ChangeContext,
				Session:        tc.args.session,
				Observations:   observations,
				Procedures:     tc.want.procedures,
				ParagraphIDs:   ids,
				ProcedureChars: tc.want.procedureChars,
				Text:           got.Text,
			}, got)
			assert.Equal(t, trace.Trace{
				ID: got.PendingID, Name: trace.NameContext, SessionID: tc.args.session.ID, Subject: tc.args.event, Time: tr.Time,
				Input: tr.Input, Output: tr.Output, Tags: tc.args.session.Tags,
			}, tr)
			assert.Equal(t, input{
				Mode: tc.args.mode, PolicyVersion: "demo-1", PromptVersion: "diagnose/v12", Procedures: tc.want.procedures,
				ParagraphIDs: ids, ProcedureChars: tc.want.procedureChars,
			}, in)
			assert.Equal(t, output{
				ChangeContext: ev.ChangeContext, Metrics: observations.Metrics(),
				KnowledgeCandidates: []diagnose.KnowledgeCandidate{}, ExampleCandidates: []diagnose.ExampleCandidate{},
			}, out)
			assert.Equal(t, trace.Traces{tr}, pending)
		})
	}
}

// A context left open by an interrupted nodloop diagnose carries no session while one of eval carries its session
func TestBatchContextRefusal(t *testing.T) {
	rerun := " so run the batch command that built it again for the event: " +
		"nodloop diagnose --event tq-012 or nodloop eval seed or holdout of that session with --events tq-012"
	tcs := []struct {
		name string
		// Session of the batch context
		args string
		// What record and select both fail with
		want string
	}{
		{
			"names both reruns for a context without a session", "",
			diagnose.ErrBatchContext.Error() + ": batch of tq-012" + rerun,
		},
		{
			"names the session of an eval context", "s1",
			diagnose.ErrBatchContext.Error() + ": batch of tq-012 in session s1" + rerun,
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(s.Source, testkit.Policy(t), nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)
			require.NoError(t, s.Traces.Append(ctx, trace.Trace{
				ID: "batch", Name: trace.NameContext, SessionID: tc.args, Subject: "tq-012",
				Input: json.RawMessage(`{"mode":"batch"}`), Output: json.RawMessage(`{}`),
			}))

			_, recordErr := d.Record(ctx, "batch", diagnose.Diagnosis{Status: evidence.StatusHold})
			_, selectErr := d.Select(ctx, "batch", diagnose.Choices{})

			assert.Equal(t, []string{tc.want, tc.want}, []string{fmt.Sprint(recordErr), fmt.Sprint(selectErr)})
		})
	}
}

// A copy of the demo data set whose procedure file has its first old text replaced
// An empty old puts the replacement at the start of the file
func editedDemo(t *testing.T, file, old, replacement string) *evidencefile.Source {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.CopyFS(dir, os.DirFS(testkit.DemoDir(t))))
	path := filepath.Join(dir, "procedures", file)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(b), old, replacement, 1)), 0o600))
	src, err := evidencefile.New(dir)
	require.NoError(t, err)
	return src
}

func TestPrepareFailure(t *testing.T) {
	type args struct {
		policy analysis.Policy
		event  string
		mode   diagnose.Mode
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{
			"unknown event fails without a trace",
			args{testkit.Policy(t), "nope", diagnose.ModeBatch},
			evidence.ErrNotFound,
		},
		{
			"analysis failure fails without a trace",
			args{analysis.Policy{Analyzers: []analysis.RuleSpec{{Rule: "nope"}}}, "tq-005", diagnose.ModeBatch},
			analysis.ErrUnknownRule,
		},
		{"unknown mode fails without a trace", args{testkit.Policy(t), "tq-005", "chat"}, diagnose.ErrUnknownMode},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(s.Source, tc.args.policy, nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)

			got, err := d.Prepare(ctx, tc.args.event, tc.args.mode, diagnose.Session{})
			assert.ErrorIs(t, err, tc.want)
			traces, err := s.Traces.List(ctx, trace.Filter{})
			require.NoError(t, err)

			assert.Equal(t, diagnose.Context{}, got)
			assert.Empty(t, traces)
		})
	}
}

// The procedures of every demo event and the runes of their paragraph section in its context text
var demoProcedures = []string{
	"data-integrity-hold", "metric-anomaly-investigation", "outcome-rate-degradation", "segment-concentration-review",
}

const (
	demoProcedureChars = 5655
	// Runes of the section headings and the cut notice of the select text
	knowledgeHeading = 141
	knowledgeNotice  = 110
	examplesHeading  = 188
	examplesNotice   = 185
)

func TestRecord(t *testing.T) {
	const (
		confirm    = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
		segment    = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
		decide     = "metric-anomaly-investigation#Metric anomaly investigation/Decide#1"
		ordConfirm = "outcome-rate-degradation#Outcome rate degradation/Confirm the rate#1"
		ordSplit   = "outcome-rate-degradation#Outcome rate degradation/Separate numerator from denominator#1"
	)
	type example struct {
		TraceID string `json:"trace_id"`
		Reason  string `json:"reason"`
		Chars   int    `json:"chars"`
		Cut     bool   `json:"cut"`
	}
	type sections struct {
		Procedures int `json:"procedures"`
		Knowledge  int `json:"knowledge"`
		Examples   int `json:"examples"`
	}
	type args struct {
		knowledge []knowledge.Knowledge
		// Approved items imported because approval refuses a folder over the cap
		imported []knowledge.Knowledge
		// Events reviewed and rejected before the context so they become example candidates
		rejected []string
		selects  []diagnose.Choices
		records  []diagnose.Diagnosis
		// Traces appended after the context whose Ref names a key of the pending ids
		traces  []trace.Trace
		pending string
		diag    diagnose.Diagnosis
	}
	type input struct {
		SessionID     string
		Subject       string
		Tags          []string
		Mode          diagnose.Mode               `json:"mode"`
		PolicyVersion string                      `json:"policy_version"`
		PromptVersion string                      `json:"prompt_version"`
		ChangeContext evidence.Context            `json:"change_context"`
		Metrics       []string                    `json:"metrics"`
		Selector      diagnose.Selector           `json:"select_mode"`
		Procedures    []string                    `json:"procedures"`
		Knowledge     []diagnose.AppliedKnowledge `json:"knowledge"`
		Examples      []example                   `json:"examples"`
		Omitted       bool                        `json:"omitted"`
		Chars         sections                    `json:"chars"`
		OmittedChars  sections                    `json:"omitted_chars"`
		UnknownIDs    []string                    `json:"unknown_ids"`
	}
	type revised struct {
		Subject string
		Reasons []string `json:"reasons"`
		Review  diagnose.Diagnosis
	}
	type outcome struct {
		// Result without its trace id
		result diagnose.Result
		// Name of the trace the result id refers to
		traced  trace.Name
		inputs  []input
		revised []revised
	}
	type want struct {
		outcome outcome
		err     error
		// The key in ids of the trace the error names
		names string
	}
	agg := knowledge.Knowledge{
		ID: "k-agg", Kind: knowledge.KindMeaning, Content: "clicks and conversions use different aggregation time bases",
		Scope: knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}},
	}
	checks := diagnose.Checks{
		{Step: "confirm the signal", Purpose: "signal", ParagraphIDs: []string{confirm}},
		{Step: "check the segment", Purpose: "segment", ParagraphIDs: []string{segment}},
	}
	ready := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{segment}}},
		Checks: checks,
	}
	// Misses the first step of its lead procedure
	incomplete := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{segment}}},
		Checks: diagnose.Checks{{Step: "s", Purpose: "p", ParagraphIDs: []string{segment}}},
	}
	missing := "check " + confirm + " is missing. The first step of the lead procedure is always a check"
	decideCause := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview, Causes: []diagnose.Cause{{Summary: "c", ParagraphIDs: []string{decide}}},
		Checks: diagnose.Checks{{Step: "s", Purpose: "p", ParagraphIDs: []string{confirm}}},
	}
	citesDecide := `cause "c" cites the Decide paragraph ` + decide + `. A Decide paragraph states no cause`
	ordCauses := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{
			{Summary: "a", ParagraphIDs: []string{ordSplit}},
			{Summary: "b", ParagraphIDs: []string{ordConfirm}},
		},
		Checks: diagnose.Checks{{Step: "s", Purpose: "p", ParagraphIDs: []string{ordSplit}}},
	}
	ordMissing := "check " + ordConfirm + " is missing. The first step of the lead procedure is always a check"
	firstStepCause := func(summary, id string) string {
		return `cause "` + summary + `" cites ` + id + `, the first step of its procedure. ` +
			`A first step is a check and never states a cause. Cite the paragraph that states the cause`
	}
	// The first step of the lead procedure is both a check and a cause
	confirmCause := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "c", ParagraphIDs: []string{segment, confirm}}},
		Checks: checks,
	}
	// The first step of an included procedure other than the lead
	otherFirstStep := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{
			{Summary: "a", ParagraphIDs: []string{segment}}, {Summary: "b", ParagraphIDs: []string{ordConfirm}},
		},
		Checks: checks,
	}
	heldConfirm := diagnose.Diagnosis{
		Status: evidence.StatusHold, Causes: []diagnose.Cause{{Summary: "c", ParagraphIDs: []string{confirm}}},
	}
	stepUnchecked := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview, Causes: []diagnose.Cause{{Summary: "c", ParagraphIDs: []string{segment}}},
		Checks: diagnose.Checks{{Step: "s", Purpose: "p", ParagraphIDs: []string{confirm}}},
	}
	madeUp := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "worn trim", ParagraphIDs: []string{"made-up"}}},
		Checks: checks,
	}
	citesNone := `cause "worn trim" cites no paragraph id from the list. Cite the paragraph that states it or return hold`
	twoProcedures := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "c", ParagraphIDs: []string{segment, ordSplit}}},
		Checks: diagnose.Checks{},
	}
	// The old context knows the two paragraphs the review cites
	oldReady := ready
	recorded := input{
		SessionID: "s1", Subject: "tq-005", Tags: []string{"feedback:off"},
		Mode: diagnose.ModeInteractive, PolicyVersion: "demo-1", PromptVersion: "diagnose/v12",
		ChangeContext: evidence.ContextNoKnownChange, Metrics: []string{"click_count", "conversion_count"},
		Procedures: demoProcedures, Knowledge: []diagnose.AppliedKnowledge{}, Examples: []example{},
		Chars: sections{Procedures: demoProcedureChars},
	}
	withUnknown := recorded
	withUnknown.UnknownIDs = []string{"made-up"}
	heldUnknown := withUnknown
	heldUnknown.Tags = []string{"feedback:off", diagnose.TagGateHold}
	heldRecorded := recorded
	heldRecorded.Tags = heldUnknown.Tags
	selected := recorded
	selected.Selector = diagnose.SelectByClaude
	chosen := selected
	chosen.Knowledge = []diagnose.AppliedKnowledge{{ID: "k-agg", Version: 1, Reason: "part of the picture", Chars: 112}}
	chosen.Chars.Knowledge = knowledgeHeading + 112
	// One item over the knowledge cap in Korean so its sizes read in runes
	large := agg
	large.Content = strings.Repeat("가", knowledge.ReviewChars+1000)
	shared := selected
	shared.Omitted = true
	shared.Knowledge = []diagnose.AppliedKnowledge{
		{ID: "k-agg", Version: 1, Chars: knowledge.ReviewChars - 1, OmittedChars: 1092, Cut: true},
	}
	// The cut mark of four digits leaves one rune of the share unused
	shared.Chars.Knowledge = knowledgeHeading + knowledge.ReviewChars - 1 + knowledgeNotice
	shared.OmittedChars.Knowledge = 1092
	withExample := selected
	withExample.Examples = []example{{TraceID: "tq-007", Reason: "same shape", Chars: 683}}
	withExample.Chars.Examples = examplesHeading + 683
	old := input{
		Mode: diagnose.ModeInteractive, PolicyVersion: "demo-0", PromptVersion: "diagnose/v2",
		ChangeContext: evidence.ContextNoKnownChange, Metrics: []string{},
		Knowledge: []diagnose.AppliedKnowledge{}, Examples: []example{},
	}
	oldContext := `{"mode":"interactive","policy_version":"demo-0","prompt_version":"diagnose/v2",` +
		`"paragraph_ids":["` + confirm + `","` + segment + `"]}`
	oldOutput := `{"change_context":"no_known_change","knowledge_candidates":[],"example_candidates":[]}`
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "records a gated review of a context without candidates",
			args: args{pending: "context", diag: diagnose.Diagnosis{
				Status: evidence.StatusReadyForReview, Checks: checks,
				Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{segment, "made-up"}}},
			}},
			want: want{
				outcome: outcome{
					result: diagnose.Result{Diagnosis: ready}, traced: trace.NameDiagnose, inputs: []input{withUnknown},
				},
			},
		},
		{
			name: "copies the chosen knowledge into the record",
			args: args{
				knowledge: []knowledge.Knowledge{agg}, pending: "context", diag: ready,
				selects: []diagnose.Choices{{Knowledge: []diagnose.Choice{{ID: "k-agg", Reason: "part of the picture"}}}},
			},
			want: want{
				outcome: outcome{result: diagnose.Result{Diagnosis: ready}, traced: trace.NameDiagnose, inputs: []input{chosen}},
			},
		},
		{
			name: "copies a knowledge item cut inside its share with the omitted size into the record",
			args: args{
				imported: []knowledge.Knowledge{large}, pending: "context", diag: ready,
				selects: []diagnose.Choices{{Knowledge: []diagnose.Choice{{ID: "k-agg"}}}},
			},
			want: want{
				outcome: outcome{result: diagnose.Result{Diagnosis: ready}, traced: trace.NameDiagnose, inputs: []input{shared}},
			},
		},
		{
			name: "copies the chosen example with its size into the record",
			args: args{
				rejected: []string{"tq-007"}, pending: "context", diag: ready,
				selects: []diagnose.Choices{{Examples: []diagnose.Choice{{ID: "tq-007", Reason: "same shape"}}}},
			},
			want: want{
				outcome: outcome{
					result: diagnose.Result{Diagnosis: ready},
					traced: trace.NameDiagnose,
					inputs: []input{withExample},
				},
			},
		},
		{
			name: "keeps the policy and prompt versions of the context trace",
			args: args{
				traces: []trace.Trace{
					{ID: "old", Name: trace.NameContext, Input: json.RawMessage(oldContext), Output: json.RawMessage(oldOutput)},
				},
				pending: "old",
				diag:    ready,
			},
			want: want{
				outcome: outcome{result: diagnose.Result{Diagnosis: oldReady}, traced: trace.NameDiagnose, inputs: []input{old}},
			},
		},
		{
			name: "newest select replaces an earlier one",
			args: args{
				knowledge: []knowledge.Knowledge{agg},
				selects:   []diagnose.Choices{{Knowledge: []diagnose.Choice{{ID: "k-agg"}}}, {}},
				pending:   "context", diag: ready,
			},
			want: want{
				outcome: outcome{result: diagnose.Result{Diagnosis: ready}, traced: trace.NameDiagnose, inputs: []input{selected}},
			},
		},
		{
			name: "sends back a review that misses the first step of its lead procedure",
			args: args{pending: "context", diag: incomplete},
			want: want{outcome: outcome{
				result: diagnose.Result{Revisions: []string{missing}}, traced: trace.NameRevise,
				revised: []revised{{Subject: "tq-005", Reasons: []string{missing}, Review: incomplete}},
			}},
		},
		{
			name: "records the review after one send back whatever it says",
			args: args{records: []diagnose.Diagnosis{incomplete}, pending: "context", diag: incomplete},
			want: want{outcome: outcome{
				result: diagnose.Result{Diagnosis: incomplete}, traced: trace.NameDiagnose, inputs: []input{recorded},
				revised: []revised{{Subject: "tq-005", Reasons: []string{missing}, Review: incomplete}},
			}},
		},
		{
			name: "sends back a cause that cites a Decide paragraph",
			args: args{pending: "context", diag: decideCause},
			want: want{outcome: outcome{
				result: diagnose.Result{Revisions: []string{citesDecide}}, traced: trace.NameRevise,
				revised: []revised{{Subject: "tq-005", Reasons: []string{citesDecide}, Review: decideCause}},
			}},
		},
		{
			name: "sends back a cause left without a listed paragraph",
			args: args{pending: "context", diag: madeUp},
			want: want{outcome: outcome{
				result: diagnose.Result{Revisions: []string{citesNone}}, traced: trace.NameRevise,
				revised: []revised{{Subject: "tq-005", Reasons: []string{citesNone}, Review: madeUp}},
			}},
		},
		{
			name: "records the forced hold of a cause still uncited after the send back with the unknown ids",
			args: args{records: []diagnose.Diagnosis{madeUp}, pending: "context", diag: madeUp},
			want: want{outcome: outcome{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
					Status: evidence.StatusHold, Causes: []diagnose.Cause{}, Checks: checks,
					HoldReasons: []string{"no paragraph supports: worn trim"},
				}, Forced: true},
				traced: trace.NameDiagnose, inputs: []input{heldUnknown},
				revised: []revised{{Subject: "tq-005", Reasons: []string{citesNone}, Review: madeUp}},
			}},
		},
		{
			name: "records the forced hold of a cause citing only a Decide paragraph after the send back",
			args: args{records: []diagnose.Diagnosis{decideCause}, pending: "context", diag: decideCause},
			want: want{outcome: outcome{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
					Status: evidence.StatusHold, Causes: []diagnose.Cause{}, Checks: decideCause.Checks,
					HoldReasons: []string{`no paragraph supports: c`},
				}, Forced: true},
				traced: trace.NameDiagnose, inputs: []input{heldRecorded},
				revised: []revised{{Subject: "tq-005", Reasons: []string{citesDecide}, Review: decideCause}},
			}},
		},
		{
			name: "holds causes of one procedure to the first step of that procedure and never to it as a cause",
			args: args{pending: "context", diag: ordCauses},
			want: want{outcome: outcome{
				result: diagnose.Result{Revisions: []string{firstStepCause("b", ordConfirm), ordMissing}}, traced: trace.NameRevise,
				revised: []revised{
					{Subject: "tq-005", Reasons: []string{firstStepCause("b", ordConfirm), ordMissing}, Review: ordCauses},
				},
			}},
		},
		{
			name: "sends back a cause that cites the first step of the lead procedure",
			args: args{pending: "context", diag: confirmCause},
			want: want{outcome: outcome{
				result: diagnose.Result{Revisions: []string{firstStepCause("c", confirm)}}, traced: trace.NameRevise,
				revised: []revised{{Subject: "tq-005", Reasons: []string{firstStepCause("c", confirm)}, Review: confirmCause}},
			}},
		},
		{
			name: "sends back a cause that cites the first step of another included procedure",
			args: args{pending: "context", diag: otherFirstStep},
			want: want{outcome: outcome{
				result: diagnose.Result{Revisions: []string{firstStepCause("b", ordConfirm)}}, traced: trace.NameRevise,
				revised: []revised{{Subject: "tq-005", Reasons: []string{firstStepCause("b", ordConfirm)}, Review: otherFirstStep}},
			}},
		},
		{
			name: "never sends a hold back for a first step it cites as a cause",
			args: args{pending: "context", diag: heldConfirm},
			want: want{outcome: outcome{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
					Status: evidence.StatusHold, Causes: []diagnose.Cause{},
					HoldReasons: []string{"the model returned hold without a reason"},
				}},
				traced: trace.NameDiagnose, inputs: []input{recorded},
			}},
		},
		{
			name: "never demands a cited step that no check follows",
			args: args{pending: "context", diag: stepUnchecked},
			want: want{
				outcome: outcome{
					result: diagnose.Result{Diagnosis: stepUnchecked},
					traced: trace.NameDiagnose,
					inputs: []input{recorded},
				},
			},
		},
		{
			name: "never holds causes across two procedures to the steps of either",
			args: args{pending: "context", diag: twoProcedures},
			want: want{
				outcome: outcome{
					result: diagnose.Result{Diagnosis: twoProcedures},
					traced: trace.NameDiagnose,
					inputs: []input{recorded},
				},
			},
		},
		{
			name: "never holds a hold to the steps",
			args: args{pending: "context", diag: diagnose.Diagnosis{
				Status: evidence.StatusHold, Causes: []diagnose.Cause{{Summary: "c", ParagraphIDs: []string{segment}}},
			}},
			want: want{outcome: outcome{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
					Status: evidence.StatusHold, Causes: []diagnose.Cause{},
					HoldReasons: []string{"the model returned hold without a reason"},
				}},
				traced: trace.NameDiagnose, inputs: []input{recorded},
			}},
		},
		{
			name: "records a no_action without causes",
			args: args{pending: "context", diag: diagnose.Diagnosis{Status: evidence.StatusNoAction}},
			want: want{outcome: outcome{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{Status: evidence.StatusNoAction}}, traced: trace.NameDiagnose,
				inputs: []input{recorded},
			}},
		},
		{
			name: "refuses a context that offered candidates without a select",
			args: args{knowledge: []knowledge.Knowledge{agg}, pending: "context", diag: ready},
			want: want{err: diagnose.ErrNotSelected},
		},
		{
			name: "refuses a second record of one context",
			args: args{records: []diagnose.Diagnosis{ready}, pending: "context", diag: ready},
			want: want{outcome: outcome{inputs: []input{recorded}}, err: diagnose.ErrRecorded},
		},
		{
			name: "refuses an unknown pending id",
			args: args{pending: "no-such-id", diag: ready},
			want: want{err: trace.ErrNotFound},
		},
		{
			name: "refuses a diagnose trace as the pending id",
			args: args{records: []diagnose.Diagnosis{ready}, pending: "diagnose", diag: ready},
			want: want{err: diagnose.ErrNotContext},
		},
		{
			name: "fails on a context trace whose input does not decode",
			args: args{
				traces:  []trace.Trace{{ID: "bad", Name: trace.NameContext, Input: json.RawMessage(`[]`)}},
				pending: "bad",
				diag:    ready,
			},
			want: want{err: diagnose.ErrMalformed},
		},
		{
			name: "refuses a context the batch path left open and records nothing",
			args: args{
				traces: []trace.Trace{{
					ID: "batch", Name: trace.NameContext, SessionID: "s1", Subject: "tq-012", Tags: []string{"feedback:off"},
					Input: json.RawMessage(`{"mode":"batch"}`), Output: json.RawMessage(`{}`),
				}},
				pending: "batch", diag: ready,
			},
			want: want{err: diagnose.ErrBatchContext, names: "batch"},
		},
		{
			name: "fails on a context trace whose output does not decode",
			args: args{
				traces: []trace.Trace{
					{ID: "bad", Name: trace.NameContext, Input: json.RawMessage(`{}`), Output: json.RawMessage(`[]`)},
				},
				pending: "bad", diag: ready,
			},
			want: want{err: diagnose.ErrMalformed},
		},
		{
			name: "fails on a select trace whose input does not decode",
			args: args{
				traces:  []trace.Trace{{ID: "bad", Name: trace.NameSelect, Ref: "context", Input: json.RawMessage(`[]`)}},
				pending: "context", diag: ready,
			},
			want: want{err: diagnose.ErrMalformed},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(
				s.Source, testkit.Policy(t), nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			ids := map[string]string{"no-such-id": "no-such-id", "bad": "bad", "old": "old", "batch": "batch"}
			for _, event := range tc.args.rejected {
				c, err := d.Prepare(ctx, event, diagnose.ModeInteractive, diagnose.Session{})
				require.NoError(t, err)
				res, err := d.Record(ctx, c.PendingID, ready)
				require.NoError(t, err)
				ids[event] = res.TraceID
				fb, err := feedback.New(res.TraceID, feedback.VerdictReject, "wrong segment", nil, "", s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Feedback.Append(ctx, fb))
			}
			for _, k := range tc.args.knowledge {
				k.Evidence, k.Author = knowledge.Evidence{ParagraphIDs: []string{"p-1"}}, "author"
				_, _, err := s.Ledger.Propose(ctx, k)
				require.NoError(t, err)
				_, err = s.Ledger.Approve(ctx, k.ID, 1, "author")
				require.NoError(t, err)
			}
			for _, k := range tc.args.imported {
				k.Version, k.Basis, k.Author = 1, knowledge.BasisStated, "author"
				k.Evidence = knowledge.Evidence{ParagraphIDs: []string{"p-1"}}
				k.Status, k.Approver, k.Time = knowledge.StatusApproved, "author", s.Clock.Now()
				require.NoError(t, testkit.Err(s.Ledger.Import(ctx, []knowledge.Knowledge{k})))
			}
			c, err := d.Prepare(
				ctx, "tq-005", diagnose.ModeInteractive, diagnose.Session{ID: "s1", Tags: []string{"feedback:off"}},
			)
			require.NoError(t, err)
			ids["context"] = c.PendingID
			for _, choices := range tc.args.selects {
				picks := diagnose.Choices{Knowledge: choices.Knowledge}
				for _, e := range choices.Examples {
					picks.Examples = append(picks.Examples, diagnose.Choice{ID: ids[e.ID], Reason: e.Reason})
				}
				_, err := d.Select(ctx, c.PendingID, picks)
				require.NoError(t, err)
			}
			for _, diag := range tc.args.records {
				res, err := d.Record(ctx, c.PendingID, diag)
				require.NoError(t, err)
				ids["diagnose"] = res.TraceID
			}
			for _, tr := range tc.args.traces {
				tr.Ref = ids[tr.Ref]
				require.NoError(t, s.Traces.Append(ctx, tr))
			}
			wantOutcome := tc.want.outcome
			wantOutcome.inputs = nil
			for _, in := range tc.want.outcome.inputs {
				examples := []example{}
				for _, e := range in.Examples {
					e.TraceID = ids[e.TraceID]
					examples = append(examples, e)
				}
				in.Examples = examples
				wantOutcome.inputs = append(wantOutcome.inputs, in)
			}

			got, err := d.Record(ctx, ids[tc.args.pending], tc.args.diag)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, fmt.Sprint(err), ids[tc.want.names])
			all, err := s.Traces.List(ctx, trace.Filter{Ref: ids[tc.args.pending]})
			require.NoError(t, err)
			names := map[string]trace.Name{}
			for _, tr := range all {
				names[tr.ID] = tr.Name
			}
			diagnoses, err := s.Traces.List(ctx, trace.Filter{Name: trace.NameDiagnose, Ref: ids[tc.args.pending]})
			require.NoError(t, err)
			var inputs []input
			for _, tr := range diagnoses {
				in := input{SessionID: tr.SessionID, Subject: tr.Subject, Tags: tr.Tags}
				require.NoError(t, json.Unmarshal(tr.Input, &in))
				inputs = append(inputs, in)
			}
			revises, err := s.Traces.List(ctx, trace.Filter{Name: trace.NameRevise, Ref: ids[tc.args.pending]})
			require.NoError(t, err)
			var revisions []revised
			for _, tr := range revises {
				r := revised{Subject: tr.Subject}
				require.NoError(t, json.Unmarshal(tr.Input, &r))
				require.NoError(t, json.Unmarshal(tr.Output, &r.Review))
				revisions = append(revisions, r)
			}

			assert.Equal(t, wantOutcome, outcome{
				result:  diagnose.Result{Diagnosis: got.Diagnosis, Forced: got.Forced, Revisions: got.Revisions},
				traced:  names[got.TraceID],
				inputs:  inputs,
				revised: revisions,
			})
		})
	}
}

func TestRecordConcurrent(t *testing.T) {
	const (
		confirm = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
		segment = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
	)
	type want struct {
		succeeded, refused, recorded int
	}
	tcs := []struct {
		name string
		args int
		want want
	}{
		// Eight writers make an unguarded check and append race in most runs
		{
			"one of many concurrent records on one context wins and the rest are refused",
			8,
			want{succeeded: 1, refused: 7, recorded: 1},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(
				s.Source, testkit.Policy(t), nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			c, err := d.Prepare(ctx, "tq-005", diagnose.ModeInteractive, diagnose.Session{})
			require.NoError(t, err)
			ready := diagnose.Diagnosis{
				Status: evidence.StatusReadyForReview,
				Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{segment}}},
				Checks: diagnose.Checks{
					{Step: "confirm the signal", Purpose: "signal", ParagraphIDs: []string{confirm}},
					{Step: "check the segment", Purpose: "segment", ParagraphIDs: []string{segment}},
				},
			}

			errs := make([]error, tc.args)
			var wg sync.WaitGroup
			for i := range tc.args {
				wg.Go(func() {
					_, errs[i] = d.Record(ctx, c.PendingID, ready)
				})
			}
			wg.Wait()
			traces, err := s.Traces.List(ctx, trace.Filter{Name: trace.NameDiagnose, Ref: c.PendingID})
			require.NoError(t, err)
			succeeded := slices.DeleteFunc(slices.Clone(errs), func(err error) bool { return err != nil })
			refused := slices.DeleteFunc(
				slices.Clone(errs), func(err error) bool { return !errors.Is(err, diagnose.ErrRecorded) },
			)

			assert.Equal(t, tc.want, want{succeeded: len(succeeded), refused: len(refused), recorded: len(traces)})
		})
	}
}

func TestPending(t *testing.T) {
	type want struct {
		// Newest first
		ids []string
		err error
	}
	opened := func(id, mode string) trace.Trace {
		return trace.Trace{
			ID: id, Name: trace.NameContext, Subject: "tq-005",
			Input: json.RawMessage(`{"mode":"` + mode + `"}`), Output: json.RawMessage(`{}`),
		}
	}
	recorded := trace.Trace{ID: "review", Name: trace.NameDiagnose, Ref: "closed", Subject: "tq-005"}
	tcs := []struct {
		name string
		args []trace.Trace
		want want
	}{
		{
			"lists the open contexts of the conversation and leaves out batch and recorded ones",
			[]trace.Trace{opened("open", "interactive"), opened("batch", "batch"), opened("closed", "interactive"), recorded},
			want{ids: []string{"open"}},
		},
		{
			"a context that no longer decodes fails the list",
			[]trace.Trace{opened("open", "interactive"), {ID: "bad", Name: trace.NameContext, Input: json.RawMessage(`[]`)}},
			want{err: diagnose.ErrMalformed},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			for _, tr := range tc.args {
				require.NoError(t, s.Traces.Append(ctx, tr))
			}
			d := diagnose.New(s.Source, testkit.Policy(t), nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)

			got, err := d.Pending(ctx)

			var ids []string
			for _, tr := range got {
				ids = append(ids, tr.ID)
			}
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.ids, ids)
		})
	}
}

func TestRun(t *testing.T) {
	const (
		confirm = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
		segment = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
	)
	type call struct {
		prompt   string
		response llm.Response
		err      error
	}
	type args struct {
		// Model calls in order
		calls []call
		opts  diagnose.BatchOptions
	}
	// One trace that refers to the context
	type recorded struct {
		Name       trace.Name
		Model      string
		Error      string
		Output     string
		Usage      trace.Usage
		DurationMS int64
		SessionID  string
		Tags       []string
		Mode       diagnose.Mode     `json:"mode"`
		Selector   diagnose.Selector `json:"select_mode"`
		Reasons    []string          `json:"reasons"`
	}
	type outcome struct {
		// Result without its trace id
		result diagnose.Result
		// Name of the trace the result id refers to
		traced trace.Name
		// Newest first
		traces []recorded
	}
	type want struct {
		outcome outcome
		err     error
	}
	ctx := context.Background()
	base := testkit.Open(t)
	preparer := diagnose.New(
		base.Source, testkit.Policy(t), nil, base.Traces, base.Feedback, base.Ledger,
		base.Clock.Now,
	)
	prepared, err := preparer.Prepare(ctx, "tq-005", diagnose.ModeBatch, diagnose.Session{})
	require.NoError(t, err)
	text := prepared.Text
	ready := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{segment}}},
		Checks: diagnose.Checks{
			{Step: "confirm the signal", Purpose: "signal", ParagraphIDs: []string{confirm}},
			{Step: "check the segment", Purpose: "segment", ParagraphIDs: []string{segment}},
		},
	}
	output, err := json.Marshal(ready)
	require.NoError(t, err)
	incomplete := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{segment}}},
		Checks: diagnose.Checks{{Step: "s", Purpose: "p", ParagraphIDs: []string{segment}}},
	}
	incompleteOutput, err := json.Marshal(incomplete)
	require.NoError(t, err)
	missing := "check " + confirm + " is missing. The first step of the lead procedure is always a check"
	revisePrompt := text + "\n\n## Your previous review\n\n" + string(incompleteOutput) +
		"\n\n## Revise\n\nReturn the review again with only these defects fixed:\n- " + missing + "\n"
	maxTurns := &llm.ResultError{Subtype: "error_max_turns", CostUSD: 0.01}
	step := testkit.ClockStep.Milliseconds()
	// A select trace input names its selector under mode
	selectedByCode := recorded{Name: trace.NameSelect, Mode: diagnose.Mode(diagnose.SelectByCode)}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "records the model review with usage and duration",
			args: args{
				calls: []call{{
					prompt: text,
					response: llm.Response{
						Output:       output,
						CostUSD:      0.02,
						InputTokens:  100,
						OutputTokens: 30,
						CacheRead:    7,
						CacheCreate:  5,
					},
				}},
				opts: diagnose.BatchOptions{
					Model:   "haiku",
					Session: diagnose.Session{ID: "eval-1", Tags: []string{"knowledge:on"}},
				},
			},
			want: want{outcome: outcome{
				result: diagnose.Result{Diagnosis: ready},
				traced: trace.NameDiagnose,
				traces: []recorded{
					{
						Name: trace.NameDiagnose, Model: "haiku", Output: string(output), DurationMS: step, SessionID: "eval-1",
						Usage: trace.Usage{
							InputTokens:       100,
							OutputTokens:      30,
							CacheReadTokens:   7,
							CacheCreateTokens: 5,
							CostUSD:           0.02,
						},
						Tags: []string{"knowledge:on"}, Mode: diagnose.ModeBatch, Selector: diagnose.SelectByCode,
					},
					{
						Name:      trace.NameSelect,
						SessionID: "eval-1",
						Tags:      []string{"knowledge:on"},
						Mode:      diagnose.Mode(diagnose.SelectByCode),
					},
				},
			}},
		},
		{
			name: "revises a review sent back through a second call that sees the first review and the reasons",
			args: args{
				calls: []call{
					{
						prompt:   text,
						response: llm.Response{Output: incompleteOutput, CostUSD: 0.02, InputTokens: 100, OutputTokens: 10},
					},
					{
						prompt:   revisePrompt,
						response: llm.Response{Output: output, CostUSD: 0.03, InputTokens: 150, OutputTokens: 12},
					},
				},
				opts: diagnose.BatchOptions{
					Model:   "haiku",
					Session: diagnose.Session{ID: "eval-1", Tags: []string{"feedback:off"}},
				},
			},
			want: want{outcome: outcome{
				result: diagnose.Result{Diagnosis: ready},
				traced: trace.NameDiagnose,
				traces: []recorded{
					{
						Name: trace.NameDiagnose, Model: "haiku", Output: string(output), DurationMS: step, SessionID: "eval-1",
						Usage: trace.Usage{InputTokens: 150, OutputTokens: 12, CostUSD: 0.03}, Tags: []string{"feedback:off"},
						Mode: diagnose.ModeBatch, Selector: diagnose.SelectByCode,
					},
					{
						Name: trace.NameRevise, Model: "haiku", Output: string(incompleteOutput), DurationMS: step, SessionID: "eval-1",
						Usage: trace.Usage{InputTokens: 100, OutputTokens: 10, CostUSD: 0.02}, Tags: []string{"feedback:off"},
						Reasons: []string{missing},
					},
					{
						Name:      trace.NameSelect,
						SessionID: "eval-1",
						Tags:      []string{"feedback:off"},
						Mode:      diagnose.Mode(diagnose.SelectByCode),
					},
				},
			}},
		},
		{
			name: "model failure records its error and cost and closes the context",
			args: args{calls: []call{{prompt: text, err: maxTurns}}},
			want: want{
				outcome: outcome{
					traced: trace.NameDiagnose,
					traces: []recorded{
						{
							Name: trace.NameDiagnose, Error: maxTurns.Error(), Usage: trace.Usage{CostUSD: 0.01}, DurationMS: step,
							Mode: diagnose.ModeBatch, Selector: diagnose.SelectByCode,
						},
						selectedByCode,
					},
				},
				err: maxTurns,
			},
		},
		{
			name: "output outside the schema fails and records the error",
			args: args{
				calls: []call{{prompt: text, response: llm.Response{Output: json.RawMessage(`{"status": 42}`), CostUSD: 0.02}}},
			},
			want: want{
				outcome: outcome{
					traced: trace.NameDiagnose,
					traces: []recorded{
						{
							Name: trace.NameDiagnose,
							Error: "diagnose: output does not match the schema: " +
								"json: cannot unmarshal number into Go struct field Diagnosis.status of type evidence.Status",
							Usage: trace.Usage{CostUSD: 0.02}, DurationMS: step, Mode: diagnose.ModeBatch, Selector: diagnose.SelectByCode,
						},
						selectedByCode,
					},
				},
				err: diagnose.ErrBadOutput,
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			d := diagnose.New(
				s.Source, testkit.Policy(t), client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			var calls []any
			for _, c := range tc.args.calls {
				request := llm.Request{
					System: diagnose.Rules,
					Prompt: c.prompt,
					Schema: json.RawMessage(diagnose.Schema),
					Model:  tc.args.opts.Model,
				}
				calls = append(calls, client.EXPECT().Complete(ctx, request).Return(c.response, c.err))
			}
			gomock.InOrder(calls...)

			got, err := d.Run(ctx, "tq-005", tc.args.opts)
			assert.ErrorIs(t, err, tc.want.err)
			contexts, err := s.Traces.List(ctx, trace.Filter{Name: trace.NameContext})
			require.NoError(t, err)
			require.Len(t, contexts, 1)
			traces, err := s.Traces.List(ctx, trace.Filter{Ref: contexts[0].ID})
			require.NoError(t, err)
			names := map[string]trace.Name{}
			var views []recorded
			for _, tr := range traces {
				names[tr.ID] = tr.Name
				view := recorded{
					Name: tr.Name, Model: tr.Model, Error: tr.Error, Output: string(tr.Output), Usage: tr.Usage,
					DurationMS: tr.DurationMS, SessionID: tr.SessionID, Tags: tr.Tags,
				}
				require.NoError(t, json.Unmarshal(tr.Input, &view))
				views = append(views, view)
			}
			all, err := s.Traces.List(ctx, trace.Filter{})
			require.NoError(t, err)
			pending := all.Pending()

			assert.Equal(t, tc.want.outcome, outcome{
				result: diagnose.Result{Diagnosis: got.Diagnosis, Forced: got.Forced, Revisions: got.Revisions},
				traced: names[got.TraceID],
				traces: views,
			})
			assert.Empty(t, pending)
		})
	}
}

func TestRunWithoutModelCall(t *testing.T) {
	type args struct {
		client    llm.Client
		event     string
		knowledge diagnose.KnowledgeMode
	}
	// No call is expected so any model call fails the test
	client := llmmock.NewMockClient(gomock.NewController(t))
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"diagnoser without a client refuses the batch path", args{nil, "tq-005", ""}, diagnose.ErrNoClient},
		{"unknown event fails before the model call", args{client, "nope", ""}, evidence.ErrNotFound},
		{"unknown knowledge mode fails before the context", args{client, "tq-005", "most"}, diagnose.ErrUnknownKnowledgeMode},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(
				s.Source, testkit.Policy(t), tc.args.client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)

			got, err := d.Run(ctx, tc.args.event, diagnose.BatchOptions{Knowledge: tc.args.knowledge})
			assert.ErrorIs(t, err, tc.want)
			traces, err := s.Traces.List(ctx, trace.Filter{})
			require.NoError(t, err)

			assert.Equal(t, diagnose.Result{}, got)
			assert.Empty(t, traces)
		})
	}
}

// A review of tq-007 rejected before k-agg is approved makes both an example and a knowledge candidate of tq-005
// Prepare reads the ledger and the feedback once each so the selection makes the second read
func TestRunStoreFailure(t *testing.T) {
	type args struct {
		opts diagnose.BatchOptions
		// Reads that succeed before each store fails
		knowledgeReads, feedbackReads int
	}
	type outcome struct {
		// Name of the trace the result id refers to
		traced trace.Name
		// Names and errors of the traces written newest first
		names  []trace.Name
		errors []string
	}
	type want struct {
		outcome outcome
		err     error
	}
	failed := outcome{
		traced: trace.NameDiagnose,
		names:  []trace.Name{trace.NameDiagnose, trace.NameContext, trace.NameDiagnose, trace.NameContext},
		errors: []string{assert.AnError.Error(), "", "", ""},
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "a selection that cannot read the ledger closes the context with an error trace",
			args: args{
				opts:           diagnose.BatchOptions{Knowledge: diagnose.KnowledgeSelected},
				knowledgeReads: 1,
				feedbackReads:  9,
			},
			want: want{outcome: failed, err: assert.AnError},
		},
		{
			name: "a selection of every approved item that cannot read the ledger closes the context with an error trace",
			args: args{
				opts:           diagnose.BatchOptions{Knowledge: diagnose.KnowledgeAll},
				knowledgeReads: 1,
				feedbackReads:  9,
			},
			want: want{outcome: failed, err: assert.AnError},
		},
		{
			name: "an example that cannot be joined with its feedback closes the context with an error trace",
			args: args{opts: diagnose.BatchOptions{Examples: 1}, knowledgeReads: 9, feedbackReads: 1},
			want: want{outcome: failed, err: assert.AnError},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			dir := t.TempDir()
			items, err := knowledgefile.New(dir)
			require.NoError(t, err)
			verdicts, err := feedbackfile.New(dir)
			require.NoError(t, err)
			ledger := knowledge.NewLedger(
				items, vetofile.NewApprovedFile(t.TempDir(), "records"),
				s.Clock.Now, func(prefix string) string { return prefix + "generated" },
			)
			reviewer := diagnose.New(
				s.Source, testkit.Policy(t), nil, s.Traces, verdicts, ledger, s.Clock.Now,
			)
			c, err := reviewer.Prepare(ctx, "tq-007", diagnose.ModeInteractive, diagnose.Session{})
			require.NoError(t, err)
			res, err := reviewer.Record(ctx, c.PendingID, diagnose.Diagnosis{Status: evidence.StatusNoAction})
			require.NoError(t, err)
			fb, err := feedback.New(res.TraceID, feedback.VerdictReject, "wrong cause", nil, "", s.Clock.Now())
			require.NoError(t, err)
			require.NoError(t, verdicts.Append(ctx, fb))
			_, _, err = ledger.Propose(ctx, knowledge.Knowledge{
				ID: "k-agg", Kind: knowledge.KindMeaning, Content: "aggregation time bases differ", Author: "author",
				Scope:    knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}},
				Evidence: knowledge.Evidence{ParagraphIDs: []string{"p-1"}},
			})
			require.NoError(t, err)
			_, err = ledger.Approve(ctx, "k-agg", 1, "author")
			require.NoError(t, err)
			flakyKnowledge := &testkit.FlakyKnowledge{
				Store: items,
				Reads: testkit.Reads{Allowed: tc.args.knowledgeReads, Err: assert.AnError},
			}
			flakyFeedback := &testkit.FlakyFeedback{
				Store: verdicts,
				Reads: testkit.Reads{Allowed: tc.args.feedbackReads, Err: assert.AnError},
			}
			flakyLedger := knowledge.NewLedger(
				flakyKnowledge, vetofile.NewApprovedFile(t.TempDir(), "records"),
				s.Clock.Now, func(prefix string) string { return prefix + "generated" },
			)
			client := llmmock.NewMockClient(gomock.NewController(t))
			d := diagnose.New(
				s.Source, testkit.Policy(t), client, s.Traces, flakyFeedback, flakyLedger, s.Clock.Now,
			)

			got, err := d.Run(ctx, "tq-005", tc.args.opts)
			assert.ErrorIs(t, err, tc.want.err)
			traces, err := s.Traces.List(ctx, trace.Filter{})
			require.NoError(t, err)
			view := outcome{}
			names := map[string]trace.Name{}
			for _, tr := range traces {
				names[tr.ID] = tr.Name
				view.names = append(view.names, tr.Name)
				view.errors = append(view.errors, tr.Error)
			}
			view.traced = names[got.TraceID]
			pending := traces.Pending()

			assert.Equal(t, tc.want.outcome, view)
			assert.Empty(t, pending)
		})
	}
}

// The traces live in their own record dir that the model call changes
// tq-008 offers the approved item k-agg so every row writes a select trace before the model call
// During the call the trace file keeps only the named traces and the record dir moves through a hop to its new name
func TestRunKeepsItsSelection(t *testing.T) {
	type args struct {
		mode diagnose.KnowledgeMode
		// Names of the traces left in the file during the model call
		keep []trace.Name
		// Name of the record dir after the model call
		moved string
	}
	type written struct {
		name    trace.Name
		applied []knowledge.Ref
	}
	type outcome struct {
		// Name of the trace the result id refers to
		traced trace.Name
		// Newest first
		traces []written
	}
	type want struct {
		outcome outcome
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "knowledge none with an offered item writes an empty select trace and records the review",
			args: args{
				mode:  diagnose.KnowledgeNone,
				keep:  []trace.Name{trace.NameContext, trace.NameSelect},
				moved: "records",
			},
			want: want{outcome: outcome{
				traced: trace.NameDiagnose,
				traces: []written{{name: trace.NameDiagnose}, {name: trace.NameSelect}, {name: trace.NameContext}},
			}},
		},
		{
			name: "a select trace lost from the store during the model call never fails the review",
			args: args{mode: diagnose.KnowledgeSelected, keep: []trace.Name{trace.NameContext}, moved: "records"},
			want: want{outcome: outcome{
				traced: trace.NameDiagnose,
				traces: []written{{name: trace.NameDiagnose, applied: []knowledge.Ref{{ID: "k-agg", Version: 1}}}, {name: trace.NameContext}},
			}},
		},
		{
			name: "a record dir moved during the model call fails as a store error and never as a missing select",
			args: args{
				mode:  diagnose.KnowledgeSelected,
				keep:  []trace.Name{trace.NameContext, trace.NameSelect},
				moved: "elsewhere",
			},
			want: want{
				outcome: outcome{
					traces: []written{{name: trace.NameSelect, applied: []knowledge.Ref{{ID: "k-agg", Version: 1}}}, {name: trace.NameContext}},
				},
				err: os.ErrNotExist,
			},
		},
	}
	output, err := json.Marshal(diagnose.Diagnosis{Status: evidence.StatusNoAction})
	require.NoError(t, err)
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			root := t.TempDir()
			dir := filepath.Join(root, "records")
			require.NoError(t, os.Mkdir(dir, 0o700))
			traces, err := tracefile.New(dir)
			require.NoError(t, err)
			_, _, err = s.Ledger.Propose(ctx, knowledge.Knowledge{
				ID: "k-agg", Kind: knowledge.KindMeaning, Content: "aggregation time bases differ", Author: "author",
				Scope:    knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}},
				Evidence: knowledge.Evidence{ParagraphIDs: []string{"p-1"}},
			})
			require.NoError(t, err)
			_, err = s.Ledger.Approve(ctx, "k-agg", 1, "author")
			require.NoError(t, err)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().
				Complete(ctx, gomock.Any()).
				DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
					path := filepath.Join(dir, "traces.jsonl")
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					lines := slices.DeleteFunc(bytes.SplitAfter(data, []byte("\n")), func(line []byte) bool {
						var tr trace.Trace
						return json.Unmarshal(line, &tr) != nil || !slices.Contains(tc.args.keep, tr.Name)
					})
					require.NoError(t, os.WriteFile(path, bytes.Join(lines, nil), 0o600))
					require.NoError(t, os.Rename(dir, filepath.Join(root, "hop")))
					require.NoError(t, os.Rename(filepath.Join(root, "hop"), filepath.Join(root, tc.args.moved)))
					return llm.Response{Output: output}, nil
				})
			d := diagnose.New(
				s.Source, testkit.Policy(t), client, traces, s.Feedback, s.Ledger, s.Clock.Now,
			)

			got, err := d.Run(ctx, "tq-008", diagnose.BatchOptions{Knowledge: tc.args.mode})
			assert.ErrorIs(t, err, tc.want.err)
			assert.NotErrorIs(t, err, diagnose.ErrNotSelected)
			moved, err := tracefile.New(filepath.Join(root, tc.args.moved))
			require.NoError(t, err)
			all, err := moved.List(ctx, trace.Filter{})
			require.NoError(t, err)
			view := outcome{}
			names := map[string]trace.Name{}
			for _, tr := range all {
				names[tr.ID] = tr.Name
				view.traces = append(view.traces, written{name: tr.Name, applied: diagnose.KnowledgeApplied(tr.Input)})
			}
			view.traced = names[got.TraceID]

			assert.Equal(t, tc.want.outcome, view)
		})
	}
}

// Real claude call on a demo event
// It spends plan usage so it runs only when enabled by env
func TestRunLive(t *testing.T) {
	if os.Getenv("NODLOOP_LLM_LIVE") == "" {
		t.Skip("set NODLOOP_LLM_LIVE=1 to run a real review")
	}
	ctx := context.Background()
	s := testkit.Open(t)
	client := llm.NewClaudeCLI("", "", "", 0)
	d := diagnose.New(
		s.Source, testkit.Policy(t), client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
	)

	got, err := d.Run(ctx, "tq-005", diagnose.BatchOptions{Model: "haiku"})
	require.NoError(t, err)
	tr, err := s.Traces.Get(ctx, got.TraceID)
	require.NoError(t, err)

	statuses := []evidence.Status{evidence.StatusNoAction, evidence.StatusReadyForReview, evidence.StatusHold}
	assert.Contains(t, statuses, got.Diagnosis.Status)
	t.Logf(
		"status %s causes %d forced %v cost $%.4f tokens in %d out %d", got.Diagnosis.Status, len(got.Diagnosis.Causes),
		got.Forced, tr.Usage.CostUSD, tr.Usage.InputTokens, tr.Usage.OutputTokens,
	)
}

// A paragraph of a procedure whose scope leaves the event is unknown to its review like any other unlisted id
func TestRecordExcludedProcedure(t *testing.T) {
	const excluded = "segment-concentration-review#Segment concentration review/Check the concentrated segment#1"
	type want struct {
		// Whether the context lists the cited paragraph
		listed    bool
		revisions []string
		forced    bool
		status    evidence.Status
		unknown   []string
	}
	tcs := []struct {
		name string
		// The paragraph the cause cites
		args string
		want want
	}{
		{
			"a paragraph of a scoped out procedure is sent back and then held as unknown",
			excluded,
			want{
				revisions: []string{
					`cause "low quality source" cites no paragraph id from the list. Cite the paragraph that states it or return hold`,
				},
				forced: true, status: evidence.StatusHold, unknown: []string{excluded},
			},
		},
		{
			"a paragraph of an included procedure is known",
			"outcome-rate-degradation#Outcome rate degradation/Separate numerator from denominator#1",
			want{
				listed: true,
				revisions: []string{
					"check outcome-rate-degradation#Outcome rate degradation/Confirm the rate#1 is missing. " +
						"The first step of the lead procedure is always a check",
				},
				status: evidence.StatusReadyForReview,
			},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			src := editedDemo(t, "segment-concentration-review.md", "", "---\nmetrics: [click_count]\n---\n")
			d := diagnose.New(src, testkit.Policy(t), nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)
			c, err := d.Prepare(ctx, "tq-009", diagnose.ModeInteractive, diagnose.Session{})
			require.NoError(t, err)
			_, err = d.Select(ctx, c.PendingID, diagnose.Choices{})
			require.NoError(t, err)
			review := diagnose.Diagnosis{
				Status: evidence.StatusReadyForReview,
				Causes: []diagnose.Cause{{Summary: "low quality source", ParagraphIDs: []string{tc.args}}},
				Checks: diagnose.Checks{},
			}

			first, err := d.Record(ctx, c.PendingID, review)
			require.NoError(t, err)
			second, err := d.Record(ctx, c.PendingID, review)
			require.NoError(t, err)
			tr, err := s.Traces.Get(ctx, second.TraceID)
			require.NoError(t, err)
			var in struct {
				UnknownIDs []string `json:"unknown_ids"`
			}
			require.NoError(t, json.Unmarshal(tr.Input, &in))

			assert.Equal(t, tc.want, want{
				listed: slices.Contains(c.ParagraphIDs, tc.args), revisions: first.Revisions, forced: second.Forced,
				status: second.Diagnosis.Status, unknown: in.UnknownIDs,
			})
		})
	}
}
