package eval_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/eval"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The session already holds one review per entry of session with those tags
func TestHoldoutRepeated(t *testing.T) {
	type args struct {
		repeat  int
		session [][]string
	}
	type want struct {
		tags [][]string
		err  error
	}
	three := [][]string{{"feedback:off", "repeat:1"}, {"feedback:off", "repeat:2"}, {"feedback:off", "repeat:3"}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a fresh session runs once by default", args{repeat: 0}, want{tags: [][]string{{"feedback:off"}}}},
		{"a fresh session takes three repeats", args{repeat: 3}, want{tags: three}},
		{"a negative repeat is refused", args{repeat: -1}, want{err: eval.ErrRepeat}},
		{
			"a single run joins single runs",
			args{repeat: 1, session: [][]string{{"knowledge:on"}}},
			want{tags: [][]string{{"feedback:off"}}},
		},
		{
			"a repeated run joins repeated runs",
			args{repeat: 3, session: [][]string{{"knowledge:on", "repeat:2"}}},
			want{tags: three},
		},
		{
			"a repeated run is refused beside single runs",
			args{repeat: 3, session: [][]string{{"knowledge:on"}}},
			want{err: eval.ErrRepeatSession},
		},
		{
			"a single run is refused beside repeated runs",
			args{repeat: 0, session: [][]string{{"knowledge:on", "repeat:2"}}},
			want{err: eval.ErrRepeatSession},
		},
		{"seed reviews do not count", args{repeat: 3, session: [][]string{{"seed"}}}, want{tags: three}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			for i, tags := range tc.args.session {
				require.NoError(t, s.Traces.Append(ctx, trace.Trace{
					ID: fmt.Sprintf("old-%d", i), Name: trace.NameDiagnose, SessionID: "repeated", Subject: "tq-004", Tags: tags,
				}))
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{
				Output: json.RawMessage(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`),
			}, nil).Times(len(tc.want.tags))
			d := diagnose.New(s.Source, testkit.Policy(t), client, s.Traces, s.Feedback, s.Outcomes, s.Ledger, s.Clock.Now)
			r := eval.New(s.Source, d, s.Traces, s.Feedback, s.Ledger)
			traces, err := r.Holdout(ctx, eval.RunOptions{
				SessionID: "repeated", Repeat: tc.args.repeat, Parallel: 1,
				Events: []string{"tq-003"}, Conditions: []eval.Condition{eval.ConditionBaseline},
			})
			assert.ErrorIs(t, err, tc.want.err)
			var tags [][]string
			for _, tr := range traces {
				tags = append(tags, tr.Tags)
			}
			assert.Equal(t, tc.want.tags, tags)
		})
	}
}

// The session already holds one review per entry of session with those tags
func TestSeedRepeated(t *testing.T) {
	type args struct {
		repeat  int
		session [][]string
	}
	type want struct {
		tags [][]string
		err  error
	}
	two := [][]string{{"seed", "repeat:1"}, {"seed", "repeat:2"}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a repeated seed run joins repeated seed reviews", args{repeat: 2, session: [][]string{{"seed", "repeat:1"}}}, want{tags: two}},
		{
			"a repeated seed run is refused beside single seed reviews",
			args{repeat: 2, session: [][]string{{"seed"}}},
			want{err: eval.ErrRepeatSession},
		},
		{"holdout reviews do not count", args{repeat: 2, session: [][]string{{"feedback:off"}}}, want{tags: two}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			for i, tags := range tc.args.session {
				require.NoError(t, s.Traces.Append(ctx, trace.Trace{
					ID: fmt.Sprintf("old-%d", i), Name: trace.NameDiagnose, SessionID: "repeated", Subject: "tq-002", Tags: tags,
				}))
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{
				Output: json.RawMessage(`{"status":"no_action","observations":[],"causes":[],"checks":[],"open_questions":[]}`),
			}, nil).Times(len(tc.want.tags))
			d := diagnose.New(s.Source, testkit.Policy(t), client, s.Traces, s.Feedback, s.Outcomes, s.Ledger, s.Clock.Now)
			r := eval.New(s.Source, d, s.Traces, s.Feedback, s.Ledger)
			traces, err := r.Seed(ctx, eval.RunOptions{
				SessionID: "repeated", Repeat: tc.args.repeat, Parallel: 1, Events: []string{"tq-001"},
			})
			assert.ErrorIs(t, err, tc.want.err)
			var tags [][]string
			for _, tr := range traces {
				tags = append(tags, tr.Tags)
			}
			assert.Equal(t, tc.want.tags, tags)
		})
	}
}

// The baseline answers hold on tq-003 and tq-004 in both repeats and the labels expect no_action
func TestRepeatedReport(t *testing.T) {
	type args struct {
		// The repeat tag of each repeat
		tags [2]string
		// The knowledge:on status per repeat and event
		knowledge [2][2]string
	}
	type want struct {
		repeats                         []int
		mean, lowest, highest, agreeing float64
		fixed, regressed                int
		p                               float64
	}
	allHold := [2][2]string{{"hold", "hold"}, {"hold", "hold"}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"two improvements",
			args{tags: [2]string{"repeat:1", "repeat:2"}, knowledge: [2][2]string{{"no_action", "no_action"}, {"no_action", "no_action"}}},
			want{repeats: []int{1, 2}, mean: 1, lowest: 1, highest: 1, agreeing: 1, fixed: 2, p: 0.5},
		},
		{
			"inconsistent repetitions",
			args{tags: [2]string{"repeat:1", "repeat:2"}, knowledge: [2][2]string{{"no_action", "no_action"}, {"hold", "hold"}}},
			want{repeats: []int{1, 2}, mean: 0.5, lowest: 0, highest: 1, agreeing: 0.5, fixed: 2, p: 0.5},
		},
		{
			"no discordance",
			args{tags: [2]string{"repeat:1", "repeat:2"}, knowledge: allHold},
			want{repeats: []int{1, 2}, agreeing: 1, p: 1},
		},
		{
			"a malformed repeat tag counts as the first repeat",
			args{tags: [2]string{"repeat:x", "repeat:3"}, knowledge: allHold},
			want{repeats: []int{1, 3}, agreeing: 1, p: 1},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			outputs := map[eval.Condition][2][2]string{eval.ConditionBaseline: allHold, eval.ConditionKnowledge: tc.args.knowledge}
			for _, condition := range []eval.Condition{eval.ConditionBaseline, eval.ConditionKnowledge} {
				for repeat, statuses := range outputs[condition] {
					for event, status := range statuses {
						require.NoError(t, s.Traces.Append(ctx, trace.Trace{
							ID: fmt.Sprintf("%d-%d-%s", repeat, event, condition), Name: trace.NameDiagnose,
							SessionID: "repeated", Subject: fmt.Sprintf("tq-%03d", event+3),
							Tags:   []string{string(condition), tc.args.tags[repeat]},
							Output: json.RawMessage(fmt.Sprintf(`{"status":%q}`, status)),
						}))
					}
				}
			}
			r := eval.New(s.Source, nil, s.Traces, s.Feedback, s.Ledger)
			rep, err := r.Report(ctx, "repeated")
			require.NoError(t, err)
			var repeats []int
			for _, run := range rep.Runs {
				repeats = append(repeats, run.Repeat)
			}
			assert.Equal(t, tc.want.repeats, repeats)
			require.Len(t, rep.Stability, 2)
			got := rep.Stability[1]
			assert.Equal(t, tc.want.mean, got.StatusAccuracyMean)
			assert.Equal(t, tc.want.lowest, got.StatusAccuracyMin)
			assert.Equal(t, tc.want.highest, got.StatusAccuracyMax)
			assert.Equal(t, []eval.EventAgreement{
				{EventID: "tq-003", Runs: 2, Agreement: tc.want.agreeing},
				{EventID: "tq-004", Runs: 2, Agreement: tc.want.agreeing},
			}, got.Events)
			require.Len(t, rep.Runs, 2)
			assert.Equal(t, []eval.Pair{{
				Reference: eval.ConditionBaseline, Condition: eval.ConditionKnowledge, Events: 2,
				Fixed: tc.want.fixed, Regressed: tc.want.regressed, McNemarP: tc.want.p,
			}}, rep.Runs[0].Pairs)
			assert.Contains(t, rep.Table(), "First repeat details")
			assert.Contains(t, rep.Table(), "status agreement")
		})
	}
}
