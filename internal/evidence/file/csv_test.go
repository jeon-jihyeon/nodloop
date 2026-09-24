package file_test

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/evidence/file"
)

func TestSourceEvent(t *testing.T) {
	at := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	const header = "event_id,timestamp,metric,value\n"
	const events = header + "e,2026-09-22T10:00:00Z,m,1\n"
	point := []evidence.Point{{Time: at, Metric: "m", Value: 1}}
	dims := map[string]string{"source": "a", "topic": "shopping"}
	const contexts = "event_id,change_context\n"
	type args struct {
		files map[string]string
		// Mode per file name
		// Unlisted files are written readable
		modes map[string]os.FileMode
		id    string
	}
	type want struct {
		event evidence.Event
		err   error
		// A fragment of the message naming the file and line
		text string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "points are sorted by time and extra columns become dims",
			args: args{
				files: map[string]string{"events.csv": "event_id,timestamp,source,metric,value,topic\n" +
					"e1,2026-09-22T11:00:00Z,a,click_count,10,shopping\n" +
					"e1,2026-09-22T10:00:00Z,a,click_count,9,shopping\n" +
					"e2,2026-09-22T10:00:00Z,b,conversion_count,1,finance\n"},
				id: "e1",
			},
			want: want{event: evidence.Event{ID: "e1", ChangeContext: evidence.ContextUnknown, Points: []evidence.Point{
				{Time: at, Metric: "click_count", Value: 9, Dims: dims},
				{Time: at.Add(time.Hour), Metric: "click_count", Value: 10, Dims: dims},
			}}},
		},
		{
			name: "file without dimension columns gives points without dims",
			args: args{files: map[string]string{"events.csv": events}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextUnknown, Points: point}},
		},
		{
			name: "timestamps with an offset are stored in UTC",
			args: args{files: map[string]string{"events.csv": header + "e,2026-09-22T19:00:00+09:00,m,1\n"}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextUnknown, Points: point}},
		},
		{
			name: "contexts row sets the change context whatever the column order",
			args: args{
				files: map[string]string{
					"events.csv":   events,
					"contexts.csv": "change_context,event_id\nplanned_operational_change,e\nno_known_change,other\n",
				},
				id: "e",
			},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextPlannedChange, Points: point}},
		},
		{
			name: "unknown id is not found",
			args: args{files: map[string]string{"events.csv": events}, id: "missing"},
			want: want{err: evidence.ErrNotFound, text: `event "missing"`},
		},
		{
			name: "empty events file has no header",
			args: args{files: map[string]string{"events.csv": ""}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: "events.csv missing header: EOF"},
		},
		{
			name: "events file without a required column",
			args: args{files: map[string]string{"events.csv": "event_id,timestamp,metric\ne,2026-09-22T10:00:00Z,m\n"},
				id: "e"},
			want: want{err: evidence.ErrMalformed, text: `events.csv missing column "value"`},
		},
		{
			name: "unparsable timestamp names the line and column",
			args: args{files: map[string]string{"events.csv": header + "e,yesterday,m,1\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: "events.csv line 2 column timestamp"},
		},
		{
			name: "unparsable value names the line and column",
			args: args{files: map[string]string{"events.csv": header + "e,2026-09-22T10:00:00Z,m,high\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: "events.csv line 2 column value"},
		},
		{
			name: "empty event id names the line",
			args: args{files: map[string]string{"events.csv": header + ",2026-09-22T10:00:00Z,m,1\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: "events.csv line 2 empty event_id"},
		},
		{
			name: "ragged row names the line",
			args: args{files: map[string]string{"events.csv": header + "e,2026-09-22T10:00:00Z\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: "events.csv line 2: wrong number of fields"},
		},
		{
			name: "a quoted field spanning lines keeps the line count",
			args: args{files: map[string]string{"events.csv": "event_id,timestamp,metric,value,note\n" +
				"e,2026-09-22T10:00:00Z,m,1,\"two\nlines\"\n" +
				"e,yesterday,m,1,x\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: "events.csv line 4 column timestamp"},
		},
		{
			name: "empty contexts file has no header",
			args: args{files: map[string]string{"events.csv": events, "contexts.csv": ""}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: "contexts.csv missing header: EOF"},
		},
		{
			name: "contexts file without a required column",
			args: args{files: map[string]string{"events.csv": events, "contexts.csv": "event_id\ne\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: `contexts.csv missing column "change_context"`},
		},
		{
			name: "ragged contexts row names the line",
			args: args{files: map[string]string{"events.csv": events, "contexts.csv": contexts + "e\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: "contexts.csv line 2: wrong number of fields"},
		},
		{
			name: "context outside the valid set names the line",
			args: args{files: map[string]string{"events.csv": events, "contexts.csv": contexts + "e,bogus\n"}, id: "e"},
			want: want{err: evidence.ErrUnknownContext, text: `unknown change context: contexts.csv line 2: "bogus"`},
		},
		{
			name: "empty event id in contexts names the line",
			args: args{files: map[string]string{"events.csv": events, "contexts.csv": contexts + ",unknown\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: "contexts.csv line 2 empty event_id"},
		},
		{
			name: "repeated event id in contexts names the line",
			args: args{files: map[string]string{
				"events.csv":   events,
				"contexts.csv": contexts + "e,no_known_change\ne,unknown\n",
			}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: `contexts.csv line 3 repeats event_id "e"`},
		},
		{
			name: "unreadable contexts file fails",
			args: args{
				files: map[string]string{"events.csv": events, "contexts.csv": ""},
				modes: map[string]os.FileMode{"contexts.csv": 0o200},
				id:    "e",
			},
			want: want{err: os.ErrPermission, text: "contexts.csv"},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args.files {
				mode := cmp.Or(tc.args.modes[name], os.FileMode(0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), mode))
			}
			src, err := file.New(dir)
			require.NoError(t, err)
			got, err := src.Event(ctx, tc.args.id)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, fmt.Sprint(err), tc.want.text)
			assert.Equal(t, tc.want.event, got)
		})
	}
}

func TestSourceEvents(t *testing.T) {
	at := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	const header = "event_id,timestamp,metric,value\n"
	type want struct {
		refs []evidence.EventRef
		err  error
	}
	tcs := []struct {
		name string
		args map[string]string
		want want
	}{
		{
			name: "refs keep first seen order and span each event",
			args: map[string]string{"events.csv": header +
				"e2,2026-09-22T12:00:00Z,m,1\n" +
				"e1,2026-09-22T11:00:00Z,m,1\n" +
				"e2,2026-09-22T10:00:00Z,m,1\n"},
			want: want{refs: []evidence.EventRef{
				{ID: "e2", Start: at, End: at.Add(2 * time.Hour)},
				{ID: "e1", Start: at.Add(time.Hour), End: at.Add(time.Hour)},
			}},
		},
		{
			name: "header only file has no events",
			args: map[string]string{"events.csv": header},
			want: want{refs: []evidence.EventRef{}},
		},
		{
			name: "missing events file fails",
			args: map[string]string{},
			want: want{err: os.ErrNotExist},
		},
		{
			name: "a malformed contexts file does not stop the list",
			args: map[string]string{
				"events.csv":   header + "e,2026-09-22T10:00:00Z,m,1\n",
				"contexts.csv": "event_id,change_context\ne,bogus\n",
			},
			want: want{refs: []evidence.EventRef{{ID: "e", Start: at, End: at}}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}
			src, err := file.New(dir)
			require.NoError(t, err)
			got, err := src.Events(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.refs, got)
		})
	}
}
