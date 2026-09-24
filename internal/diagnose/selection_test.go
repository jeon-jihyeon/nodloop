package diagnose_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
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
		limits    diagnose.Limits
		knowledge []knowledge.Knowledge
		reviews   []string
		// Appended as they are so a verdict can name them by id
		traces   []trace.Trace
		verdicts []verdict
		event    string
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
	aggregation := "clicks and conversions use different aggregation time bases"
	conversions := knowledge.Scope{Metrics: []string{"conversion_count"}}
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
						Scope: knowledge.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}},
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
			name: "marks knowledge over the candidate cap as omitted",
			args: args{
				limits: diagnose.Limits{Candidates: 1},
				knowledge: []knowledge.Knowledge{
					{ID: "k-a", Kind: knowledge.KindMeaning, Content: "first"},
					{ID: "k-b", Kind: knowledge.KindMeaning, Content: "second"},
				},
				event: "tq-005",
			},
			want: want{offer: offer{
				knowledge: []diagnose.KnowledgeCandidate{{ID: "k-a", Version: 1, Kind: knowledge.KindMeaning, Head: "first"}},
				omitted:   true,
			}},
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
			name: "never offers an event its own review",
			args: args{reviews: reviews, verdicts: verdicts, event: "tq-005"},
			want: want{offer: offer{
				examples: []diagnose.ExampleCandidate{{TraceID: "tq-007", Verdict: feedback.VerdictEdit, Head: "newer reason"}},
			}},
		},
		{
			name: "policy candidate cap limits corrections and marks the rest omitted",
			args: args{limits: diagnose.Limits{Candidates: 1}, reviews: reviews, verdicts: verdicts, event: "tq-008"},
			want: want{offer: offer{
				examples: []diagnose.ExampleCandidate{{TraceID: "tq-007", Verdict: feedback.VerdictEdit, Head: "newer reason"}},
				omitted:  true,
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
		Status: diagnose.StatusReadyForReview,
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
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), tc.args.limits, nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
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
				c, err := d.Prepare(ctx, event, diagnose.ModeBatch, diagnose.Session{})
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
		limits  diagnose.Limits
		reviews []string
		// Knowledge ids retired after the context was built
		retired  []string
		verdicts []verdict
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
	type input struct {
		SessionID string
		Subject   string
		Selector  diagnose.Selector           `json:"mode"`
		Knowledge []diagnose.AppliedKnowledge `json:"knowledge"`
		Examples  []example                   `json:"examples"`
		Omitted   bool                        `json:"omitted"`
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
	// One correction outgrows an even share of the example cap
	three := []string{"tq-005", "tq-007", "tq-010"}
	long := `{"status":"no_action","observations":["` + strings.Repeat("lag ", 80) + `"]}`
	uneven := []verdict{
		{"tq-005", feedback.VerdictEdit, "short correction", json.RawMessage(`{"status":"hold"}`)},
		{"tq-007", feedback.VerdictEdit, "short correction", json.RawMessage(`{"status":"hold"}`)},
		{"tq-010", feedback.VerdictEdit, "long correction", json.RawMessage(long)},
	}
	twoLong := []verdict{
		{"tq-005", feedback.VerdictEdit, "short correction", json.RawMessage(`{"status":"hold"}`)},
		{"tq-007", feedback.VerdictEdit, "long correction", json.RawMessage(long)},
		{"tq-010", feedback.VerdictEdit, "long correction", json.RawMessage(long)},
	}
	aggregation := diagnose.AppliedKnowledge{
		ID:      "k-agg",
		Version: 1,
		Reason:  "conversion rate is part of the picture",
		Chars:   112,
	}
	none := []diagnose.AppliedKnowledge{}
	empty := input{Selector: diagnose.SelectByClaude, Knowledge: none, Examples: []example{}}
	ready := diagnose.Diagnosis{
		Status: diagnose.StatusReadyForReview,
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
					Examples: []example{{TraceID: "tq-007", Reason: "same shape", Chars: 618}},
				}}},
				present: []string{
					"## Examples",
					"### Example 1",
					"Verdict: edit\nReason: newer reason\nCorrected: {\"status\":\"hold\"}\n",
				},
				absent: []string{"older reason", "## Approved knowledge"},
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
					Examples: []example{{TraceID: "tq-007", Reason: "same shape", Chars: 618}},
				}}},
				present: []string{"### Example 1"},
				absent:  []string{"### Example 2"},
			},
		},
		{
			name: "cuts the original review first and keeps the verdict, reason and corrected review",
			args: args{
				limits: diagnose.Limits{ExampleChars: 150}, reviews: reviews, verdicts: verdicts, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-007"}}},
			},
			want: want{
				selected: selected{applied: none, omitted: true, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none, Omitted: true,
					Examples: []example{{TraceID: "tq-007", Chars: 150, OmittedChars: 505, Cut: true}},
				}}},
				present: []string{
					"\n### Example 1\n\nVerdict: edit\nReason: newer reason\nCorrected: {\"status\":\"hold\"}\nOriginal review: ",
					"\n[505 characters omitted at the cap]\n", "The original review gives way first",
				},
			},
		},
		{
			name: "leaves out an original review whose room cannot hold the cut mark without a mark",
			args: args{
				limits: diagnose.Limits{ExampleChars: 100}, reviews: reviews, verdicts: verdicts, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-007"}}},
			},
			want: want{
				selected: selected{applied: none, omitted: true, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none, Omitted: true,
					Examples: []example{{TraceID: "tq-007", Chars: 80, OmittedChars: 538, Cut: true}},
				}}},
				present: []string{
					"\n### Example 1\n\nVerdict: edit\nReason: newer reason\n" +
						"Corrected: {\"status\":\"hold\"}\n\nSome examples were cut",
				},
				absent: []string{"Original review:", "characters omitted at the cap]"},
			},
		},
		{
			name: "leaves out an example whose share cannot hold its verdict, reason and the cut mark",
			args: args{
				limits: diagnose.Limits{ExampleChars: 60}, reviews: reviews, verdicts: verdicts, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-007"}}},
			},
			want: want{
				selected: selected{applied: none, omitted: true, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none, Omitted: true,
					Examples: []example{{TraceID: "tq-007", OmittedChars: 618, Cut: true}},
				}}},
				present: []string{"Some examples were cut to fit the example cap"},
				absent:  []string{"### Example", "newer reason"},
			},
		},
		{
			name: "leaves out an example whose share cannot hold the cut mark and records it whole as omitted",
			args: args{
				limits: diagnose.Limits{ExampleChars: 10}, reviews: reviews, verdicts: verdicts, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-007"}}},
			},
			want: want{
				selected: selected{applied: none, omitted: true, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none, Omitted: true,
					Examples: []example{{TraceID: "tq-007", OmittedChars: 618, Cut: true}},
				}}},
				present: []string{"Some examples were cut to fit the example cap"},
				absent:  []string{"### Example"},
			},
		},
		{
			name: "original reviews of equal length split evenly what the leads and corrections leave",
			args: args{
				limits: diagnose.Limits{ExampleChars: 400}, reviews: reviews, verdicts: verdicts, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-007"}, {ID: "tq-005"}}},
			},
			want: want{
				selected: selected{applied: none, omitted: true, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none, Omitted: true,
					Examples: []example{
						{
							TraceID:      "tq-007",
							Chars:        213,
							OmittedChars: 442,
							Cut:          true,
						}, {TraceID: "tq-005", Chars: 187, OmittedChars: 441, Cut: true},
					},
				}}},
				present: []string{
					"### Example 1", "Corrected: {\"status\":\"hold\"}\nOriginal review: ", "### Example 2",
					"Reason: older reason\nOriginal review: ", "Some examples were cut to fit the example cap",
				},
			},
		},
		{
			name: "a correction larger than an even share arrives whole when the leads and corrections fit together",
			args: args{
				limits: diagnose.Limits{ExampleChars: 600}, reviews: three, verdicts: uneven, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-010"}, {ID: "tq-005"}, {ID: "tq-007"}}},
			},
			want: want{
				selected: selected{applied: none, omitted: true, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none, Omitted: true,
					Examples: []example{
						{TraceID: "tq-010", Chars: 428, OmittedChars: 538, Cut: true},
						{TraceID: "tq-005", Chars: 84, OmittedChars: 538, Cut: true},
						{TraceID: "tq-007", Chars: 84, OmittedChars: 538, Cut: true},
					},
				}}},
				present: []string{
					"Reason: long correction\nCorrected: " + long + "\n\n### Example 2",
					"Some examples were cut to fit the example cap",
				},
				absent: []string{"Original review:", "characters omitted at the cap]"},
			},
		},
		{
			name: "corrections that outgrow the cap together are cut evenly and marked while a short correction stays whole",
			args: args{
				limits: diagnose.Limits{ExampleChars: 400}, reviews: three, verdicts: twoLong, pending: "context",
				choices: diagnose.Choices{Examples: []diagnose.Choice{{ID: "tq-010"}, {ID: "tq-005"}, {ID: "tq-007"}}},
			},
			want: want{
				selected: selected{applied: none, omitted: true, inputs: []input{{
					Selector: diagnose.SelectByClaude, Knowledge: none, Omitted: true,
					Examples: []example{
						{TraceID: "tq-010", Chars: 158, OmittedChars: 845, Cut: true},
						{TraceID: "tq-005", Chars: 84, OmittedChars: 538, Cut: true},
						{TraceID: "tq-007", Chars: 158, OmittedChars: 845, Cut: true},
					},
				}}},
				present: []string{
					"Reason: long correction\nCorrected: {\"status\":\"no_action\"",
					"lag lag lag lag l\n[845 characters omitted at the cap]\n\n### Example 2",
					"Reason: short correction\nCorrected: {\"status\":\"hold\"}\n\n### Example 3",
					"Some examples were cut to fit the example cap",
				},
				absent: []string{"Original review:"},
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
	}
	drafts := []knowledge.Knowledge{
		{
			ID: "k-agg", Kind: knowledge.KindMeaning, Content: "clicks and conversions use different aggregation time bases",
			Scope: knowledge.Scope{Metrics: []string{"conversion_count"}},
		},
		{
			ID: "k-other", Kind: knowledge.KindJudgment, Content: "only for planned changes",
			Scope: knowledge.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), tc.args.limits, nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			for _, k := range drafts {
				k.Evidence, k.Author = knowledge.Evidence{ParagraphIDs: []string{"p-1"}}, "author"
				_, _, err := s.Ledger.Propose(ctx, k)
				require.NoError(t, err)
				_, err = s.Ledger.Approve(ctx, k.ID, 1, "author")
				require.NoError(t, err)
			}
			ids := map[string]string{"no-such-id": "no-such-id", "k-agg": "k-agg", "k-other": "k-other"}
			for _, event := range tc.args.reviews {
				c, err := d.Prepare(ctx, event, diagnose.ModeBatch, diagnose.Session{})
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
	}
	type want struct {
		// Text the prompt must and must not carry
		present []string
		absent  []string
		used    []string
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
				used:    []string{"k-agg"},
			},
		},
		{
			name: "all mode takes every approved item regardless of scope",
			args: args{mode: diagnose.KnowledgeAll},
			want: want{
				present: []string{"aggregation time bases differ", "only for planned changes"},
				used:    []string{"k-agg", "k-planned"},
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
			Scope:   knowledge.Scope{Metrics: []string{"conversion_count"}},
		},
		{
			ID: "k-planned", Kind: knowledge.KindJudgment, Content: "only for planned changes",
			Scope: knowledge.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}},
		},
	}
	ready := diagnose.Diagnosis{
		Status: diagnose.StatusReadyForReview,
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
	held, err := json.Marshal(diagnose.Diagnosis{Status: diagnose.StatusHold, HoldReasons: []string{"x"}})
	require.NoError(t, err)
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), diagnose.Limits{}, client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
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
				ctx, "tq-008", diagnose.BatchOptions{Knowledge: tc.args.mode, Examples: tc.args.examples, Exclude: tc.args.exclude},
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
