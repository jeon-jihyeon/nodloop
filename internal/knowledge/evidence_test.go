package knowledge_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestCorroborateEventUnits(t *testing.T) {
	type args struct{ event, session string }
	tcs := []struct {
		name string
		args args
		want int
	}{
		{"explicit same event across sessions", args{"C1", ""}, 1},
		{"same session without an event", args{"", "acme"}, 1},
		{"separate events", args{"", ""}, 21},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, ctx := runLedger(t), context.Background()
			run := trace.Trace{ID: "acme", Name: trace.NameRun, Producer: "session", SessionID: tc.args.session,
				Labels: trace.Labels{"event": {tc.args.event}}, Time: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)}
			draft := runItem("acme", &knowledge.RunScope{Producer: "session"})
			draft.Evidence = (knowledge.Evidence{}).Join(run.ID, run.EvidenceUnit())
			_, _, err := l.Propose(ctx, draft)
			require.NoError(t, err)
			for i := range 20 {
				run.ID = fmt.Sprintf("acme-%d", i)
				verdicts := feedback.Records{{TraceID: run.ID, Verdict: feedback.VerdictReject, Reviewer: "ann", Time: run.Time}}
				_, err := l.Corroborate(ctx, "acme", 1, run, verdicts)
				require.NoError(t, err)
			}
			history, err := l.History(ctx, "acme")
			require.NoError(t, err)
			require.Len(t, history, tc.want)
			assert.Equal(t, tc.want, history[0].Evidence.Support())
			assert.Equal(t, knowledge.StatusCandidate, history[0].Status)
			assert.Equal(t, draft.Content, history[0].Content)
		})
	}
}

func TestLegacyEvidenceSupport(t *testing.T) {
	tcs := []struct {
		name string
		args []string
		want int
	}{
		{"empty", nil, 0},
		{"unqualified duplicate references", []string{"acme", "acme"}, 1},
		{"legacy runs lack an independent grouping", []string{"acme", "acme-other"}, 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, (knowledge.Evidence{FeedbackTraceIDs: tc.args}).Support())
		})
	}
}
