package record_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

func TestFeedbackFilterMatches(t *testing.T) {
	fb := record.Feedback{TraceID: "t1", Verdict: "reject", Reviewer: "author"}
	tcs := []struct {
		name string
		args record.FeedbackFilter
		want bool
	}{
		{"empty filter matches all", record.FeedbackFilter{}, true},
		{"same trace id matches", record.FeedbackFilter{TraceID: "t1"}, true},
		{"other trace id does not match", record.FeedbackFilter{TraceID: "t2"}, false},
		{
			"verdict in the list matches",
			record.FeedbackFilter{Verdicts: []string{"edit", "reject"}},
			true,
		},
		{
			"verdict outside the list does not match",
			record.FeedbackFilter{Verdicts: []string{"approve"}},
			false,
		},
		{"same reviewer matches", record.FeedbackFilter{Reviewer: "author"}, true},
		{"other reviewer does not match", record.FeedbackFilter{Reviewer: "session"}, false},
		{"limit is ignored by matching", record.FeedbackFilter{Limit: 1}, true},
		{"a verdict before since does not match", record.FeedbackFilter{Since: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Matches(fb))
		})
	}
}
