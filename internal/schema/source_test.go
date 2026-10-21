package schema_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

// Records a store appended in order whose position is how many records a read went past
type appended[T any] []T

func (a appended[T]) after(from string, keep func(T) bool, limit int) ([]T, string, error) {
	start := 0
	if from != "" {
		n, err := strconv.Atoi(from)
		if err != nil || n > len(a) {
			return nil, from, fmt.Errorf("%w: %q", record.ErrPositionGone, from)
		}
		start = n
	}
	var out []T
	end := start
	for _, r := range a[start:] {
		if limit > 0 && len(out) == limit {
			break
		}
		end++
		if keep(r) {
			out = append(out, r)
		}
	}
	if end == 0 {
		return out, "", nil
	}
	return out, strconv.Itoa(end), nil
}

func all[T any](T) bool { return true }

type traces appended[record.Trace]

func (ts traces) After(_ context.Context, f record.TraceFilter, from string) ([]record.Trace, string, error) {
	return appended[record.Trace](ts).after(from, f.Matches, f.Limit)
}

type verdicts appended[record.Feedback]

func (vs verdicts) After(_ context.Context, from string, limit int) ([]record.Feedback, string, error) {
	return appended[record.Feedback](vs).after(from, all, limit)
}

type outcomes appended[record.Outcome]

func (os outcomes) After(_ context.Context, from string, limit int) ([]record.Outcome, string, error) {
	return appended[record.Outcome](os).after(from, all, limit)
}

type items appended[record.Knowledge]

func (is items) After(_ context.Context, from string, limit int) ([]record.Knowledge, string, error) {
	return appended[record.Knowledge](is).after(from, all, limit)
}

type guardLog appended[json.RawMessage]

func (g guardLog) After(_ context.Context, from string, limit int) ([]json.RawMessage, string, error) {
	return appended[json.RawMessage](g).after(from, all, limit)
}

// A store that cannot be read after the reads it allows
type unreadable struct {
	traces
	allowed int
	reads   *int
}

var errUnreadable = errors.New("traces.jsonl: permission denied")

func (u unreadable) After(ctx context.Context, f record.TraceFilter, from string) ([]record.Trace, string, error) {
	*u.reads++
	if *u.reads > u.allowed {
		return nil, from, errUnreadable
	}
	return u.traces.After(ctx, f, from)
}

var settled = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

// One record of every store and runs appended out of time order
// Each record holds text a page leaves out
// The last run was appended after the others with an earlier time as a slower process appends one
func source() schema.Source {
	return schema.Source{
		Traces: traces{
			{ID: "t0", Name: "turn", Time: settled, Input: json.RawMessage(`{}`), SchemaVersion: 1},
			{ID: "t1", Name: "run", Time: settled, Input: json.RawMessage(`{"applied":[]}`), Output: json.RawMessage(`"steps one"`), Producer: "bot", SchemaVersion: 1},
			{ID: "t2", Name: "run", Time: settled, Input: json.RawMessage(`{"applied":[]}`), Output: json.RawMessage(`"steps two"`), Producer: "bot", SchemaVersion: 1},
			{ID: "t3", Name: "run", Time: settled.Add(-time.Hour), Input: json.RawMessage(`{"applied":[]}`), Producer: "bot", SchemaVersion: 1},
		},
		Verdicts:  verdicts{{TraceID: "t1", Time: settled, Verdict: "reject", Reason: "missing the window", Reviewer: "ann", Proof: "none", SchemaVersion: 1}},
		Outcomes:  outcomes{{TraceID: "t1", Time: settled, Result: "confirmed", Note: "checked the broker", Reviewer: "ann", SchemaVersion: 1}},
		Knowledge: items{{ID: "k1", Version: 1, Kind: "judgment", Content: "Quote the window", Basis: "stated", Status: "candidate", Time: settled, Author: "ann", SchemaVersion: 1}},
		Guard:     guardLog{json.RawMessage(`{"time":"2026-10-06T09:00:00Z","session_id":"s1","cwd":"/repo","tool":"Bash","veto":"no-rm","action":"block","schema_version":1}`)},
	}
}

// The opaque form of the position of the fake stores after n records
func past(n int) string {
	return schema.Cursor{Position: strconv.Itoa(n)}.String()
}

func TestSourcePageModelErrors(t *testing.T) {
	for _, name := range []string{"extract", "compact", "consolidate"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := record.Trace{ID: "example", Name: name, Time: settled, Input: json.RawMessage(`{}`), Error: "private-model-stderr"}
			if name == "extract" {
				r.Output = json.RawMessage(`{"attempts":[{"error":"private-model-stderr"}]}`)
			}
			s := schema.Source{Traces: traces{r}}

			without, err := s.Page(t.Context(), schema.Query{Kind: name})
			require.NoError(t, err)
			with, err := s.Page(t.Context(), schema.Query{Kind: name, Content: true})
			require.NoError(t, err)

			require.Len(t, without.Records, 1)
			require.Len(t, with.Records, 1)
			assert.NotContains(t, string(without.Records[0]), "private-model-stderr")
			assert.Contains(t, string(with.Records[0]), "private-model-stderr")
		})
	}
}

const (
	runOne      = `{"record_kind":"run","record_id":"t1","id":"t1","name":"run","time":"2026-10-06T09:00:00Z","input":{"applied":[]},"usage":{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_create":0,"cost_usd":0},"duration_ms":0,"producer":"bot","schema_version":1}`
	runTwo      = `{"record_kind":"run","record_id":"t2","id":"t2","name":"run","time":"2026-10-06T09:00:00Z","input":{"applied":[]},"usage":{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_create":0,"cost_usd":0},"duration_ms":0,"producer":"bot","schema_version":1}`
	runLate     = `{"record_kind":"run","record_id":"t3","id":"t3","name":"run","time":"2026-10-06T08:00:00Z","input":{"applied":[]},"usage":{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_create":0,"cost_usd":0},"duration_ms":0,"producer":"bot","schema_version":1}`
	runOneShown = `{"record_kind":"run","record_id":"t1","id":"t1","name":"run","time":"2026-10-06T09:00:00Z","input":{"applied":[]},"output":"steps one","usage":{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_create":0,"cost_usd":0},"duration_ms":0,"producer":"bot","schema_version":1}`
)

// A page holds the records of one kind after its cursor in the order their store appended them in their published schema
// and leaves the text out unless asked
func TestSourcePage(t *testing.T) {
	verdictID := recordID(t, "verdict", source().Verdicts.(verdicts)[0])
	guardID := recordID(t, "guard", source().Guard.(guardLog)[0])
	type want struct {
		records []string
		next    string
	}
	tcs := []struct {
		name string
		args schema.Query
		want want
	}{
		{"the first page starts at the first run appended and leaves its output out", schema.Query{Kind: "run", Limit: 1}, want{[]string{runOne}, past(2)}},
		{"a cursor goes on after its run and takes a run appended late with an earlier time", schema.Query{Kind: "run", Since: past(2)},
			want{[]string{runTwo, runLate}, past(4)}},
		{"content keeps the output", schema.Query{Kind: "run", Limit: 1, Content: true}, want{[]string{runOneShown}, past(2)}},
		{"a page past the last record answers the cursor it was given", schema.Query{Kind: "run", Since: past(4)}, want{[]string{}, past(4)}},
		{"a kind with no record answers an empty page and the cursor past what it read", schema.Query{Kind: "answer"}, want{[]string{}, past(4)}},
		{"a cursor of a time keeps the records after it by time and answers a position", schema.Query{Kind: "run", Since: schema.CursorAt(settled, "t1")},
			want{[]string{runTwo}, past(4)}},
		{"a cursor of a time with more records than the limit answers the position after the last of the page",
			schema.Query{Kind: "run", Since: schema.CursorAt(settled.Add(-2*time.Hour), "t0"), Limit: 1}, want{[]string{runOne}, past(2)}},
		{"a verdict leaves its reason out", schema.Query{Kind: "verdict"}, want{[]string{
			`{"record_kind":"verdict","record_id":"` + verdictID + `","trace_id":"t1","time":"2026-10-06T09:00:00Z","verdict":"reject","reviewer":"ann","proof":"none","schema_version":1}`,
		}, past(1)}},
		{"a knowledge record leaves its content out", schema.Query{Kind: "knowledge", Limit: 5000}, want{[]string{
			`{"record_kind":"knowledge","record_id":"` + recordID(t, "knowledge", source().Knowledge.(items)[0]) + `","id":"k1","version":1,"kind":"judgment","evidence":{},"basis":"stated","status":"candidate","time":"2026-10-06T09:00:00Z","author":"ann","schema_version":1}`,
		}, past(1)}},
		{"a guard entry leaves its cwd out", schema.Query{Kind: "guard"}, want{[]string{
			`{"record_kind":"guard","record_id":"` + guardID + `","time":"2026-10-06T09:00:00Z","session_id":"s1","tool":"Bash","veto":"no-rm","action":"block","schema_version":1}`,
		}, past(1)}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			page, err := source().Page(context.Background(), tc.args)

			require.NoError(t, err)
			assert.Equal(t, tc.want.records, texts(page.Records))
			assert.Equal(t, tc.want.next, page.Next)
		})
	}
}

// Paging one record at a time takes each run once and in the order the store appended them
func TestSourcePageWalk(t *testing.T) {
	var walked []string
	since := ""
	for range 5 {
		page, err := source().Page(context.Background(), schema.Query{Kind: "run", Since: since, Limit: 1})
		require.NoError(t, err)
		walked = append(walked, ids(t, page.Records)...)
		since = page.Next
	}

	assert.Equal(t, []string{"t1", "t2", "t3"}, walked)
}

// An outcome leaves its note out and every page validates against the published schema of its kind
func TestSourcePageValidates(t *testing.T) {
	kinds := []string{"run", "turn", "verdict", "outcome", "knowledge", "guard"}
	for _, name := range kinds {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			k := kind(t, name)

			page, err := source().Page(context.Background(), schema.Query{Kind: name, Limit: 1})

			require.NoError(t, err)
			require.Len(t, page.Records, 1)
			assert.NoError(t, k.Validate(schema.Record{Kind: name, JSON: page.Records[0]}))
			assert.NotContains(t, string(page.Records[0]), "checked the broker")
		})
	}
}

// A page fails on an unknown kind, a cursor that names no record, a store the source lacks, a store that cannot be read and a record of no shape of its store
func TestSourcePageRefuses(t *testing.T) {
	bare := schema.Source{Traces: traces{{Name: "run", Time: settled}}}
	failing := func(allowed int) schema.Source {
		return schema.Source{Traces: unreadable{traces: source().Traces.(traces), allowed: allowed, reads: new(int)}}
	}
	early := schema.CursorAt(settled.Add(-2*time.Hour), "t0")
	tcs := []struct {
		name string
		args pageArgs
		want error
	}{
		{"an unknown kind", pageArgs{source(), schema.Query{Kind: "session"}}, schema.ErrKindUnknown},
		{"a cursor that is no form of one", pageArgs{source(), schema.Query{Kind: "run", Since: "%%%"}}, schema.ErrCursorInvalid},
		{"a position the store no longer holds", pageArgs{source(), schema.Query{Kind: "run", Since: past(9)}}, schema.ErrCursorInvalid},
		{"the guard of a source without a guard log", pageArgs{schema.Source{}, schema.Query{Kind: "guard"}}, schema.ErrNotServed},
		{"the runs of a source without traces", pageArgs{schema.Source{}, schema.Query{Kind: "run"}}, schema.ErrNotServed},
		{"the verdicts of a source without verdicts", pageArgs{schema.Source{}, schema.Query{Kind: "verdict"}}, schema.ErrNotServed},
		{"the outcomes of a source without outcomes", pageArgs{schema.Source{}, schema.Query{Kind: "outcome"}}, schema.ErrNotServed},
		{"the knowledge of a source without knowledge", pageArgs{schema.Source{}, schema.Query{Kind: "knowledge"}}, schema.ErrNotServed},
		{"a store that cannot be read", pageArgs{failing(0), schema.Query{Kind: "run"}}, errUnreadable},
		{"a store that cannot be read after a cursor of a time", pageArgs{failing(0), schema.Query{Kind: "run", Since: early}}, errUnreadable},
		{"a store that fails the read of the position after a page of a cursor of a time", pageArgs{failing(1), schema.Query{Kind: "run", Since: early, Limit: 1}},
			errUnreadable},
		{"a trace without an id", pageArgs{bare, schema.Query{Kind: "run"}}, schema.ErrRecordInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.args.source.Page(context.Background(), tc.args.query)

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// A store of no known kind serves nothing
func TestSourceReadRefuses(t *testing.T) {
	_, _, err := source().Read(context.Background(), schema.Kind{Name: "run", Store: "runs.csv"}, schema.Cursor{}, 0)

	assert.ErrorIs(t, err, schema.ErrStoreUnknown)
}

func TestSourceReadUnlimitedTimeCursor(t *testing.T) {
	for _, limit := range []int{0, -1} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			t.Parallel()
			after := schema.Cursor{Time: settled.Add(-2 * time.Hour), RecordID: "t0"}

			records, _, err := source().Read(t.Context(), kind(t, "run"), after, limit)

			require.NoError(t, err)
			assert.Len(t, records, 3)
		})
	}
}

// A limit past the most one page holds is cut to it and no limit holds the default
func TestSourcePageLimit(t *testing.T) {
	var many traces
	for i := range schema.MaxLimit + 1 {
		many = append(many, record.Trace{ID: fmt.Sprintf("t%04d", i), Name: "turn", Time: settled, Input: json.RawMessage(`{}`)})
	}
	tcs := []struct {
		name string
		args int
		want int
	}{
		{"no limit holds the default", 0, schema.DefaultLimit},
		{"a negative limit holds the default", -1, schema.DefaultLimit},
		{"a limit within the most is kept", 7, 7},
		{"a limit past the most is cut to it", schema.MaxLimit + 1, schema.MaxLimit},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			page, err := schema.Source{Traces: many}.Page(context.Background(), schema.Query{Kind: "turn", Limit: tc.args})

			require.NoError(t, err)
			assert.Len(t, page.Records, tc.want)
		})
	}
}

// A source with the query it is asked
type pageArgs struct {
	source schema.Source
	query  schema.Query
}

func recordID(t *testing.T, name string, stored any) string {
	t.Helper()
	r, err := kind(t, name).Record(stored)
	require.NoError(t, err)
	return r.ID
}

func texts(rs []json.RawMessage) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, string(r))
	}
	return out
}

// A knowledge record appended after a page with a time before that page still comes on the next page once
// An import of a ledger of another directory keeps the times its records had there
func TestSourcePageAfterImport(t *testing.T) {
	ctx := context.Background()
	store, err := knowledgefile.New(t.TempDir())
	require.NoError(t, err)
	source := schema.Source{Knowledge: store}
	recent := record.Knowledge{ID: "k1", Version: 1, Kind: "judgment", Basis: "stated", Status: "candidate", Time: settled, Author: "ann"}
	imported := record.Knowledge{ID: "k0", Version: 1, Kind: "judgment", Basis: "stated", Status: "candidate", Time: settled.AddDate(-1, 0, 0), Author: "ann"}
	require.NoError(t, store.Append(ctx, recent))
	first, err := source.Page(ctx, schema.Query{Kind: "knowledge"})
	require.NoError(t, err)
	require.NoError(t, store.Append(ctx, imported))

	next, err := source.Page(ctx, schema.Query{Kind: "knowledge", Since: first.Next})
	require.NoError(t, err)
	again, err := source.Page(ctx, schema.Query{Kind: "knowledge", Since: next.Next})
	require.NoError(t, err)

	assert.Equal(t, []string{recordID(t, "knowledge", recent)}, ids(t, first.Records))
	assert.Equal(t, []string{recordID(t, "knowledge", imported)}, ids(t, next.Records))
	assert.Empty(t, again.Records)
}

func ids(t *testing.T, rs []json.RawMessage) []string {
	t.Helper()
	var out []string
	for _, r := range rs {
		var head struct {
			ID string `json:"record_id"`
		}
		require.NoError(t, json.Unmarshal(r, &head))
		out = append(out, head.ID)
	}
	return out
}
