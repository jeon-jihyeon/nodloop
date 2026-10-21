package knowledge_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestLedgerApproveInLegacy(t *testing.T) {
	tcs := []struct {
		name string
		args trace.Labels
	}{
		{"everywhere", nil},
		{"a named repository", trace.Labels{"repo": {"nodloop"}}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newRestoreFixture()
			_, _, _, old := f.extras()
			l := f.ledger(t, old)
			before, err := l.All(ctx)
			require.NoError(t, err)

			_, scopeErr := l.ScopeIn(ctx, old.ID, old.Version, tc.args)
			_, err = l.ApproveIn(ctx, old.ID, old.Version, tc.args, "ann")

			assert.ErrorIs(t, scopeErr, knowledge.ErrScopeRequired)
			assert.ErrorIs(t, err, knowledge.ErrScopeRequired)
			after, err := l.All(ctx)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}
