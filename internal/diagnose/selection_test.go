package diagnose_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestPrepareCandidates(t *testing.T) {
	const (
		confirm = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
		segment = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
	)
	type verdict struct {
		review  string
		verdict feedback.Verdict
		reason  string
		edited  json.RawMessage
	}
	type args struct {
		knowledge []knowledge.Knowledge
		reviews   []string
		// Appended as they are so a verdict can name them by id
		traces   []trace.Trace
		verdicts []verdict
		// Appended after verdicts with the session reviewer
		sessionVerdicts []verdict
		event           string
		// Added to the demo policy that is bound to the demo data before the diagnoser gets it
		analyzers []analysis.RuleSpec
	}
	type offer struct {
		knowledge []diagnose.KnowledgeCandidate
		examples  []diagnose.ExampleCandidate
		omitted   bool
	}
	type want struct {
		offer offer
		err   error
	}
	// tq-005 and tq-007 and tq-013 share the no_known_change context and the click_count metric with tq-008
	// tq-009 has the planned change context
	reviews := []string{"tq-005", "tq-007", "tq-013", "tq-009"}
	verdicts := []verdict{
		{"tq-005", feedback.VerdictReject, "older reason", nil},
		{"tq-007", feedback.VerdictEdit, "newer reason", json.RawMessage(`{"status":"hold"}`)},
		{"tq-013", feedback.VerdictReject, "later approved", nil},
		{"tq-009", feedback.VerdictReject, "other context", nil},
		{"tq-013", feedback.VerdictApprove, "", nil},
	}
	// Eleven corrections that fit tq-008 so the cap leaves out the oldest
	var many []trace.Trace
	var manyVerdicts []verdict
	var manyOffered []diagnose.ExampleCandidate
	for i := range 11 {
		id := fmt.Sprintf("r%02d", i)
		many = append(many, trace.Trace{
			ID: id, Name: trace.NameDiagnose, Subject: "tq-005", Output: json.RawMessage(`{"status":"hold"}`),
			Input: json.RawMessage(`{"change_context":"no_known_change","metrics":["click_count"]}`),
		})
		manyVerdicts = append(manyVerdicts, verdict{id, feedback.VerdictReject, "reason " + id, nil})
		if i > 0 {
			offered := diagnose.ExampleCandidate{TraceID: id, Verdict: feedback.VerdictReject, Head: "reason " + id}
			manyOffered = append([]diagnose.ExampleCandidate{offered}, manyOffered...)
		}
	}
	// One older correction with the metric set of tq-008 and ten newer ones that share only click_count or add a metric
	ranked := []trace.Trace{{
		ID: "near", Name: trace.NameDiagnose, Subject: "tq-006", Output: json.RawMessage(`{"status":"hold"}`),
		Input: json.RawMessage(`{"change_context":"no_known_change","metrics":["click_count","conversion_count"]}`),
	}}
	rankedVerdicts := []verdict{{"near", feedback.VerdictReject, "same metric set", nil}}
	rankedOffered := []diagnose.ExampleCandidate{{TraceID: "near", Verdict: feedback.VerdictReject, Head: "same metric set"}}
	for i := range 10 {
		id := fmt.Sprintf("far%02d", i)
		metrics := `["click_count"]`
		if i%2 == 1 {
			metrics = `["click_count","conversion_count","impression_count"]`
		}
		ranked = append(ranked, trace.Trace{
			ID: id, Name: trace.NameDiagnose, Subject: "tq-005", Output: json.RawMessage(`{"status":"hold"}`),
			Input: json.RawMessage(`{"change_context":"no_known_change","metrics":` + metrics + `}`),
		})
		rankedVerdicts = append(rankedVerdicts, verdict{id, feedback.VerdictReject, "reason " + id, nil})
		if i > 0 {
			offered := diagnose.ExampleCandidate{TraceID: id, Verdict: feedback.VerdictReject, Head: "reason " + id}
			rankedOffered = slices.Insert(rankedOffered, 1, offered)
		}
	}
	// One correction of tq-008 and three newer ones of tq-005 with the same metric set
	own := []trace.Trace{{
		ID: "own", Name: trace.NameDiagnose, Subject: "tq-008", Output: json.RawMessage(`{"status":"hold"}`),
		Input: json.RawMessage(`{"change_context":"no_known_change","metrics":["click_count"]}`),
	}}
	var newer []verdict
	var newerOffered []diagnose.ExampleCandidate
	for i := range 3 {
		id := fmt.Sprintf("newer%d", i)
		own = append(own, trace.Trace{
			ID: id, Name: trace.NameDiagnose, Subject: "tq-005", Output: json.RawMessage(`{"status":"hold"}`),
			Input: json.RawMessage(`{"change_context":"no_known_change","metrics":["click_count"]}`),
		})
		newer = append(newer, verdict{id, feedback.VerdictReject, "reason " + id, nil})
		offered := diagnose.ExampleCandidate{TraceID: id, Verdict: feedback.VerdictReject, Head: "reason " + id}
		newerOffered = append([]diagnose.ExampleCandidate{offered}, newerOffered...)
	}
	ownRejected := diagnose.ExampleCandidate{TraceID: "own", Verdict: feedback.VerdictReject, Head: "own reason"}
	aggregation := "clicks and conversions use different aggregation time bases"
	conversions := knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "offers knowledge whose scope fits the event",
			args: args{
				knowledge: []knowledge.Knowledge{
					{ID: "k-agg", Kind: knowledge.KindMeaning, Content: aggregation, Scope: conversions},
					{
						ID: "k-other", Kind: knowledge.KindJudgment, Content: "only for planned changes",
						Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}}},
					},
				},
				event: "tq-005",
			},
			want: want{offer: offer{knowledge: []diagnose.KnowledgeCandidate{
				{ID: "k-agg", Version: 1, Kind: knowledge.KindMeaning, Head: aggregation, Scope: conversions},
			}}},
		},
		{
			name: "heads knowledge with its first line cut to one terminal line",
			args: args{
				knowledge: []knowledge.Knowledge{
					{ID: "k-lines", Kind: knowledge.KindMeaning, Content: "  first line  \nsecond line"},
					{ID: "k-long", Kind: knowledge.KindMeaning, Content: strings.Repeat("y", 130)},
					{ID: "k-wide", Kind: knowledge.KindMeaning, Content: strings.Repeat("가", 130)},
				},
				event: "tq-005",
			},
			want: want{offer: offer{knowledge: []diagnose.KnowledgeCandidate{
				{ID: "k-lines", Version: 1, Kind: knowledge.KindMeaning, Head: "first line"},
				{ID: "k-long", Version: 1, Kind: knowledge.KindMeaning, Head: strings.Repeat("y", 117) + "..."},
				{ID: "k-wide", Version: 1, Kind: knowledge.KindMeaning, Head: strings.Repeat("가", 117) + "..."},
			}}},
		},
		{
			name: "offers corrections of the same context and metric newest first without later approved ones",
			args: args{reviews: reviews, verdicts: verdicts, event: "tq-008"},
			want: want{offer: offer{examples: []diagnose.ExampleCandidate{
				{TraceID: "tq-007", Verdict: feedback.VerdictEdit, Head: "newer reason"},
				{TraceID: "tq-005", Verdict: feedback.VerdictReject, Head: "older reason"},
			}}},
		},
		{
			name: "offers an event its own earlier correction when it is reviewed again",
			args: args{reviews: reviews, verdicts: verdicts, event: "tq-005"},
			want: want{offer: offer{examples: []diagnose.ExampleCandidate{
				{TraceID: "tq-005", Verdict: feedback.VerdictReject, Head: "older reason"},
				{TraceID: "tq-007", Verdict: feedback.VerdictEdit, Head: "newer reason"},
			}}},
		},
		{
			name: "ranks a rejected review of the event under review ahead of newer corrections of the same metric set",
			args: args{
				traces: own, verdicts: slices.Concat([]verdict{{"own", feedback.VerdictReject, "own reason", nil}}, newer), event: "tq-008",
			},
			want: want{offer: offer{examples: slices.Concat([]diagnose.ExampleCandidate{ownRejected}, newerOffered)}},
		},
		{
			name: "ranks an edited review of the event under review ahead of newer corrections of the same metric set",
			args: args{
				traces: own, event: "tq-008",
				verdicts: slices.Concat([]verdict{{"own", feedback.VerdictEdit, "own reason", json.RawMessage(`{"status":"hold"}`)}}, newer),
			},
			want: want{offer: offer{examples: slices.Concat(
				[]diagnose.ExampleCandidate{{TraceID: "own", Verdict: feedback.VerdictEdit, Head: "own reason"}}, newerOffered,
			)}},
		},
		{
			name: "keeps newest first when the event under review has no correction of its own",
			args: args{
				traces: own, verdicts: slices.Concat([]verdict{{"own", feedback.VerdictReject, "own reason", nil}}, newer), event: "tq-013",
			},
			want: want{offer: offer{examples: slices.Concat(newerOffered, []diagnose.ExampleCandidate{ownRejected})}},
		},
		{
			name: "the candidate cap offers the ten newest corrections and marks the rest omitted",
			args: args{traces: many, verdicts: manyVerdicts, event: "tq-008"},
			want: want{offer: offer{examples: manyOffered, omitted: true}},
		},
		{
			name: "the candidate cap keeps an older correction of the same metric set ahead of newer looser ones",
			args: args{traces: ranked, verdicts: rankedVerdicts, event: "tq-008"},
			want: want{offer: offer{examples: rankedOffered, omitted: true}},
		},
		{
			name: "offers a correction of a quiet event to the next quiet event of the same context",
			args: args{
				reviews:  []string{"tq-001"},
				verdicts: []verdict{{"tq-001", feedback.VerdictReject, "expected variation", nil}},
				event:    "tq-002",
			},
			want: want{offer: offer{
				examples: []diagnose.ExampleCandidate{{TraceID: "tq-001", Verdict: feedback.VerdictReject, Head: "expected variation"}},
			}},
		},
		{
			name: "offers a quiet correction to a quiet event while the export lacks a policy metric",
			args: args{
				traces: []trace.Trace{{
					ID: "quiet", Name: trace.NameDiagnose, Subject: "tq-001", Output: json.RawMessage(`{"status":"no_action"}`),
					Input: json.RawMessage(`{"change_context":"no_known_change","metrics":[]}`),
				}},
				verdicts: []verdict{{"quiet", feedback.VerdictReject, "expected variation", nil}},
				event:    "tq-002",
				analyzers: []analysis.RuleSpec{
					{Rule: analysis.RuleZScore, Metrics: []string{"signup_count"}, Baseline: 36, Window: 12, Threshold: 3, MinSamples: 12},
				},
			},
			want: want{offer: offer{
				examples: []diagnose.ExampleCandidate{{TraceID: "quiet", Verdict: feedback.VerdictReject, Head: "expected variation"}},
			}},
		},
		{
			name: "skips a correction a session gave",
			args: args{
				reviews:         []string{"tq-005"},
				sessionVerdicts: []verdict{{"tq-005", feedback.VerdictReject, "mined from a session", nil}},
				event:           "tq-008",
			},
		},
		{
			name: "a later session approval never hides a correction a person gave",
			args: args{
				reviews:         []string{"tq-005"},
				verdicts:        []verdict{{"tq-005", feedback.VerdictReject, "person reason", nil}},
				sessionVerdicts: []verdict{{"tq-005", feedback.VerdictApprove, "", nil}},
				event:           "tq-008",
			},
			want: want{offer: offer{
				examples: []diagnose.ExampleCandidate{{TraceID: "tq-005", Verdict: feedback.VerdictReject, Head: "person reason"}},
			}},
		},
		{
			name: "skips a correction of the same context that shares no metric",
			args: args{
				reviews:  []string{"tq-001"},
				verdicts: []verdict{{"tq-001", feedback.VerdictReject, "no metric moved", nil}},
				event:    "tq-008",
			},
		},
		{
			name: "skips feedback on a missing trace and on a context trace",
			args: args{
				reviews: []string{"tq-005"},
				verdicts: []verdict{
					{"gone", feedback.VerdictReject, "orphan", nil}, {"tq-005 context", feedback.VerdictReject, "not a review", nil},
				},
				event: "tq-008",
			},
		},
		{
			name: "skips a corrected diagnose trace whose input does not decode",
			args: args{
				traces: []trace.Trace{{
					ID: "bad", Name: trace.NameDiagnose, Subject: "tq-005", Input: json.RawMessage(`[]`),
					Output: json.RawMessage(`{"status":"hold"}`),
				}},
				verdicts: []verdict{{"bad", feedback.VerdictReject, "broken record", nil}},
				event:    "tq-008",
			},
		},
	}
	ready := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{segment}}},
		Checks: diagnose.Checks{
			{Step: "confirm the signal", Purpose: "signal", ParagraphIDs: []string{confirm}},
			{Step: "check the segment", Purpose: "segment", ParagraphIDs: []string{segment}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			policy := testkit.Policy(t)
			policy.Analyzers = slices.Concat(policy.Analyzers, tc.args.analyzers)
			metrics, err := s.Source.Metrics(ctx)
			require.NoError(t, err)
			dims, err := s.Source.Dims(ctx)
			require.NoError(t, err)
			d := diagnose.New(
				s.Source, policy.Observed(metrics, knowledge.Dims(dims).Names()), nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			for _, k := range tc.args.knowledge {
				k.Evidence, k.Author = knowledge.Evidence{ParagraphIDs: []string{"p-1"}}, "author"
				_, _, err := s.Ledger.Propose(ctx, k)
				require.NoError(t, err)
				_, err = s.Ledger.Approve(ctx, k.ID, 1, "author")
				require.NoError(t, err)
			}
			ids := map[string]string{"gone": "gone"}
			for _, event := range tc.args.reviews {
				c, err := d.Prepare(ctx, event, diagnose.ModeInteractive, diagnose.Session{})
				require.NoError(t, err)
				_, err = d.Select(ctx, c.PendingID, diagnose.Choices{})
				require.NoError(t, err)
				res, err := d.Record(ctx, c.PendingID, ready)
				require.NoError(t, err)
				ids[event], ids[event+" context"] = res.TraceID, c.PendingID
			}
			for _, tr := range tc.args.traces {
				require.NoError(t, s.Traces.Append(ctx, tr))
				ids[tr.ID] = tr.ID
			}
			for _, v := range tc.args.verdicts {
				fb, err := feedback.New(ids[v.review], v.verdict, v.reason, v.edited, "", s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Feedback.Append(ctx, fb))
			}
			for _, v := range tc.args.sessionVerdicts {
				fb, err := feedback.New(ids[v.review], v.verdict, v.reason, v.edited, feedback.ReviewerSession, s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Feedback.Append(ctx, fb))
			}
			wantOffer := tc.want.offer
			wantOffer.examples = nil
			for _, e := range tc.want.offer.examples {
				e.TraceID = ids[e.TraceID]
				wantOffer.examples = append(wantOffer.examples, e)
			}

			got, err := d.Prepare(ctx, tc.args.event, diagnose.ModeInteractive, diagnose.Session{})
			assert.ErrorIs(t, err, tc.want.err)

			assert.Equal(
				t, wantOffer,
				offer{knowledge: got.KnowledgeCandidates, examples: got.ExampleCandidates, omitted: got.CandidatesOmitted},
			)
		})
	}
}

// Under a zscore only policy tq-005 moves click_count on source-a alone while every source stays in the event
func TestPrepareKnowledgeScopedToMovedSeries(t *testing.T) {
	policy, err := analysis.LoadPolicy([]byte("version: zscore-1\nanalyzers:\n" +
		"  - rule: zscore\n    metrics: [click_count]\n    baseline: 36\n    window: 12\n    threshold: 3\n    min_samples: 12\n"))
	require.NoError(t, err)
	clicksOn := func(source string) knowledge.Scope {
		return knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"click_count"}}, Dims: map[string]string{"source": source}}
	}
	// The demo policy adds concentration_change whose source-b share on tq-005 falls only because source-a grew
	demo := testkit.Policy(t)
	// ev-1 of testdata diluted moves only source-d whose share rose because the other sources fell
	diluted, err := analysis.LoadPolicy([]byte("version: diluted-1\nanalyzers:\n" +
		"  - rule: zscore\n    metrics: [click_count]\n    baseline: 36\n    window: 12\n    threshold: 3\n    min_samples: 12\n" +
		"  - rule: concentration_change\n    metrics: [click_count]\n    group_by: source\n    baseline: 36\n    window: 12\n" +
		"    threshold: 0.15\n    min_samples: 12\n"))
	require.NoError(t, err)
	demoDir, dilutedDir := testkit.DemoDir(t), filepath.Join("testdata", "diluted")
	clicks := knowledge.Scope{Scope: evidence.Scope{
		ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}, Metrics: []string{"click_count"},
	}}
	type args struct {
		policy analysis.Policy
		scope  knowledge.Scope
		dir    string
		event  string
	}
	tcs := []struct {
		name string
		args args
		want []string
	}{
		{"offers an item scoped to the source that moved", args{policy, clicksOn("source-a"), demoDir, "tq-005"}, []string{"k-scoped"}},
		{
			"skips an item scoped to a source the event carries that never moved",
			args{policy, clicksOn("source-b"), demoDir, "tq-005"},
			nil,
		},
		{
			"offers an item scoped to a carried source without a metric",
			args{policy, knowledge.Scope{Dims: map[string]string{"source": "source-b"}}, demoDir, "tq-005"},
			[]string{"k-scoped"},
		},
		{
			"offers an item scoped to the source whose share grew on its own",
			args{demo, clicksOn("source-a"), demoDir, "tq-005"},
			[]string{"k-scoped"},
		},
		{
			"skips an item scoped to the source whose share only the other sources moved",
			args{demo, clicksOn("source-b"), demoDir, "tq-005"},
			nil,
		},
		{
			"offers an item scoped to a diluted source without a metric",
			args{demo, knowledge.Scope{Dims: map[string]string{"source": "source-b"}}, demoDir, "tq-005"},
			[]string{"k-scoped"},
		},
		{
			"offers a metric scope on an event whose only moved group is diluted",
			args{diluted, clicks, dilutedDir, "ev-1"},
			[]string{"k-scoped"},
		},
		{
			"skips a scope on the diluted group of an event whose only moved group is diluted",
			args{diluted, clicksOn("source-d"), dilutedDir, "ev-1"},
			nil,
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			src, err := evidencefile.New(tc.args.dir)
			require.NoError(t, err)
			d := diagnose.New(src, tc.args.policy, nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)
			k := knowledge.Knowledge{
				ID: "k-scoped", Kind: knowledge.KindMeaning, Content: "a source rule", Scope: tc.args.scope,
				Evidence: knowledge.Evidence{ParagraphIDs: []string{"p-1"}}, Author: "author",
			}
			_, _, err = s.Ledger.Propose(ctx, k)
			require.NoError(t, err)
			_, err = s.Ledger.Approve(ctx, k.ID, 1, "author")
			require.NoError(t, err)

			got, err := d.Prepare(ctx, tc.args.event, diagnose.ModeInteractive, diagnose.Session{})
			require.NoError(t, err)
			var ids []string
			for _, c := range got.KnowledgeCandidates {
				ids = append(ids, c.ID)
			}

			assert.Equal(t, tc.want, ids)
		})
	}
}

func TestSelect(t *testing.T) {
	const (
		confirm = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
		segment = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
	)
	type verdict struct {
		review  string
		verdict feedback.Verdict
		reason  string
		edited  json.RawMessage
	}
	type args struct {
		reviews []string
		// Knowledge ids retired after the context was built
		retired  []string
		verdicts []verdict
		// Verdicts given after the context was built
		later []verdict
		// Verdicts a session gave after the context was built
		laterSession []verdict
		// Reviews recorded on the context before the select under test
		records []diagnose.Diagnosis
		pending string
		choices diagnose.Choices
	}
	type example struct {
		TraceID      string `json:"trace_id"`
		Reason       string `json:"reason"`
		Chars        int    `json:"chars"`
		OmittedChars int    `json:"omitted_chars"`
		Cut          bool   `json:"cut"`
	}
	type sections struct {
		Knowledge int `json:"knowledge"`
		Examples  int `json:"examples"`
	}
	type input struct {
		SessionID    string
		Subject      string
		Selector     diagnose.Selector           `json:"mode"`
		Knowledge    []diagnose.AppliedKnowledge `json:"knowledge"`
		Examples     []example                   `json:"examples"`
		Omitted      bool                        `json:"omitted"`
		Chars        sections                    `json:"chars"`
		OmittedChars sections                    `json:"omitted_chars"`
	}
	type selected struct {
		applied []diagnose.AppliedKnowledge
		omitted bool
		// Select trace inputs newest first
		inputs []input
	}
	type want struct {
		selected selected
		// Text the selection must and must not carry
		present []string
		absent  []string
		err     error
	}
	reviews := []string{"tq-005", "tq-007", "tq-009"}
	verdicts := []verdict{
		{"tq-005", feedback.VerdictReject, "older reason", nil},
		{"tq-007", feedback.VerdictEdit, "newer reason", json.RawMessage(`{"status":"hold"}`)},
		{"tq-009", feedback.VerdictReject, "other context", nil},
	}
	// A correction that leaves the original review less than the example cap
	longest := `{"status":"no_action","observations":["` + strings.Repeat("lag ", 1550) + `"]}`
	longEdit := []verdict{
		{"tq-005", feedback.VerdictReject, "older reason", nil},
		{"tq-007", feedback.VerdictEdit, "newer reason", json.RawMessage(longest)},
	}
	// What the edits of tq-007 changed in the recorded ready review
	changed := func(status string) string {
		return "Original status: ready_for_review with 1 cause\nCorrected status: " + status + " with no causes\n" +
			"Cause paragraphs removed: " + segment + "\n"
	}
	handEdit := []verdict{{"tq-007", feedback.VerdictEdit, "newer reason", json.RawMessage(`"rewritten by hand"`)}}
	// An edit citing a paragraph the procedures no longer hold beside a listed one
	goneEdit := []verdict{{"tq-007", feedback.VerdictEdit, "newer reason", json.RawMessage(
		`{"status":"ready_for_review","causes":[{"summary":"s","paragraph_ids":["gone#1","` + segment + `"]}]}`,
	)}}
	aggregation := diagnose.AppliedKnowledge{
		ID:      "k-agg",
		Version: 1,
		Reason:  "conversion rate is part of the picture",
		Chars:   112,
	}
	none := []diagnose.AppliedKnowledge{}
	empty := input{Selector: diagnose.SelectByClaude, Knowledge: none, Examples: []example{}}
	ready := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{segment}}},
		Checks: diagnose.Checks{
			{Step: "confirm the signal", Purpose: "signal", ParagraphIDs: []string{confirm}},
			{Step: "check the segment", Purpose: "segment", ParagraphIDs: []string{segment}},
		},
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "returns chosen knowledge with its reason",
			args: args{
				pending: "context",
				choices: diagnose.Choices{Knowledge: []diagnose.Choice{{ID: "k-agg", Reason: aggregation.Reason}}},
			},
			want: want{
				selected: selected{
					applied: []diagnose.AppliedKnowledge{aggregation},
					inputs: []input{{
						Selector: diagnose.SelectByClaude, Knowledge: []diagnose.AppliedKnowledge{aggregation}, Examples: []example{},
						Chars: sections{Knowledge: knowledgeHeading + 112},
					}},
				},
				present: []string{
					"## Approved knowledge",
					"[k-agg v1 meaning] clicks and conversions use different aggregation time bases\nScope: ",
				},
				absent: []string{"## Examples", "omitted"},
			},
		},
		{
			name: "counts a repeated knowledge id once",
			args: args{
				pending: "context",
				choices: diagnose.Choices{
					Knowledge: []diagnose.Choice{{ID: "k-agg", Reason: aggregation.Reason}, {ID: "k-agg", Reason: "again"}},
				},
			},
			want: want{
				selected: selected{
					applied: []diagnose.AppliedKnowledge{aggregation},
					inputs: []input{{
						Selector: diagnose.SelectByClaude, Knowledge: []diagnose.AppliedKnowledge{aggregation}, Examples: []example{},
						Chars: sections{Knowledge: knowledgeHeading + 112},
					}},
				},
				present: []string{"## Approved knowledge"},
				absent:  []string{"again"},
			},
		},
		{
			name: "refuses a select after the context is recorded",
			args: args{records: []diagnose.Diagnosis{ready}, pending: "context"},
			want: want{selected: selected{inputs: []input{empty}}, err: diagnose.ErrRecorded},
		},
		{
			name: "records an empty choice",
			args: args{pending: "context"},
			want: want{selected: selected{applied: none, inputs: []input{empty}}, absent: []string{"##"}},
		},
		{
			name: "renders the chosen example with its correction",
			args: args{
				reviews: reviews, verdicts: verdicts, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-007", Reason: "same shape"}}},
			},
			want: want{
				selected: selected{applied: none, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none,
					Examples: []example{{TraceID: "tq-007", Reason: "same shape", Chars: 807}},
					Chars:    sections{Examples: examplesHeading + 807},
				}}},
				present: []string{
					"## Examples",
					"### Example 1",
					"Verdict: edit\nReason: newer reason\n" + changed("hold") + "Corrected: {\"status\":\"hold\"}\n",
				},
				absent: []string{"older reason", "## Approved knowledge"},
			},
		},
		{
			name: "names the paragraphs an edited example cites that the context does not list",
			args: args{
				reviews: reviews, verdicts: goneEdit, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-007"}}},
			},
			want: want{
				selected: selected{applied: none, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none,
					Examples: []example{{TraceID: "tq-007", Chars: 1016}},
					Chars:    sections{Examples: examplesHeading + 1016},
				}}},
				present: []string{
					"Cause paragraphs added: gone#1\nParagraphs this review does not list: gone#1. " +
						"Follow the correction through the listed paragraph that states the same finding\nCorrected: ",
				},
			},
		},
		{
			name: "marks the original review of a rejected example as wrong",
			args: args{
				reviews: reviews, verdicts: verdicts, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-005"}}},
			},
			want: want{
				selected: selected{applied: none, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none,
					Examples: []example{{TraceID: "tq-005", Chars: 682}},
					Chars:    sections{Examples: examplesHeading + 682},
				}}},
				present: []string{
					"Verdict: reject\nReason: older reason\n" +
						"Original status: ready_for_review with 1 cause. The reviewer rejected this review as wrong\nOriginal review: {",
				},
				absent: []string{"Corrected"},
			},
		},
		{
			name: "renders an example approved after the context was built by its approval",
			args: args{
				reviews: reviews, verdicts: verdicts, pending: "context",
				later:   []verdict{{"tq-005", feedback.VerdictApprove, "right after all", nil}},
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-005"}}},
			},
			want: want{
				selected: selected{applied: none, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none,
					Examples: []example{{TraceID: "tq-005", Chars: 595}},
					Chars:    sections{Examples: examplesHeading + 595},
				}}},
				present: []string{"Verdict: approve\nReason: right after all\nOriginal review: {"},
				absent:  []string{"rejected", "Original status", "Corrected"},
			},
		},
		{
			name: "renders an example a session approved after the context was built by the verdict a person gave",
			args: args{
				reviews: reviews, verdicts: verdicts, pending: "context",
				laterSession: []verdict{{"tq-005", feedback.VerdictApprove, "mined approval", nil}},
				choices:      diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-005"}}},
			},
			want: want{
				selected: selected{applied: none, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none,
					Examples: []example{{TraceID: "tq-005", Chars: 682}},
					Chars:    sections{Examples: examplesHeading + 682},
				}}},
				present: []string{"Verdict: reject\nReason: older reason\n"},
				absent:  []string{"mined approval"},
			},
		},
		{
			name: "renders an edited review that is no review without the change",
			args: args{
				reviews: reviews, verdicts: handEdit, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-007"}}},
			},
			want: want{
				selected: selected{applied: none, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none,
					Examples: []example{{TraceID: "tq-007", Chars: 620}},
					Chars:    sections{Examples: examplesHeading + 620},
				}}},
				present: []string{"Verdict: edit\nReason: newer reason\nCorrected: \"rewritten by hand\"\n"},
				absent:  []string{"Original status"},
			},
		},
		{
			name: "counts a repeated example id once",
			args: args{
				reviews: reviews, verdicts: verdicts, pending: "context",
				choices: diagnose.Choices{
					Examples: []diagnose.Choice{{ID: "tq-007", Reason: "same shape"}, {ID: "tq-007", Reason: "again"}},
				},
			},
			want: want{
				selected: selected{applied: none, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none,
					Examples: []example{{TraceID: "tq-007", Reason: "same shape", Chars: 807}},
					Chars:    sections{Examples: examplesHeading + 807},
				}}},
				present: []string{"### Example 1"},
				absent:  []string{"### Example 2"},
			},
		},
		{
			name: "cuts the original review first and keeps the verdict, reason and corrected review",
			args: args{
				reviews: reviews, verdicts: longEdit, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-007"}}},
			},
			want: want{
				selected: selected{applied: none, omitted: true, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none, Omitted: true,
					Examples: []example{{TraceID: "tq-007", Chars: 6999, OmittedChars: 74, Cut: true}},
					Chars:    sections{Examples: examplesHeading + 6999 + examplesNotice}, OmittedChars: sections{Examples: 74},
				}}},
				present: []string{
					"\n### Example 1\n\nVerdict: edit\nReason: newer reason\n" + changed("no_action") + "Corrected: " + longest +
						"\nOriginal review: ",
					"\n[74 characters omitted at the cap]\n", "The original review gives way first",
				},
			},
		},
		{
			name: "refuses knowledge outside the offer",
			args: args{
				pending: "context",
				choices: diagnose.Choices{Knowledge: []diagnose.Choice{{ID: "k-other", Reason: "x"}}},
			},
			want: want{err: diagnose.ErrNotOffered},
		},
		{
			name: "refuses an example outside the offer",
			args: args{
				reviews:  reviews,
				verdicts: verdicts,
				pending:  "context",
				choices:  diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-009"}}},
			},
			want: want{err: diagnose.ErrNotOffered},
		},
		{
			name: "refuses knowledge retired after the context was built",
			args: args{
				retired: []string{"k-agg"},
				pending: "context",
				choices: diagnose.Choices{Knowledge: []diagnose.Choice{{ID: "k-agg"}}},
			},
			want: want{err: knowledge.ErrVersionUnapproved},
		},
		{
			name: "fails on an unknown pending id",
			args: args{pending: "no-such-id"},
			want: want{err: trace.ErrNotFound},
		},
		{
			name: "refuses a context the batch path left open",
			args: args{pending: "batch", choices: diagnose.Choices{Knowledge: []diagnose.Choice{{ID: "k-agg"}}}},
			want: want{err: diagnose.ErrBatchContext},
		},
	}
	// Left open by an interrupted batch run
	batch := trace.Trace{
		ID: "batch", Name: trace.NameContext, SessionID: "s1", Subject: "tq-008", Tags: []string{"feedback:off"},
		Input: json.RawMessage(`{"mode":"batch"}`), Output: json.RawMessage(`{"knowledge_candidates":[{"id":"k-agg","version":1}]}`),
	}
	drafts := []knowledge.Knowledge{
		{
			ID: "k-agg", Kind: knowledge.KindMeaning, Content: "clicks and conversions use different aggregation time bases",
			Scope: knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}},
		},
		{
			ID: "k-other", Kind: knowledge.KindJudgment, Content: "only for planned changes",
			Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}}},
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
			for _, k := range drafts {
				k.Evidence, k.Author = knowledge.Evidence{ParagraphIDs: []string{"p-1"}}, "author"
				_, _, err := s.Ledger.Propose(ctx, k)
				require.NoError(t, err)
				_, err = s.Ledger.Approve(ctx, k.ID, 1, "author")
				require.NoError(t, err)
			}
			ids := map[string]string{"no-such-id": "no-such-id", "k-agg": "k-agg", "k-other": "k-other", "batch": batch.ID}
			require.NoError(t, s.Traces.Append(ctx, batch))
			for _, event := range tc.args.reviews {
				c, err := d.Prepare(ctx, event, diagnose.ModeInteractive, diagnose.Session{})
				require.NoError(t, err)
				_, err = d.Select(ctx, c.PendingID, diagnose.Choices{})
				require.NoError(t, err)
				res, err := d.Record(ctx, c.PendingID, ready)
				require.NoError(t, err)
				ids[event] = res.TraceID
			}
			for _, v := range tc.args.verdicts {
				fb, err := feedback.New(ids[v.review], v.verdict, v.reason, v.edited, "", s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Feedback.Append(ctx, fb))
			}
			c, err := d.Prepare(ctx, "tq-008", diagnose.ModeInteractive, diagnose.Session{ID: "s1"})
			require.NoError(t, err)
			ids["context"] = c.PendingID
			for _, v := range tc.args.later {
				fb, err := feedback.New(ids[v.review], v.verdict, v.reason, v.edited, "", s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Feedback.Append(ctx, fb))
			}
			for _, v := range tc.args.laterSession {
				fb, err := feedback.New(ids[v.review], v.verdict, v.reason, v.edited, feedback.ReviewerSession, s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Feedback.Append(ctx, fb))
			}
			for _, id := range tc.args.retired {
				_, err := s.Ledger.Retire(ctx, id, 1, "author")
				require.NoError(t, err)
			}
			for _, diag := range tc.args.records {
				_, err := d.Select(ctx, c.PendingID, diagnose.Choices{})
				require.NoError(t, err)
				_, err = d.Record(ctx, c.PendingID, diag)
				require.NoError(t, err)
			}
			choices := diagnose.Choices{Knowledge: tc.args.choices.Knowledge}
			for _, e := range tc.args.choices.Examples {
				choices.Examples = append(choices.Examples, diagnose.Choice{ID: ids[e.ID], Reason: e.Reason})
			}
			wantSelected := tc.want.selected
			wantSelected.inputs = nil
			for _, in := range tc.want.selected.inputs {
				examples := []example{}
				for _, e := range in.Examples {
					e.TraceID = ids[e.TraceID]
					examples = append(examples, e)
				}
				in.SessionID, in.Subject, in.Examples = "s1", "tq-008", examples
				wantSelected.inputs = append(wantSelected.inputs, in)
			}

			got, err := d.Select(ctx, ids[tc.args.pending], choices)
			assert.ErrorIs(t, err, tc.want.err)
			selects, err := s.Traces.List(ctx, trace.Filter{Name: trace.NameSelect, Ref: c.PendingID})
			require.NoError(t, err)
			var recorded []input
			for _, tr := range selects {
				in := input{SessionID: tr.SessionID, Subject: tr.Subject}
				require.NoError(t, json.Unmarshal(tr.Input, &in))
				recorded = append(recorded, in)
			}

			assert.Equal(t, wantSelected, selected{applied: got.Applied, omitted: got.Omitted, inputs: recorded})
			for _, text := range tc.want.present {
				assert.Contains(t, got.Text, text)
			}
			for _, text := range tc.want.absent {
				assert.NotContains(t, got.Text, text)
			}
		})
	}
}

// The prompt is the subject here so the model call is matched on its context and the prompt is read back
func TestRunSelection(t *testing.T) {
	const (
		confirm = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
		segment = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
	)
	type args struct {
		mode     diagnose.KnowledgeMode
		examples int
		exclude  []string
		// Empty means tq-008
		event string
	}
	type want struct {
		// Text the prompt must and must not carry
		present []string
		absent  []string
		used    []knowledge.Ref
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "empty mode gives no knowledge and no examples",
			want: want{absent: []string{"## Approved knowledge", "## Examples"}},
		},
		{
			name: "none mode gives no knowledge",
			args: args{mode: diagnose.KnowledgeNone},
			want: want{absent: []string{"## Approved knowledge"}},
		},
		{
			name: "selected mode takes the offered items in scope",
			args: args{mode: diagnose.KnowledgeSelected},
			want: want{
				present: []string{"aggregation time bases differ"},
				absent:  []string{"only for planned changes"},
				used:    []knowledge.Ref{{ID: "k-agg", Version: 1}},
			},
		},
		{
			name: "all mode takes every approved item regardless of scope",
			args: args{mode: diagnose.KnowledgeAll},
			want: want{
				present: []string{"aggregation time bases differ", "only for planned changes"},
				used:    []knowledge.Ref{{ID: "k-agg", Version: 1}, {ID: "k-planned", Version: 1}},
			},
		},
		{
			name: "examples follow the candidate order up to the count",
			args: args{examples: 1},
			want: want{present: []string{"## Examples", "newer reason"}, absent: []string{"older reason"}},
		},
		{
			name: "reviews of an excluded event are never examples",
			args: args{examples: 1, exclude: []string{"tq-007"}},
			want: want{present: []string{"## Examples", "older reason"}, absent: []string{"newer reason"}},
		},
		{
			name: "the code pick carries the correction of the event under review ahead of a newer one",
			args: args{examples: 1, event: "tq-005"},
			want: want{present: []string{"## Examples", "older reason"}, absent: []string{"newer reason"}},
		},
		{
			name: "negative example count gives no examples",
			args: args{examples: -1},
			want: want{absent: []string{"## Examples"}},
		},
	}
	drafts := []knowledge.Knowledge{
		{
			ID:      "k-agg",
			Kind:    knowledge.KindMeaning,
			Content: "aggregation time bases differ",
			Scope:   knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}},
		},
		{
			ID: "k-planned", Kind: knowledge.KindJudgment, Content: "only for planned changes",
			Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}}},
		},
	}
	ready := diagnose.Diagnosis{
		Status: evidence.StatusReadyForReview,
		Causes: []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: []string{segment}}},
		Checks: diagnose.Checks{
			{Step: "confirm the signal", Purpose: "signal", ParagraphIDs: []string{confirm}},
			{Step: "check the segment", Purpose: "segment", ParagraphIDs: []string{segment}},
		},
	}
	verdicts := []struct {
		review  string
		verdict feedback.Verdict
		reason  string
		edited  json.RawMessage
	}{
		{"tq-005", feedback.VerdictReject, "older reason", nil},
		{"tq-007", feedback.VerdictEdit, "newer reason", json.RawMessage(`{"status":"hold"}`)},
	}
	held, err := json.Marshal(diagnose.Diagnosis{Status: evidence.StatusHold, HoldReasons: []string{"x"}})
	require.NoError(t, err)
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			d := diagnose.New(
				s.Source, testkit.Policy(t), client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			for _, k := range drafts {
				k.Evidence, k.Author = knowledge.Evidence{ParagraphIDs: []string{"p-1"}}, "author"
				_, _, err := s.Ledger.Propose(ctx, k)
				require.NoError(t, err)
				_, err = s.Ledger.Approve(ctx, k.ID, 1, "author")
				require.NoError(t, err)
			}
			examples := map[string]string{}
			for _, event := range []string{"tq-005", "tq-007"} {
				c, err := d.Prepare(ctx, event, diagnose.ModeInteractive, diagnose.Session{})
				require.NoError(t, err)
				_, err = d.Select(ctx, c.PendingID, diagnose.Choices{})
				require.NoError(t, err)
				res, err := d.Record(ctx, c.PendingID, ready)
				require.NoError(t, err)
				examples[event] = res.TraceID
			}
			for _, v := range verdicts {
				fb, err := feedback.New(examples[v.review], v.verdict, v.reason, v.edited, "", s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Feedback.Append(ctx, fb))
			}
			var prompt string
			client.EXPECT().
				Complete(ctx, gomock.Any()).
				DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
					prompt = req.Prompt
					return llm.Response{Output: held}, nil
				})

			got, err := d.Run(
				ctx, cmp.Or(tc.args.event, "tq-008"), diagnose.BatchOptions{Knowledge: tc.args.mode, Examples: tc.args.examples, Exclude: tc.args.exclude},
			)
			require.NoError(t, err)
			tr, err := s.Traces.Get(ctx, got.TraceID)
			require.NoError(t, err)

			for _, text := range tc.want.present {
				assert.Contains(t, prompt, text)
			}
			for _, text := range tc.want.absent {
				assert.NotContains(t, prompt, text)
			}
			assert.Equal(t, tc.want.used, diagnose.KnowledgeApplied(tr.Input))
		})
	}
}
