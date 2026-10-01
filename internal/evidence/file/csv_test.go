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
		// The declaration of the policy
		// Nil reads as the default five
		contexts evidence.Contexts
	}
	type want struct {
		event evidence.Event
		err   error
		// A fragment of the message naming the file and line
		text string
	}
	declared := evidence.Contexts{{Name: "deploy", BreaksBaseline: true}, {Name: evidence.ContextUnknown}}
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
			name: "a value that is not a number is rejected with the line and column",
			args: args{files: map[string]string{"events.csv": header + "e,2026-09-22T10:00:00Z,m,NaN\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: `events.csv line 2 column value: not a finite number "NaN"`},
		},
		{
			name: "a positive infinite value is rejected with the line and column",
			args: args{files: map[string]string{"events.csv": header + "e,2026-09-22T10:00:00Z,m,+Inf\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: `events.csv line 2 column value: not a finite number "+Inf"`},
		},
		{
			name: "a negative infinite value is rejected with the line and column",
			args: args{files: map[string]string{"events.csv": header + "e,2026-09-22T10:00:00Z,m,-Infinity\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: `events.csv line 2 column value: not a finite number "-Infinity"`},
		},
		{
			name: "a repeated point with the same value loads once",
			args: args{files: map[string]string{"events.csv": events + "e,2026-09-22T10:00:00Z,m,1\n"}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextUnknown, Points: point}},
		},
		{
			name: "the same instant spelled with an offset is the same point",
			args: args{files: map[string]string{"events.csv": events + "e,2026-09-22T19:00:00+09:00,m,1\n"}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextUnknown, Points: point}},
		},
		{
			name: "a repeated point with another value names both lines",
			args: args{files: map[string]string{"events.csv": events + "e,2026-09-22T19:00:00+09:00,m,2\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: `events.csv line 3 repeats line 2 with another value: event "e" series "m"`},
		},
		{
			name: "the same instant in another series or event is a point of its own",
			args: args{files: map[string]string{"events.csv": "event_id,timestamp,metric,value,source\n" +
				"e,2026-09-22T10:00:00Z,m,1,a\n" +
				"e,2026-09-22T10:00:00Z,m,1,b\n" +
				"e,2026-09-22T10:00:00Z,n,1,a\n" +
				"f,2026-09-22T10:00:00Z,m,1,a\n"}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextUnknown, Points: []evidence.Point{
				{Time: at, Metric: "m", Value: 1, Dims: map[string]string{"source": "a"}},
				{Time: at, Metric: "m", Value: 1, Dims: map[string]string{"source": "b"}},
				{Time: at, Metric: "n", Value: 1, Dims: map[string]string{"source": "a"}},
			}}},
		},
		{
			name: "a byte order mark before the header is dropped",
			args: args{files: map[string]string{"events.csv": "\uFEFF" + events}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextUnknown, Points: point}},
		},
		{
			name: "a byte order mark before a quoted header is dropped",
			args: args{files: map[string]string{"events.csv": "\uFEFF\"event_id\",timestamp,metric,value\n" +
				"e,2026-09-22T10:00:00Z,m,1\n"}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextUnknown, Points: point}},
		},
		{
			name: "a byte order mark before a dimension column leaves the dimension name clean",
			args: args{files: map[string]string{"events.csv": "\uFEFFsource,event_id,timestamp,metric,value\n" +
				"a,e,2026-09-22T10:00:00Z,m,1\n"}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextUnknown, Points: []evidence.Point{
				{Time: at, Metric: "m", Value: 1, Dims: map[string]string{"source": "a"}},
			}}},
		},
		{
			name: "a byte order mark before the contexts header is dropped",
			args: args{files: map[string]string{
				"events.csv":   events,
				"contexts.csv": "\uFEFF" + contexts + "e,planned_operational_change\n",
			}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextPlannedChange, Points: point}},
		},
		{
			name: "a column without a name is not a dimension",
			args: args{files: map[string]string{"events.csv": "event_id,timestamp,metric,value,\n" +
				"e,2026-09-22T10:00:00Z,m,1,\n"}, id: "e"},
			want: want{event: evidence.Event{ID: "e", ChangeContext: evidence.ContextUnknown, Points: point}},
		},
		{
			name: "a repeated column name names the column",
			args: args{files: map[string]string{"events.csv": "event_id,timestamp,metric,value,source,source\n" +
				"e,2026-09-22T10:00:00Z,m,1,a,b\n"}, id: "e"},
			want: want{err: evidence.ErrMalformed, text: `events.csv column 6 repeats column "source"`},
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
			want: want{err: evidence.ErrUnknownContext, text: `unknown change context: contexts.csv line 2: "bogus" is not one of [no_known_change`},
		},
		{
			name: "a context the policy declares sets the change context",
			args: args{files: map[string]string{"events.csv": events, "contexts.csv": contexts + "e,deploy\n"}, id: "e", contexts: declared},
			want: want{event: evidence.Event{ID: "e", ChangeContext: "deploy", Points: point}},
		},
		{
			name: "a default context the policy does not declare names the declared ones",
			args: args{
				files: map[string]string{"events.csv": events, "contexts.csv": contexts + "e,measurement_context_changed\n"}, id: "e", contexts: declared,
			},
			want: want{err: evidence.ErrUnknownContext, text: `"measurement_context_changed" is not one of [deploy unknown]`},
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
			contexts := tc.args.contexts
			if contexts == nil {
				contexts = evidence.DefaultContexts()
			}
			src, err := file.New(dir, contexts)
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
				{ID: "e2", Start: at, End: at.Add(2 * time.Hour), Dims: map[string][]string{}},
				{ID: "e1", Start: at.Add(time.Hour), End: at.Add(time.Hour), Dims: map[string][]string{}},
			}},
		},
		{
			name: "refs carry the dimension values of their own event",
			args: map[string]string{"events.csv": "event_id,timestamp,source,metric,value\n" +
				"e1,2026-09-22T10:00:00Z,b,m,1\n" +
				"e1,2026-09-22T10:00:00Z,a,m,1\n" +
				"e2,2026-09-22T10:00:00Z,c,m,1\n"},
			want: want{refs: []evidence.EventRef{
				{ID: "e1", Start: at, End: at, Dims: map[string][]string{"source": {"a", "b"}}},
				{ID: "e2", Start: at, End: at, Dims: map[string][]string{"source": {"c"}}},
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
			want: want{refs: []evidence.EventRef{{ID: "e", Start: at, End: at, Dims: map[string][]string{}}}},
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
			src, err := file.New(dir, evidence.DefaultContexts())
			require.NoError(t, err)
			got, err := src.Events(ctx)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.refs, got)
		})
	}
}

func TestSourceAll(t *testing.T) {
	at := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	const events = "event_id,timestamp,metric,value\ne2,2026-09-22T10:00:00Z,m,1\ne1,2026-09-22T10:00:00Z,m,2\n"
	type want struct {
		// Change context per event in first seen order
		contexts []evidence.Context
		err      error
	}
	tcs := []struct {
		name string
		args map[string]string
		want want
	}{
		{
			"every event carries its context and one without a row reads unknown",
			map[string]string{"events.csv": events, "contexts.csv": "event_id,change_context\ne1,deploy\n"},
			want{contexts: []evidence.Context{evidence.ContextUnknown, "deploy"}},
		},
		{"no contexts file reads unknown everywhere", map[string]string{"events.csv": events}, want{contexts: []evidence.Context{evidence.ContextUnknown, evidence.ContextUnknown}}},
		{
			"an undeclared context fails",
			map[string]string{"events.csv": events, "contexts.csv": "event_id,change_context\ne1,campaign\n"},
			want{err: evidence.ErrUnknownContext},
		},
	}
	declared := evidence.Contexts{{Name: "deploy"}, {Name: evidence.ContextUnknown}}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range tc.args {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}
			src, err := file.New(dir, declared)
			require.NoError(t, err)

			got, err := src.All(t.Context())

			require.ErrorIs(t, err, tc.want.err)
			if tc.want.err != nil {
				return
			}
			var contexts []evidence.Context
			for _, ev := range got {
				contexts = append(contexts, ev.ChangeContext)
			}
			assert.Equal(t, tc.want.contexts, contexts)
			assert.Equal(t, []evidence.Point{{Time: at, Metric: "m", Value: 2}}, got[1].Points)
		})
	}
}
