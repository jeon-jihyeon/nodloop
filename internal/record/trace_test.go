package record_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

func TestTraceFilterMatches(t *testing.T) {
	tr := record.Trace{
		ID:        "d1",
		Name:      "run",
		SessionID: "eval-1",
		Subject:   "tq-001",
		Ref:       "c1",
		Tags:      []string{"feedback:on", "z:3"},
	}
	tcs := []struct {
		name string
		args record.TraceFilter
		want bool
	}{
		{"empty filter matches all", record.TraceFilter{}, true},
		{"same id matches", record.TraceFilter{ID: "d1"}, true},
		{"other id does not match", record.TraceFilter{ID: "d2"}, false},
		{"same name matches", record.TraceFilter{Name: "run"}, true},
		{"other name does not match", record.TraceFilter{Name: "check"}, false},
		{"same session matches", record.TraceFilter{SessionID: "eval-1"}, true},
		{"other session does not match", record.TraceFilter{SessionID: "eval-2"}, false},
		{"same subject matches", record.TraceFilter{Subject: "tq-001"}, true},
		{"other subject does not match", record.TraceFilter{Subject: "tq-002"}, false},
		{"same ref matches", record.TraceFilter{Ref: "c1"}, true},
		{"other ref does not match", record.TraceFilter{Ref: "c2"}, false},
		{"every tag present in any order matches", record.TraceFilter{Tags: []string{"z:3", "feedback:on"}}, true},
		{"one missing tag does not match", record.TraceFilter{Tags: []string{"feedback:on", "feedback:off"}}, false},
		{"limit is ignored by matching", record.TraceFilter{Limit: 1}, true},
		{"a trace before since does not match", record.TraceFilter{Since: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Matches(tr))
		})
	}
}
