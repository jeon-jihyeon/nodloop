package schema_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

func TestParseCursor(t *testing.T) {
	ms := time.Date(2026, 10, 6, 9, 0, 0, 123456789, time.UTC)
	tcs := []struct {
		name string
		args string
		want schema.Cursor
		err  error
	}{
		{"the form of a position names its position", schema.Cursor{Position: "12:af"}.String(), schema.Cursor{Position: "12:af"}, nil},
		{"the form of a time names its record", schema.Cursor{Time: ms, RecordID: "t1"}.String(), schema.Cursor{Time: ms, RecordID: "t1"}, nil},
		{"the form a digest answers names its record", schema.CursorAt(ms, "t2"), schema.Cursor{Time: ms, RecordID: "t2"}, nil},
		{"an empty form is the start", "", schema.Cursor{}, nil},
		{"a form that is no base64 is refused", "%%%", schema.Cursor{}, schema.ErrCursorInvalid},
		{"a form of a time without a record id is refused", "MTIzOg", schema.Cursor{}, schema.ErrCursorInvalid},
		{"a form whose time is no number is refused", "bm9vbjp0MQ", schema.Cursor{}, schema.ErrCursorInvalid},
		{"a form of an empty position is refused", "QA", schema.Cursor{}, schema.ErrCursorInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := schema.ParseCursor(tc.args)

			assert.ErrorIs(t, err, tc.err)
			assert.True(t, tc.want.Time.Equal(got.Time))
			assert.Equal(t, tc.want.RecordID, got.RecordID)
			assert.Equal(t, tc.want.Position, got.Position)
		})
	}
	assert.Empty(t, schema.Cursor{}.String())
}

// The cursor a digest answers after a knowledge record names that record by time so a page from it starts after it
func TestKnowledgeCursor(t *testing.T) {
	k := source().Knowledge.(items)[0]

	got, err := schema.ParseCursor(schema.KnowledgeCursor(k))
	require.NoError(t, err)
	page, err := source().Page(context.Background(), schema.Query{Kind: "knowledge", Since: schema.KnowledgeCursor(k)})
	require.NoError(t, err)

	assert.True(t, k.Time.Equal(got.Time))
	assert.Equal(t, recordID(t, "knowledge", k), got.RecordID)
	assert.Empty(t, page.Records)
}
