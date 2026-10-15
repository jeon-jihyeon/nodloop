package record_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

func TestOutcomeFilterMatches(t *testing.T) {
	o := record.Outcome{TraceID: "t1", Result: "confirmed"}
	tcs := []struct {
		name string
		args record.OutcomeFilter
		want bool
	}{
		{"empty filter matches all", record.OutcomeFilter{}, true},
		{"same trace id matches", record.OutcomeFilter{TraceID: "t1"}, true},
		{"other trace id does not match", record.OutcomeFilter{TraceID: "t2"}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Matches(o))
		})
	}
}
