package knowledge_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestCheckApprover(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"a person's name passes", "ann", nil},
		{"an empty name is refused", "", knowledge.ErrApproverRequired},
		{"the reserved name is refused", knowledge.ApproverNodloop, knowledge.ErrApproverReserved},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, knowledge.CheckApprover(tc.args), tc.want)
		})
	}
}

// Every operation only a person decides refuses the reserved name and writes nothing
func TestLedgerRefusesReservedApprover(t *testing.T) {
	seeds := newCompactionSeeds()
	candidate := knowledge.Knowledge{
		ID: "k1", Kind: knowledge.KindMeaning, Content: "one", Run: repoRun(),
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}, Author: "author",
	}
	const reserved = knowledge.ApproverNodloop
	tcs := []struct {
		name string
		args func(ctx context.Context, l *knowledge.Ledger, compaction string) error
	}{
		{"approve", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.Approve(ctx, "k1", 1, reserved))
		}},
		{"approve in labels", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.ApproveIn(ctx, "k1", 1, trace.Labels{}, reserved))
		}},
		{"retire", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.Retire(ctx, "a", 1, reserved))
		}},
		{"reaffirm", func(ctx context.Context, l *knowledge.Ledger, _ string) error {
			return testkit.Err(l.Reaffirm(ctx, "a", 1, reserved))
		}},
		{"approve a compaction", func(ctx context.Context, l *knowledge.Ledger, compaction string) error {
			return testkit.Err(approveCompaction(ctx, l, compaction, reserved))
		}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), seeds.at)
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			_, _, err := l.Propose(ctx, candidate)
			require.NoError(t, err)
			proposed, err := l.ProposeCompaction(ctx, "j", seeds.drafts())
			require.NoError(t, err)
			before, err := l.All(ctx)
			require.NoError(t, err)

			err = tc.args(ctx, l, proposed.ID)

			assert.ErrorIs(t, err, knowledge.ErrApproverReserved)
			after, err := l.All(ctx)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}
