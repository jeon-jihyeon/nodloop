// Package evidencetest is the contract suite every evidence source must pass
package evidencetest

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Every read the suite makes
type Store interface {
	Events(ctx context.Context) ([]evidence.EventRef, error)
	Event(ctx context.Context, id string) (evidence.Event, error)
	Paragraphs(ctx context.Context) ([]evidence.Paragraph, error)
	Labels(ctx context.Context) ([]evidence.Label, error)
}

// What the seeded data must contain for the suite to check ids and paragraphs and labels
type Seed struct {
	EventID       string
	MinPoints     int
	ChangeContext evidence.Context
	ParagraphID   evidence.ParagraphID
	// An event that has a label whose paragraph ids all exist
	LabeledEventID string
}

// Runs against a read only Store seeded with the given data
func Run(t *testing.T, src Store, seed Seed) {
	t.Helper()
	ctx := context.Background()

	t.Run("events", func(t *testing.T) {
		t.Parallel()
		refs, err := src.Events(ctx)
		require.NoError(t, err)
		seeded := func(r evidence.EventRef) bool { return r.ID == seed.EventID }
		i := slices.IndexFunc(refs, seeded)
		require.GreaterOrEqual(t, i, 0, "event %s missing from %+v", seed.EventID, refs)
		assert.False(t, refs[i].End.Before(refs[i].Start), "end before start: %+v", refs[i])
	})

	t.Run("event", func(t *testing.T) {
		t.Parallel()
		ev, err := src.Event(ctx, seed.EventID)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(ev.Points), seed.MinPoints)
		assert.Equal(t, seed.ChangeContext, ev.ChangeContext)
		assert.True(t, slices.IsSortedFunc(ev.Points, evidence.Point.Compare), "points not sorted")
	})

	t.Run("unknown_event", func(t *testing.T) {
		t.Parallel()
		ev, err := src.Event(ctx, "no-such-event")
		assert.ErrorIs(t, err, evidence.ErrNotFound)
		assert.Zero(t, ev)
	})

	t.Run("paragraphs", func(t *testing.T) {
		t.Parallel()
		first, err := src.Paragraphs(ctx)
		require.NoError(t, err)
		second, err := src.Paragraphs(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, first)
		assert.Equal(t, paragraphs(first).ids(), paragraphs(second).ids(), "ids changed between runs")
		assert.Contains(t, paragraphs(first).ids(), seed.ParagraphID)
	})

	t.Run("labels_cite_existing_paragraphs", func(t *testing.T) {
		t.Parallel()
		labels, err := src.Labels(ctx)
		require.NoError(t, err)
		all, err := src.Paragraphs(ctx)
		require.NoError(t, err)
		known := paragraphs(all).ids()
		labeled := func(l evidence.Label) bool { return l.EventID == seed.LabeledEventID }
		assert.True(t, slices.ContainsFunc(labels, labeled), "label for %s missing", seed.LabeledEventID)
		for _, l := range labels {
			assert.True(t, l.Type.Valid(), "label %s has type %q", l.EventID, l.Type)
			assert.False(t, l.IsHold() && len(l.Paragraphs) > 0, "hold label %s cites paragraphs", l.EventID)
			cited := slices.Concat(l.Paragraphs, l.RequiredChecks)
			assert.Subset(t, known, cited, "label %s cites an unknown paragraph", l.EventID)
		}
	})
}

type paragraphs []evidence.Paragraph

func (ps paragraphs) ids() []evidence.ParagraphID {
	ids := make([]evidence.ParagraphID, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return ids
}
