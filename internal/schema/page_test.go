package schema_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

// Records of one millisecond page by record_id and a record younger than the settle time waits for a later page
func TestRecordsPage(t *testing.T) {
	ms := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	now := ms.Add(time.Minute)
	rec := func(id string, at time.Time) schema.Record { return schema.Record{Kind: "run", ID: id, Time: at} }
	// Listed out of order as two stores would hand them over
	old, a, b, c := rec("old", ms.Add(-time.Hour)), rec("a", ms), rec("b", ms), rec("c", ms)
	all := schema.Records{c, a, rec("young", now.Add(-time.Second)), b, old}
	type args struct {
		after schema.Cursor
		limit int
	}
	type want struct {
		page schema.Records
		next schema.Cursor
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the first page starts at the oldest record", args{schema.Cursor{}, 2},
			want{schema.Records{old, a}, a.Cursor()}},
		{"a cursor inside one millisecond goes on by record_id", args{schema.Cursor{Time: ms, RecordID: "a"}, 1},
			want{schema.Records{b}, b.Cursor()}},
		{"no limit holds every settled record", args{schema.Cursor{}, 0},
			want{schema.Records{old, a, b, c}, c.Cursor()}},
		{"a page past the settled records is empty and keeps the cursor", args{schema.Cursor{Time: ms, RecordID: "c"}, 5},
			want{schema.Records{}, c.Cursor()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			page, next := all.Page(tc.args.after, tc.args.limit, now)

			assert.Equal(t, tc.want.page, page)
			assert.Equal(t, tc.want.next, next)
		})
	}
}

// Paging one record at a time through records of one millisecond returns each once
func TestRecordsPageWalk(t *testing.T) {
	ms := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	all := schema.Records{{ID: "b", Time: ms}, {ID: "a", Time: ms}, {ID: "c", Time: ms}}
	var walked []string
	after := schema.Cursor{}
	for range len(all) + 1 {
		page, next := all.Page(after, 1, ms.Add(time.Hour))
		for _, r := range page {
			walked = append(walked, r.ID)
		}
		parsed, err := schema.ParseCursor(next.String())
		require.NoError(t, err)
		after = parsed
	}

	assert.Equal(t, []string{"a", "b", "c"}, walked)
}

func TestParseCursor(t *testing.T) {
	ms := time.Date(2026, 10, 6, 9, 0, 0, 123456789, time.UTC)
	type want struct {
		cursor schema.Cursor
		err    error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"the form of a cursor names its record", schema.Cursor{Time: ms, RecordID: "t1"}.String(), want{cursor: schema.Cursor{Time: ms, RecordID: "t1"}}},
		{"an empty form is the start", "", want{}},
		{"a form that is no base64 is refused", "%%%", want{err: schema.ErrCursorInvalid}},
		{"a form without a record id is refused", schema.Cursor{Time: ms}.String(), want{err: schema.ErrCursorInvalid}},
		{"a form whose time is no number is refused", "bm9vbjp0MQ", want{err: schema.ErrCursorInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := schema.ParseCursor(tc.args)

			assert.ErrorIs(t, err, tc.want.err)
			assert.True(t, tc.want.cursor.Time.Equal(got.Time))
			assert.Equal(t, tc.want.cursor.RecordID, got.RecordID)
		})
	}
	assert.Empty(t, schema.Cursor{}.String())
}
