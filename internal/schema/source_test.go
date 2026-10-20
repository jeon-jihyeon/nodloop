package schema_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

// Traces a store lists through its filter
type traces []record.Trace

func (ts traces) List(_ context.Context, f record.TraceFilter) ([]record.Trace, error) {
	var out []record.Trace
	for _, t := range ts {
		if f.Matches(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

type verdicts []record.Feedback

func (vs verdicts) List(_ context.Context, f record.FeedbackFilter) ([]record.Feedback, error) {
	var out []record.Feedback
	for _, v := range vs {
		if f.Matches(v) {
			out = append(out, v)
		}
	}
	return out, nil
}

type outcomes []record.Outcome

func (os outcomes) List(context.Context, string) ([]record.Outcome, error) { return os, nil }

type items []record.Knowledge

func (is items) List(context.Context) ([]record.Knowledge, error) { return is, nil }

type guardLog []json.RawMessage

func (g guardLog) List(context.Context) ([]json.RawMessage, error) { return g, nil }

// A store that cannot be read
type unreadable struct{}

var errUnreadable = errors.New("traces.jsonl: permission denied")

func (unreadable) List(context.Context, record.TraceFilter) ([]record.Trace, error) {
	return nil, errUnreadable
}

var (
	settled = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	now     = settled.Add(time.Minute)
)

// One record of every store a minute old and a run younger than the settle time, each with text a page leaves out
func source() schema.Source {
	return schema.Source{
		Traces: traces{
			{ID: "t2", Name: "run", Time: settled, Input: json.RawMessage(`{"applied":[]}`), Output: json.RawMessage(`"steps two"`), Producer: "bot", SchemaVersion: 1},
			{ID: "t1", Name: "run", Time: settled, Input: json.RawMessage(`{"applied":[]}`), Output: json.RawMessage(`"steps one"`), Producer: "bot", SchemaVersion: 1},
			{ID: "t0", Name: "turn", Time: settled, Input: json.RawMessage(`{}`), SchemaVersion: 1},
			{ID: "t3", Name: "run", Time: now.Add(-time.Second), Input: json.RawMessage(`{"applied":[]}`), Producer: "bot", SchemaVersion: 1},
		},
		Verdicts:  verdicts{{TraceID: "t1", Time: settled, Verdict: "reject", Reason: "missing the window", Reviewer: "ann", Proof: "none", SchemaVersion: 1}},
		Outcomes:  outcomes{{TraceID: "t1", Time: settled, Result: "confirmed", Note: "checked the broker", Reviewer: "ann", SchemaVersion: 1}},
		Knowledge: items{{ID: "k1", Version: 1, Kind: "judgment", Content: "Quote the window", Basis: "stated", Status: "candidate", Time: settled, Author: "ann", SchemaVersion: 1}},
		Guard:     guardLog{json.RawMessage(`{"time":"2026-10-06T09:00:00Z","session_id":"s1","cwd":"/repo","tool":"Bash","veto":"no-rm","action":"block","schema_version":1}`)},
	}
}

const (
	runOne      = `{"record_kind":"run","record_id":"t1","id":"t1","name":"run","time":"2026-10-06T09:00:00Z","input":{"applied":[]},"usage":{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_create":0,"cost_usd":0},"duration_ms":0,"producer":"bot","schema_version":1}`
	runTwo      = `{"record_kind":"run","record_id":"t2","id":"t2","name":"run","time":"2026-10-06T09:00:00Z","input":{"applied":[]},"usage":{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_create":0,"cost_usd":0},"duration_ms":0,"producer":"bot","schema_version":1}`
	runOneShown = `{"record_kind":"run","record_id":"t1","id":"t1","name":"run","time":"2026-10-06T09:00:00Z","input":{"applied":[]},"output":"steps one","usage":{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_create":0,"cost_usd":0},"duration_ms":0,"producer":"bot","schema_version":1}`
)

// A page holds the settled records of one kind after its cursor in their published schema and leaves the text out unless asked
func TestSourcePage(t *testing.T) {
	ix, err := schema.Published.Index()
	require.NoError(t, err)
	verdict, err := ix.Kind("verdict")
	require.NoError(t, err)
	verdictID, err := verdict.Record(source().Verdicts.(verdicts)[0])
	require.NoError(t, err)
	t1 := schema.Cursor{Time: settled, RecordID: "t1"}.String()
	t2 := schema.Cursor{Time: settled, RecordID: "t2"}.String()
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
		{"the first page starts at the oldest run and leaves its output out", schema.Query{Kind: "run", Limit: 1}, want{[]string{runOne}, t1}},
		{"a cursor goes on after its run and holds back a run younger than the settle time", schema.Query{Kind: "run", Since: t1}, want{[]string{runTwo}, t2}},
		{"content keeps the output", schema.Query{Kind: "run", Limit: 1, Content: true}, want{[]string{runOneShown}, t1}},
		{"a page past the settled records keeps the cursor it was given", schema.Query{Kind: "run", Since: t2}, want{[]string{}, t2}},
		{"a kind with no record answers an empty page and an empty cursor", schema.Query{Kind: "answer"}, want{[]string{}, ""}},
		{"a verdict leaves its reason out", schema.Query{Kind: "verdict"}, want{[]string{
			`{"record_kind":"verdict","record_id":"` + verdictID.ID + `","trace_id":"t1","time":"2026-10-06T09:00:00Z","verdict":"reject","reviewer":"ann","proof":"none","schema_version":1}`,
		}, verdictID.Cursor().String()}},
		{"a knowledge record leaves its content out", schema.Query{Kind: "knowledge", Limit: 5000}, want{[]string{
			`{"record_kind":"knowledge","record_id":"` + recordID(t, "knowledge", source().Knowledge.(items)[0]) + `","id":"k1","version":1,"kind":"judgment","evidence":{},"basis":"stated","status":"candidate","time":"2026-10-06T09:00:00Z","author":"ann","schema_version":1}`,
		}, schema.Cursor{Time: settled, RecordID: recordID(t, "knowledge", source().Knowledge.(items)[0])}.String()}},
		{"a guard entry leaves its cwd out", schema.Query{Kind: "guard"}, want{[]string{
			`{"record_kind":"guard","record_id":"` + guardID + `","time":"2026-10-06T09:00:00Z","session_id":"s1","tool":"Bash","veto":"no-rm","action":"block","schema_version":1}`,
		}, schema.Cursor{Time: settled, RecordID: guardID}.String()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			page, err := source().Page(context.Background(), tc.args, now)

			require.NoError(t, err)
			assert.Equal(t, tc.want.records, texts(page.Records))
			assert.Equal(t, tc.want.next, page.Next)
		})
	}
}

// An outcome leaves its note out and every page validates against the published schema of its kind
func TestSourcePageValidates(t *testing.T) {
	kinds := []string{"run", "turn", "verdict", "outcome", "knowledge", "guard"}
	for _, name := range kinds {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			k := kind(t, name)

			page, err := source().Page(context.Background(), schema.Query{Kind: name, Limit: 1}, now)

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
	tcs := []struct {
		name string
		args pageArgs
		want error
	}{
		{"an unknown kind", pageArgs{source(), schema.Query{Kind: "session"}}, schema.ErrKindUnknown},
		{"a cursor that names no record", pageArgs{source(), schema.Query{Kind: "run", Since: "%%%"}}, schema.ErrCursorInvalid},
		{"the guard of a source without a guard log", pageArgs{schema.Source{}, schema.Query{Kind: "guard"}}, schema.ErrNotServed},
		{"the runs of a source without traces", pageArgs{schema.Source{}, schema.Query{Kind: "run"}}, schema.ErrNotServed},
		{"the verdicts of a source without verdicts", pageArgs{schema.Source{}, schema.Query{Kind: "verdict"}}, schema.ErrNotServed},
		{"the outcomes of a source without outcomes", pageArgs{schema.Source{}, schema.Query{Kind: "outcome"}}, schema.ErrNotServed},
		{"the knowledge of a source without knowledge", pageArgs{schema.Source{}, schema.Query{Kind: "knowledge"}}, schema.ErrNotServed},
		{"a store that cannot be read", pageArgs{schema.Source{Traces: unreadable{}}, schema.Query{Kind: "run"}}, errUnreadable},
		{"a trace without an id", pageArgs{bare, schema.Query{Kind: "run"}}, schema.ErrRecordInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.args.source.Page(context.Background(), tc.args.query, now)

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// A store of no known kind serves nothing
func TestSourceRecordsRefuses(t *testing.T) {
	_, err := source().Records(context.Background(), schema.Kind{Name: "run", Store: "runs.csv"}, schema.Cursor{})

	assert.ErrorIs(t, err, schema.ErrStoreUnknown)
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

			page, err := schema.Source{Traces: many}.Page(context.Background(), schema.Query{Kind: "turn", Limit: tc.args}, now)

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
