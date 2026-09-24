package diagnose_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestRecordGate(t *testing.T) {
	const (
		segment = "metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"
		signal  = "metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"
	)
	type want struct {
		// Result without its trace id
		result diagnose.Result
		// Name and tags of the trace the result id refers to
		traced trace.Name
		tags   []string
	}
	cause := []diagnose.Cause{{Summary: "cause x", ParagraphIDs: []string{segment}}}
	uncited := []diagnose.Cause{{Summary: "cause x", ParagraphIDs: []string{"nope"}}}
	gateHold := []string{diagnose.TagGateHold}
	tcs := []struct {
		name string
		args diagnose.Diagnosis
		want want
	}{
		{
			name: "ready review citing known paragraphs stays",
			args: diagnose.Diagnosis{
				Status: diagnose.StatusReadyForReview,
				Causes: []diagnose.Cause{{Summary: "cause x", ParagraphIDs: []string{segment, signal}}},
				Checks: diagnose.Checks{{Step: "s", ParagraphIDs: []string{signal, segment}}},
			},
			want: want{result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
				Status: diagnose.StatusReadyForReview,
				Causes: []diagnose.Cause{{Summary: "cause x", ParagraphIDs: []string{segment, signal}}},
				Checks: diagnose.Checks{{Step: "s", ParagraphIDs: []string{signal, segment}}},
			}}, traced: trace.NameDiagnose},
		},
		{
			name: "unknown ids are dropped from causes and checks and the cause survives",
			args: diagnose.Diagnosis{
				Status: diagnose.StatusReadyForReview,
				Causes: []diagnose.Cause{{Summary: "cause x", ParagraphIDs: []string{segment, "nope"}}},
				Checks: diagnose.Checks{{Step: "s", ParagraphIDs: []string{"nope", signal, segment}}},
			},
			want: want{result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
				Status: diagnose.StatusReadyForReview, Causes: cause,
				Checks: diagnose.Checks{{Step: "s", ParagraphIDs: []string{signal, segment}}},
			}}, traced: trace.NameDiagnose},
		},
		{
			name: "cause left without a citation forces hold naming the cause",
			args: diagnose.Diagnosis{Status: diagnose.StatusReadyForReview, Causes: uncited},
			want: want{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
					Status: diagnose.StatusHold, Causes: []diagnose.Cause{}, HoldReasons: []string{"no paragraph supports: cause x"},
				}, Forced: true},
				traced: trace.NameDiagnose, tags: gateHold,
			},
		},
		{
			name: "ready review without causes forces hold",
			args: diagnose.Diagnosis{Status: diagnose.StatusReadyForReview},
			want: want{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
					Status: diagnose.StatusHold, Causes: []diagnose.Cause{}, HoldReasons: []string{"no cause was given"},
				}, Forced: true},
				traced: trace.NameDiagnose, tags: gateHold,
			},
		},
		{
			name: "no action without causes stays",
			args: diagnose.Diagnosis{Status: diagnose.StatusNoAction},
			want: want{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{Status: diagnose.StatusNoAction}},
				traced: trace.NameDiagnose,
			},
		},
		{
			name: "no action with causes forces hold",
			args: diagnose.Diagnosis{Status: diagnose.StatusNoAction, Causes: cause},
			want: want{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
					Status: diagnose.StatusHold, Causes: []diagnose.Cause{},
					HoldReasons: []string{"no_action was returned together with causes"},
				}, Forced: true},
				traced: trace.NameDiagnose, tags: gateHold,
			},
		},
		{
			name: "hold keeps its reasons and an empty causes list",
			args: diagnose.Diagnosis{Status: diagnose.StatusHold, HoldReasons: []string{"gap in window"}},
			want: want{result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
				Status: diagnose.StatusHold, Causes: []diagnose.Cause{}, HoldReasons: []string{"gap in window"},
			}}, traced: trace.NameDiagnose},
		},
		{
			name: "hold drops the causes the model returned with it",
			args: diagnose.Diagnosis{
				Status:      diagnose.StatusHold,
				Causes:      cause,
				HoldReasons: []string{"gap in window"},
			},
			want: want{result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
				Status: diagnose.StatusHold, Causes: []diagnose.Cause{}, HoldReasons: []string{"gap in window"},
			}}, traced: trace.NameDiagnose},
		},
		{
			name: "hold keeps the checks that would resolve it",
			args: diagnose.Diagnosis{
				Status: diagnose.StatusHold, HoldReasons: []string{"tracking changed in the window"},
				Checks: diagnose.Checks{
					{
						Step:         "compare the tracking release",
						Purpose:      "rules out a measurement change",
						ParagraphIDs: []string{signal},
					},
				},
			},
			want: want{result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
				Status: diagnose.StatusHold, Causes: []diagnose.Cause{}, HoldReasons: []string{"tracking changed in the window"},
				Checks: diagnose.Checks{
					{
						Step:         "compare the tracking release",
						Purpose:      "rules out a measurement change",
						ParagraphIDs: []string{signal},
					},
				},
			}}, traced: trace.NameDiagnose},
		},
		{
			name: "hold drops unknown ids from its checks and keeps the checks",
			args: diagnose.Diagnosis{
				Status: diagnose.StatusHold, HoldReasons: []string{"gap in window"},
				Checks: diagnose.Checks{
					{Step: "s", ParagraphIDs: []string{"nope", segment}},
					{Step: "t", ParagraphIDs: []string{"nope"}},
				},
			},
			want: want{result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
				Status: diagnose.StatusHold, Causes: []diagnose.Cause{}, HoldReasons: []string{"gap in window"},
				Checks: diagnose.Checks{{Step: "s", ParagraphIDs: []string{segment}}, {Step: "t"}},
			}}, traced: trace.NameDiagnose},
		},
		{
			name: "forced hold keeps the checks of the review it replaced",
			args: diagnose.Diagnosis{
				Status: diagnose.StatusReadyForReview, Causes: uncited,
				Checks: diagnose.Checks{{Step: "s", ParagraphIDs: []string{signal}}},
			},
			want: want{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
					Status: diagnose.StatusHold, Causes: []diagnose.Cause{}, HoldReasons: []string{"no paragraph supports: cause x"},
					Checks: diagnose.Checks{{Step: "s", ParagraphIDs: []string{signal}}},
				}, Forced: true},
				traced: trace.NameDiagnose, tags: gateHold,
			},
		},
		{
			name: "hold without reasons gets one",
			args: diagnose.Diagnosis{Status: diagnose.StatusHold},
			want: want{result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
				Status: diagnose.StatusHold, Causes: []diagnose.Cause{},
				HoldReasons: []string{"the model returned hold without a reason"},
			}}, traced: trace.NameDiagnose},
		},
		{
			name: "unknown status forces hold",
			args: diagnose.Diagnosis{Status: "maybe"},
			want: want{
				result: diagnose.Result{Diagnosis: diagnose.Diagnosis{
					Status: diagnose.StatusHold, Causes: []diagnose.Cause{}, HoldReasons: []string{`unknown status "maybe"`},
				}, Forced: true},
				traced: trace.NameDiagnose, tags: gateHold,
			},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), diagnose.Limits{}, nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			c, err := d.Prepare(ctx, "tq-005", diagnose.ModeInteractive, diagnose.Session{})
			require.NoError(t, err)
			before, err := json.Marshal(tc.args)
			require.NoError(t, err)

			got, err := d.Record(ctx, c.PendingID, tc.args)
			require.NoError(t, err)
			after, err := json.Marshal(tc.args)
			require.NoError(t, err)
			tr, err := s.Traces.Get(ctx, got.TraceID)
			require.NoError(t, err)

			assert.Equal(t, tc.want, want{
				result: diagnose.Result{Diagnosis: got.Diagnosis, Forced: got.Forced, Revisions: got.Revisions},
				traced: tr.Name,
				tags:   tr.Tags,
			})
			assert.JSONEq(t, string(before), string(after), "the caller's review is left as it was")
		})
	}
}
