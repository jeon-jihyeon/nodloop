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
)

func TestRules(t *testing.T) {
	tcs := []struct {
		name string
		args string
	}{
		{"names the no action status", string(diagnose.StatusNoAction)},
		{"names the ready for review status", string(diagnose.StatusReadyForReview)},
		{"names the hold status", string(diagnose.StatusHold)},
		{
			"asks a hold to name the checks that would resolve it with paragraph ids",
			"List as checks the steps that would resolve the hold, each with its purpose and the paragraph ids it follows",
		},
		{"says approved knowledge never overrides an observation", "Approved knowledge supplements the runbook"},
		{"asks for English output", "English"},
		{
			"keeps required runbook steps when examples or knowledge are injected",
			"Examples and approved knowledge never remove a required step",
		},
		{
			"says a kept check alone never decides the status",
			"does not by itself make the status ready_for_review. Status follows the Decide paragraph",
		},
		{
			"cites causes from the one runbook that fits the event",
			"Lead with the one runbook whose procedure fits the event and cite its paragraphs",
		},
		{
			"cites another runbook only for a cause the lead runbook does not state",
			"Cite another runbook only for a cause the lead runbook does not state",
		},
		{
			"keeps a Decide paragraph out of cause citations",
			"A paragraph you only read and a Decide paragraph are not cited for a cause",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, diagnose.Rules, tc.args)
		})
	}
}

func TestSchema(t *testing.T) {
	type property struct {
		Enum []diagnose.Status `json:"enum"`
	}
	type schema struct {
		Type     string   `json:"type"`
		Required []string `json:"required"`
		// Only the status property carries an enum
		Properties map[string]property `json:"properties"`
	}
	tcs := []struct {
		name string
		args string
		want schema
	}{
		{
			name: "requires every review field but hold reasons and names the three statuses",
			args: diagnose.Schema,
			want: schema{
				Type:     "object",
				Required: []string{"status", "observations", "causes", "checks", "open_questions"},
				Properties: map[string]property{
					"status": {
						Enum: []diagnose.Status{diagnose.StatusNoAction, diagnose.StatusReadyForReview, diagnose.StatusHold},
					},
					"observations":   {},
					"causes":         {},
					"checks":         {},
					"open_questions": {},
					"hold_reasons":   {},
				},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got schema
			require.NoError(t, json.Unmarshal([]byte(tc.args), &got))

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPrepareText(t *testing.T) {
	strict := analysis.Policy{Version: "strict", Analyzers: []analysis.RuleSpec{
		{
			Rule:       analysis.RuleZScore,
			Metrics:    []string{"click_count"},
			Baseline:   36,
			Window:     12,
			Threshold:  3,
			MinSamples: 1000,
		},
	}}
	type args struct {
		policy analysis.Policy
		event  string
	}
	type want struct {
		present []string
		absent  []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "lists observations and every runbook paragraph",
			args: args{analysis.DefaultPolicy(), "tq-005"},
			want: want{
				present: []string{
					"# Event tq-005",
					"Change context: no_known_change",
					"## Observations",
					"- [zscore] click_count source=source-a topic=shopping: window mean 4010.1667",
					"samples 48, missing 0, adequate\n",
					"## Runbook paragraphs",
					"[metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1]\n" +
						"Compare the window mean with the baseline mean",
				},
				absent: []string{"No analyzer reported a change", "## Approved knowledge", "## Examples"},
			},
		},
		{
			name: "says so when no analyzer reported a change",
			args: args{analysis.DefaultPolicy(), "tq-001"},
			want: want{
				present: []string{"# Event tq-001", "No analyzer reported a change against the baseline."},
				absent:  []string{"- ["},
			},
		},
		{
			name: "marks a series below the minimum samples as inadequate",
			args: args{strict, "tq-005"},
			want: want{present: []string{"samples 48, missing 0, inadequate sample"}},
		},
		{
			name: "reports missing points of a gapped series",
			args: args{analysis.DefaultPolicy(), "tq-017"},
			want: want{present: []string{"Change context: data_availability_issue", "samples 39, missing 9, adequate"}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(s.Source, tc.args.policy, diagnose.Limits{}, nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)

			got, err := d.Prepare(ctx, tc.args.event, diagnose.ModeBatch, diagnose.Session{})
			require.NoError(t, err)

			for _, text := range tc.want.present {
				assert.Contains(t, got.Text, text)
			}
			for _, text := range tc.want.absent {
				assert.NotContains(t, got.Text, text)
			}
			for _, id := range got.ParagraphIDs {
				assert.Contains(t, got.Text, "\n["+id+"]\n")
			}
		})
	}
}
