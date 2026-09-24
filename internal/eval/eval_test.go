package eval_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/eval"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Every trace is a diagnose or a revise trace of session s
func TestReport(t *testing.T) {
	const (
		na         = -1
		base       = eval.ConditionBaseline
		segment    = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
		downstream = "metric-anomaly-investigation#Metric anomaly investigation/Check downstream outcomes#1"
		confirm    = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
		coverage   = "data-integrity-hold#Data integrity hold/Coverage#1"
	)
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	off := []string{string(base)}
	checks := `"checks":[{"step":"s","purpose":"p","paragraph_ids":["` + confirm + `"]}]`
	ready := json.RawMessage(`{"status":"ready_for_review",` +
		`"causes":[{"summary":"x","paragraph_ids":["` + segment + `","z"]}],` + checks + `}`)
	edited := json.RawMessage(`{"status":"ready_for_review",` +
		`"causes":[{"summary":"y","paragraph_ids":["` + segment + `"]}],` + checks + `,"open_questions":[]}`)
	noAction := json.RawMessage(`{"status":"no_action"}`)
	readyBare := json.RawMessage(`{"status":"ready_for_review"}`)
	hold := json.RawMessage(`{"status":"hold"}`)
	knowledge := json.RawMessage(`{"knowledge":[{"id":"k-aggregation-basis","version":1},{"id":"k9","version":1}]}`)
	uncompared := map[eval.Condition]eval.Comparison{}
	none := eval.Comparison{Fixed: []string{}, Regressed: []string{}, Weakened: []string{}}
	type args struct {
		traces    []trace.Trace
		revisions []trace.Trace
		feedback  []feedback.Feedback
	}
	type want struct {
		report eval.Report
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a review citing one expected and one stray paragraph scores half precision and recall",
			args{traces: []trace.Trace{{Subject: "tq-005", Tags: off, Output: ready, DurationMS: 5, Usage: trace.Usage{
				CostUSD: 0.25, InputTokens: 10, CacheReadTokens: 5, CacheCreateTokens: 5, OutputTokens: 3,
			}}}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1, EditRate: na, MeanEditWidth: na,
					MeanCostUSD: 0.25, MeanInputTokens: 20, MeanOutputTokens: 3, P50DurationMS: 5, P95DurationMS: 5,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
					ExpectedStatus: diagnose.StatusReadyForReview, Status: diagnose.StatusReadyForReview, StatusOK: true,
					CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1,
					CostUSD: 0.25, InputTokens: 20, OutputTokens: 3, DurationMS: 5,
				}},
			}},
		},
		{
			"a review citing nothing scores zero precision and recall",
			args{traces: []trace.Trace{
				{Subject: "tq-005", Tags: off, Output: json.RawMessage(`{"status":"ready_for_review","causes":[]}`)},
			}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
					ExpectedStatus: diagnose.StatusReadyForReview, Status: diagnose.StatusReadyForReview, StatusOK: true,
				}},
			}},
		},
		{
			"a gate hold on a hold label scores the status as forced",
			args{traces: []trace.Trace{{
				Subject: "tq-017", Tags: []string{string(base), diagnose.TagGateHold},
				Output: json.RawMessage(`{"status":"hold","hold_reasons":["gap"]}`),
			}}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: 1, HoldPrecision: 1,
					CitationPrecision: na, CitationRecall: na, KnowledgeHit: na, ForcedHolds: 1, EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-017", Condition: base, Type: evidence.TypeHold,
					ExpectedStatus: diagnose.StatusHold, Status: diagnose.StatusHold, StatusOK: true, ForcedHold: true,
					CitationPrecision: na, CitationRecall: na, KnowledgeHit: na,
				}},
			}},
		},
		{
			"a trace with an error scores zero on every metric its label expects",
			args{traces: []trace.Trace{{Subject: "tq-005", Tags: off, Error: "boom", DurationMS: 7}}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, HoldRecall: na, HoldPrecision: na, Failures: 1,
					EditRate: na, MeanEditWidth: na, P50DurationMS: 7, P95DurationMS: 7,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
					ExpectedStatus: diagnose.StatusReadyForReview, Failed: true, DurationMS: 7,
				}},
			}},
		},
		{
			"an output that is not a review scores as a failure",
			args{traces: []trace.Trace{{Subject: "tq-001", Tags: off, Output: json.RawMessage(`[]`)}}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, HoldRecall: na, HoldPrecision: na, CitationPrecision: na, CitationRecall: na,
					RequiredChecks: na, FirstCheck: na, KnowledgeHit: na, Failures: 1, EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-001", Condition: base, Type: evidence.TypeNormalVariation,
					ExpectedStatus: diagnose.StatusNoAction, Failed: true,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
				}},
			}},
		},
		{
			"a failed trace given knowledge counts none of it as used",
			args{traces: []trace.Trace{{Subject: "tq-005", Tags: off, Error: "boom", Input: knowledge}}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, HoldRecall: na, HoldPrecision: na, Failures: 1, EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
					ExpectedStatus: diagnose.StatusReadyForReview, Failed: true,
				}},
			}},
		},
		{
			"feedback on a failed trace still counts its verdict",
			args{
				traces:   []trace.Trace{{ID: "t1", Subject: "tq-001", Tags: off, Error: "boom"}},
				feedback: []feedback.Feedback{{TraceID: "t1", Verdict: feedback.VerdictReject}},
			},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, HoldRecall: na, HoldPrecision: na, CitationPrecision: na, CitationRecall: na,
					RequiredChecks: na, FirstCheck: na, KnowledgeHit: na, Failures: 1,
					Annotated: 1, Rejections: 1, EditRate: 1, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-001", Condition: base, Type: evidence.TypeNormalVariation,
					ExpectedStatus: diagnose.StatusNoAction, Failed: true, Verdict: feedback.VerdictReject,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
				}},
			}},
		},
		{
			"an edit verdict counts the changed and the added top level fields",
			args{
				traces:   []trace.Trace{{ID: "t1", Subject: "tq-005", Tags: off, Output: ready}},
				feedback: []feedback.Feedback{{TraceID: "t1", Verdict: feedback.VerdictEdit, Edited: edited}},
			},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1,
					Annotated: 1, Edits: 1, EditRate: 1, MeanEditWidth: 2,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
					ExpectedStatus: diagnose.StatusReadyForReview, Status: diagnose.StatusReadyForReview, StatusOK: true,
					CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1,
					Verdict: feedback.VerdictEdit, EditWidth: 2,
				}},
			}},
		},
		{
			"an edit verdict whose edited review is not an object counts no fields",
			args{
				traces:   []trace.Trace{{ID: "t1", Subject: "tq-005", Tags: off, Output: ready}},
				feedback: []feedback.Feedback{{TraceID: "t1", Verdict: feedback.VerdictEdit, Edited: json.RawMessage(`[]`)}},
			},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1,
					Annotated: 1, Edits: 1, EditRate: 1,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
					ExpectedStatus: diagnose.StatusReadyForReview, Status: diagnose.StatusReadyForReview, StatusOK: true,
					CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1,
					Verdict: feedback.VerdictEdit,
				}},
			}},
		},
		{
			"knowledge used partly as expected scores the hit and each misapplied id",
			args{traces: []trace.Trace{{Subject: "tq-005", Tags: off, Output: ready, Input: knowledge}}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1,
					KnowledgeHit: 1, Misapplications: 1, EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
					ExpectedStatus: diagnose.StatusReadyForReview, Status: diagnose.StatusReadyForReview, StatusOK: true,
					CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1,
					KnowledgeHit: 1, Misapplications: 1, KnowledgeUsed: []string{"k-aggregation-basis", "k9"},
				}},
			}},
		},
		{
			"knowledge used where none is expected is misapplied",
			args{traces: []trace.Trace{{
				Subject: "tq-001", Tags: off, Output: noAction, Input: json.RawMessage(`{"knowledge":[{"id":"k1","version":1}]}`),
			}}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					Misapplications: 1, EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-001", Condition: base, Type: evidence.TypeNormalVariation,
					ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					Misapplications: 1, KnowledgeUsed: []string{"k1"},
				}},
			}},
		},
		{
			"a correct no_action review of an attribution lag event leaves required checks not applicable",
			args{traces: []trace.Trace{
				{Subject: "tq-021", Tags: off, Output: json.RawMessage(`{"status":"no_action","causes":[],"checks":[]}`)},
			}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-021", Condition: base, Type: evidence.TypeAttributionLag,
					ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na,
				}},
			}},
		},
		{
			"checks of a wrong ready_for_review answer on a no_action label are not scored",
			args{traces: []trace.Trace{{Subject: "tq-001", Tags: off, Output: ready}}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, HoldRecall: na, HoldPrecision: na, CitationPrecision: na, CitationRecall: na,
					RequiredChecks: na, FirstCheck: na, KnowledgeHit: na, EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-001", Condition: base, Type: evidence.TypeNormalVariation,
					ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusReadyForReview,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
				}},
			}},
		},
		{
			"means keep failures in every denominator and durations take the nearest rank",
			args{
				traces: []trace.Trace{
					{
						ID: "t1", Subject: "tq-017", Tags: []string{string(base), diagnose.TagGateHold}, DurationMS: 100,
						Usage:  trace.Usage{CostUSD: 0.25, InputTokens: 100, OutputTokens: 30},
						Output: json.RawMessage(`{"status":"hold","checks":[{"paragraph_ids":["` + coverage + `"]}]}`),
					},
					{
						ID: "t2", Subject: "tq-005", Tags: off, DurationMS: 300, Usage: trace.Usage{CostUSD: 0.5, InputTokens: 300},
						Output: json.RawMessage(`{"status":"ready_for_review",` +
							`"causes":[{"summary":"x","paragraph_ids":["` + segment + `","` + downstream + `"]}],` + checks + `}`),
					},
					{ID: "t3", Subject: "tq-001", Tags: off, DurationMS: 200, Error: "boom"},
				},
				feedback: []feedback.Feedback{
					{TraceID: "t1", Verdict: feedback.VerdictApprove},
					{TraceID: "t2", Verdict: feedback.VerdictEdit, Edited: hold},
					{TraceID: "t3", Verdict: feedback.VerdictReject},
				},
			},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 3, StatusAccuracy: 2.0 / 3, HoldRecall: 1, HoldPrecision: 1,
					CitationPrecision: 1, CitationRecall: 1, RequiredChecks: 0.75, FirstCheck: 1, ForcedHolds: 1, Failures: 1,
					Annotated: 3, Approvals: 1, Edits: 1, Rejections: 1, EditRate: 2.0 / 3, MeanEditWidth: 3,
					MeanCostUSD: 0.25, MeanInputTokens: 400.0 / 3, MeanOutputTokens: 10, P50DurationMS: 200, P95DurationMS: 300,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{
					{
						EventID: "tq-001", Condition: base, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Failed: true, Verdict: feedback.VerdictReject, DurationMS: 200,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
						ExpectedStatus: diagnose.StatusReadyForReview, Status: diagnose.StatusReadyForReview, StatusOK: true,
						CitationPrecision: 1, CitationRecall: 1, RequiredChecks: 0.5, FirstCheck: 1,
						Verdict: feedback.VerdictEdit, EditWidth: 3, CostUSD: 0.5, InputTokens: 300, DurationMS: 300,
					},
					{
						EventID: "tq-017", Condition: base, Type: evidence.TypeHold,
						ExpectedStatus: diagnose.StatusHold, Status: diagnose.StatusHold, StatusOK: true, ForcedHold: true,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: 1, FirstCheck: 1, KnowledgeHit: na,
						Verdict: feedback.VerdictApprove, CostUSD: 0.25, InputTokens: 100, OutputTokens: 30, DurationMS: 100,
					},
				},
			}},
		},
		{
			"a hold answer on an event whose label does not hold lowers hold precision",
			args{traces: []trace.Trace{
				{Subject: "tq-017", Tags: off, Output: hold},
				{Subject: "tq-003", Tags: off, Output: hold},
			}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 2, StatusAccuracy: 0.5, HoldRecall: 1, HoldPrecision: 0.5,
					CitationPrecision: na, CitationRecall: na, KnowledgeHit: na, EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{
					{
						EventID: "tq-003", Condition: base, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusHold,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-017", Condition: base, Type: evidence.TypeHold,
						ExpectedStatus: diagnose.StatusHold, Status: diagnose.StatusHold, StatusOK: true,
						CitationPrecision: na, CitationRecall: na, KnowledgeHit: na,
					},
				},
			}},
		},
		{
			"the p95 of eleven durations is the eleventh by ceiling rank",
			args{traces: []trace.Trace{
				{Subject: "tq-001", Tags: off, Error: "x", DurationMS: 100},
				{Subject: "tq-002", Tags: off, Error: "x", DurationMS: 200},
				{Subject: "tq-003", Tags: off, Error: "x", DurationMS: 300},
				{Subject: "tq-004", Tags: off, Error: "x", DurationMS: 400},
				{Subject: "tq-005", Tags: off, Error: "x", DurationMS: 500},
				{Subject: "tq-006", Tags: off, Error: "x", DurationMS: 600},
				{Subject: "tq-007", Tags: off, Error: "x", DurationMS: 700},
				{Subject: "tq-008", Tags: off, Error: "x", DurationMS: 800},
				{Subject: "tq-009", Tags: off, Error: "x", DurationMS: 900},
				{Subject: "tq-010", Tags: off, Error: "x", DurationMS: 1000},
				{Subject: "tq-011", Tags: off, Error: "x", DurationMS: 1100},
			}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 11, HoldRecall: na, HoldPrecision: na, Failures: 11, EditRate: na, MeanEditWidth: na,
					P50DurationMS: 600, P95DurationMS: 1100,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{
					{
						EventID: "tq-001", Condition: base, Type: evidence.TypeNormalVariation, ExpectedStatus: diagnose.StatusNoAction,
						Failed: true, DurationMS: 100,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-002", Condition: base, Type: evidence.TypeNormalVariation, ExpectedStatus: diagnose.StatusNoAction,
						Failed: true, DurationMS: 200,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-003", Condition: base, Type: evidence.TypeNormalVariation, ExpectedStatus: diagnose.StatusNoAction,
						Failed: true, DurationMS: 300,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-004", Condition: base, Type: evidence.TypeNormalVariation, ExpectedStatus: diagnose.StatusNoAction,
						Failed: true, DurationMS: 400,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
						ExpectedStatus: diagnose.StatusReadyForReview, Failed: true, DurationMS: 500,
					},
					{
						EventID: "tq-006", Condition: base, Type: evidence.TypeClickSpike,
						ExpectedStatus: diagnose.StatusReadyForReview, Failed: true, DurationMS: 600,
					},
					{
						EventID: "tq-007", Condition: base, Type: evidence.TypeClickSpike,
						ExpectedStatus: diagnose.StatusReadyForReview, Failed: true, DurationMS: 700,
					},
					{
						EventID: "tq-008", Condition: base, Type: evidence.TypeClickSpike,
						ExpectedStatus: diagnose.StatusReadyForReview, Failed: true, DurationMS: 800,
					},
					{
						EventID: "tq-009", Condition: base, Type: evidence.TypeConversionRateDrop,
						ExpectedStatus: diagnose.StatusReadyForReview, Failed: true, DurationMS: 900,
					},
					{
						EventID: "tq-010", Condition: base, Type: evidence.TypeConversionRateDrop,
						ExpectedStatus: diagnose.StatusReadyForReview, Failed: true, DurationMS: 1000,
					},
					{
						EventID: "tq-011", Condition: base, Type: evidence.TypeConversionRateDrop,
						ExpectedStatus: diagnose.StatusReadyForReview, Failed: true, DurationMS: 1100,
					},
				},
			}},
		},
		{
			"conditions without hold labels or annotations report them as not applicable in report order",
			args{traces: []trace.Trace{
				{Subject: "tq-003", Tags: []string{string(eval.ConditionKnowledgeAll)}, Output: noAction},
				{Subject: "tq-001", Tags: []string{string(eval.ConditionSeed)}, Output: noAction},
			}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{
					{
						Condition: eval.ConditionSeed, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
						EditRate: na, MeanEditWidth: na,
					},
					{
						Condition: eval.ConditionKnowledgeAll, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
						EditRate: na, MeanEditWidth: na,
					},
				},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{
					{
						EventID: "tq-001", Condition: eval.ConditionSeed, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-003", Condition: eval.ConditionKnowledgeAll, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
				},
			}},
		},
		{
			"the newest trace per event and condition replaces an earlier one",
			args{traces: []trace.Trace{
				{ID: "t1", Subject: "tq-003", Tags: off, Error: "boom"},
				{ID: "t2", Subject: "tq-003", Tags: off, Output: noAction},
			}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-003", Condition: base, Type: evidence.TypeNormalVariation,
					ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
				}},
			}},
		},
		{
			"traces of unknown events or without a condition tag are left out",
			args{traces: []trace.Trace{
				{ID: "t1", Subject: "tq-999", Tags: off, Output: noAction},
				{ID: "t2", Subject: "tq-003", Tags: []string{"other"}, Output: noAction},
			}},
			want{report: eval.Report{SessionID: "s", AgainstBaseline: uncompared}},
		},
		{
			"holdout conditions are not compared when the baseline was not run",
			args{traces: []trace.Trace{{ID: "t1", Subject: "tq-003", Tags: []string{"feedback:on"}, Output: noAction}}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: eval.ConditionExamples, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					EditRate: na, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-003", Condition: eval.ConditionExamples, Type: evidence.TypeNormalVariation,
					ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
				}},
			}},
		},
		{
			"each holdout condition is compared with the baseline",
			args{traces: []trace.Trace{
				{ID: "t1", Subject: "tq-003", Tags: off, Output: noAction},
				{ID: "t2", Subject: "tq-003", Tags: []string{"feedback:on"}, Output: hold},
				{ID: "t3", Subject: "tq-003", Tags: []string{"knowledge:on"}, Output: noAction},
			}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{
					{
						Condition: base, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
						EditRate: na, MeanEditWidth: na,
					},
					{
						Condition: eval.ConditionExamples, Events: 1, HoldRecall: na,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
						EditRate: na, MeanEditWidth: na,
					},
					{
						Condition: eval.ConditionKnowledge, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
						EditRate: na, MeanEditWidth: na,
					},
				},
				AgainstBaseline: map[eval.Condition]eval.Comparison{
					eval.ConditionExamples:  {Fixed: []string{}, Regressed: []string{"tq-003"}, Weakened: []string{}},
					eval.ConditionKnowledge: none,
				},
				Scores: []eval.Score{
					{
						EventID: "tq-003", Condition: base, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-003", Condition: eval.ConditionExamples, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusHold,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-003", Condition: eval.ConditionKnowledge, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
				},
			}},
		},
		{
			"a condition that fixes one event and weakens another names both against the baseline",
			args{traces: []trace.Trace{
				{ID: "t1", Subject: "tq-003", Tags: off, Output: hold},
				{ID: "t2", Subject: "tq-005", Tags: off, Output: ready},
				{ID: "t3", Subject: "tq-003", Tags: []string{"feedback:on"}, Output: noAction},
				{ID: "t4", Subject: "tq-005", Tags: []string{"feedback:on"}, Output: readyBare},
			}},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{
					{
						Condition: base, Events: 2, StatusAccuracy: 0.5, HoldRecall: na, HoldPrecision: 0,
						CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1,
						EditRate: na, MeanEditWidth: na,
					},
					{
						Condition: eval.ConditionExamples, Events: 2, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
						EditRate: na, MeanEditWidth: na,
					},
				},
				AgainstBaseline: map[eval.Condition]eval.Comparison{
					eval.ConditionExamples: {Fixed: []string{"tq-003"}, Regressed: []string{}, Weakened: []string{"tq-005"}},
				},
				Scores: []eval.Score{
					{
						EventID: "tq-003", Condition: base, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusHold,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-005", Condition: base, Type: evidence.TypeClickSpike,
						ExpectedStatus: diagnose.StatusReadyForReview, Status: diagnose.StatusReadyForReview, StatusOK: true,
						CitationPrecision: 0.5, CitationRecall: 0.5, RequiredChecks: 0.5, FirstCheck: 1,
					},
					{
						EventID: "tq-003", Condition: eval.ConditionExamples, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					},
					{
						EventID: "tq-005", Condition: eval.ConditionExamples, Type: evidence.TypeClickSpike,
						ExpectedStatus: diagnose.StatusReadyForReview, Status: diagnose.StatusReadyForReview, StatusOK: true,
					},
				},
			}},
		},
		{
			"the latest feedback on a trace joins its score",
			args{
				traces: []trace.Trace{{ID: "t1", Subject: "tq-003", Tags: []string{"seed"}, Output: noAction}},
				feedback: []feedback.Feedback{
					{TraceID: "t1", Verdict: feedback.VerdictEdit, Time: at},
					{TraceID: "t1", Verdict: feedback.VerdictApprove, Time: at.Add(time.Second)},
				},
			},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: eval.ConditionSeed, Events: 1, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					Annotated: 1, Approvals: 1, MeanEditWidth: na,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{{
					EventID: "tq-003", Condition: eval.ConditionSeed, Type: evidence.TypeNormalVariation,
					ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					Verdict: feedback.VerdictApprove,
				}},
			}},
		},
		{
			"a review that record sent back once carries the cost and duration of both model calls",
			args{
				traces: []trace.Trace{
					{
						ID: "d1", Subject: "tq-001", Ref: "c1", Tags: off, Output: noAction, DurationMS: 500,
						Usage: trace.Usage{CostUSD: 0.25, InputTokens: 150},
					},
					{
						ID: "d2", Subject: "tq-002", Ref: "c2", Tags: off, Output: noAction, DurationMS: 100,
						Usage: trace.Usage{CostUSD: 0.5},
					},
				},
				revisions: []trace.Trace{{
					ID: "r1", Subject: "tq-001", Ref: "c1", Tags: off, Output: noAction, DurationMS: 300,
					Usage: trace.Usage{CostUSD: 0.125, InputTokens: 100}, Input: json.RawMessage(`{"reasons":["x"]}`),
				}},
			},
			want{report: eval.Report{
				SessionID: "s",
				Summaries: []eval.Summary{{
					Condition: base, Events: 2, StatusAccuracy: 1, HoldRecall: na, HoldPrecision: na,
					CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
					Revisions: 1, EditRate: na, MeanEditWidth: na,
					MeanCostUSD: 0.4375, MeanInputTokens: 125, P50DurationMS: 100, P95DurationMS: 800,
				}},
				AgainstBaseline: uncompared,
				Scores: []eval.Score{
					{
						EventID: "tq-001", Condition: base, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
						Revised: true, CostUSD: 0.375, InputTokens: 250, DurationMS: 800,
					},
					{
						EventID: "tq-002", Condition: base, Type: evidence.TypeNormalVariation,
						ExpectedStatus: diagnose.StatusNoAction, Status: diagnose.StatusNoAction, StatusOK: true,
						CitationPrecision: na, CitationRecall: na, RequiredChecks: na, FirstCheck: na, KnowledgeHit: na,
						CostUSD: 0.5, DurationMS: 100,
					},
				},
			}},
		},
		{
			"a session without diagnose traces is refused",
			args{},
			want{err: eval.ErrNoTraces},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			for _, tr := range tc.args.traces {
				tr.Name, tr.SessionID = trace.NameDiagnose, "s"
				require.NoError(t, s.Traces.Append(ctx, tr))
			}
			for _, tr := range tc.args.revisions {
				tr.Name, tr.SessionID = trace.NameRevise, "s"
				require.NoError(t, s.Traces.Append(ctx, tr))
			}
			for _, fb := range tc.args.feedback {
				require.NoError(t, s.Feedback.Append(ctx, fb))
			}
			rep, err := eval.New(s.Source, nil, s.Traces, s.Feedback, s.Ledger).Report(ctx, "s")
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.report, rep)
		})
	}
}

// The label source is a temp dir whose labels file is a directory
func TestReportLabels(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"report fails when the labels cannot be read", "labels.jsonl", syscall.EISDIR},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, tc.args), 0o700))
			src, err := evidencefile.New(dir)
			require.NoError(t, err)
			rep, err := eval.New(src, nil, s.Traces, s.Feedback, s.Ledger).Report(ctx, "s")
			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, eval.Report{}, rep)
		})
	}
}
