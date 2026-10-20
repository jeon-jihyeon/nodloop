package classify_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
)

func TestAnswersVerdict(t *testing.T) {
	type args struct {
		corrects float64
		approves float64
	}
	tcs := []struct {
		name string
		args args
		want feedback.Verdict
	}{
		{"a correction alone rejects", args{0.9, 0.1}, feedback.VerdictReject},
		{"an approval alone approves", args{0.2, 0.7}, feedback.VerdictApprove},
		{"both call nothing", args{0.6, 0.6}, ""},
		{"neither calls nothing", args{0.3, 0.4}, ""},
		{"a yes from one half up counts", args{0.5, 0.49}, feedback.VerdictReject},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			as := classify.Answers{"corrects": {Yes: tc.args.corrects}, "approves": {Yes: tc.args.approves}}

			assert.Equal(t, tc.want, as.Verdict())
		})
	}
}

func TestAnswersSure(t *testing.T) {
	tcs := []struct {
		name string
		args classify.Answers
		want bool
	}{
		{"every answer at 0.8 on its side", classify.Answers{"a": {Yes: 0.8}, "b": {Yes: 0.2}}, true},
		{"one answer below 0.8", classify.Answers{"a": {Yes: 0.95}, "b": {Yes: 0.79}}, false},
		{"one answer near one half", classify.Answers{"a": {Yes: 0.5}}, false},
		{"no answer", classify.Answers{}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Sure())
		})
	}
}
