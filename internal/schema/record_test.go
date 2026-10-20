package schema_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

var at = time.Date(2026, 10, 6, 9, 0, 0, 123456789, time.UTC)

func kind(t *testing.T, name string) schema.Kind {
	t.Helper()
	ix, err := schema.Published.Index()
	require.NoError(t, err)
	k, err := ix.Kind(name)
	require.NoError(t, err)
	return k
}

// A stored record becomes the record every transport sends with the record_id of its store
func TestKindRecord(t *testing.T) {
	type args struct {
		kind   string
		stored string
	}
	type want struct {
		id   string
		json string
		err  error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a trace keeps its id and gains version 1 when it carries none",
			args{"turn", `{"id":"t1","name":"turn","time":"2026-10-06T09:00:00.123456789Z","input":{}}`},
			want{id: "t1", json: `{"record_kind":"turn","record_id":"t1","id":"t1","name":"turn","time":"2026-10-06T09:00:00.123456789Z","input":{},"schema_version":1}`}},
		{"a verdict is named like its OTLP evaluation and keeps the version it carries",
			args{"verdict", `{"trace_id":"r1","time":"2026-10-06T09:00:00.123456789Z","verdict":"approve","reviewer":"ann","schema_version":1}`},
			want{id: otel.VerdictSpan("r1", at).TraceID,
				json: `{"record_kind":"verdict","record_id":"` + otel.VerdictSpan("r1", at).TraceID + `","trace_id":"r1","time":"2026-10-06T09:00:00.123456789Z","verdict":"approve","reviewer":"ann","schema_version":1}`}},
		{"an outcome is named like its OTLP evaluation",
			args{"outcome", `{"trace_id":"r1","time":"2026-10-06T09:00:00.123456789Z","result":"confirmed","reviewer":"ann"}`},
			want{id: otel.OutcomeSpan("r1", at).TraceID,
				json: `{"record_kind":"outcome","record_id":"` + otel.OutcomeSpan("r1", at).TraceID + `","trace_id":"r1","time":"2026-10-06T09:00:00.123456789Z","result":"confirmed","reviewer":"ann","schema_version":1}`}},
		{"a knowledge record is named like the decision span of its status and keeps the kind of its item",
			args{"knowledge", `{"id":"k1","version":2,"kind":"judgment","status":"approved","time":"2026-10-06T09:00:00.123456789Z","veto":{"tool":"Bash"}}`},
			want{id: otel.DecisionSpan(otel.Ref{ID: "k1", Version: 2}, "approved", at).TraceID,
				json: `{"record_kind":"knowledge","record_id":"` + otel.DecisionSpan(otel.Ref{ID: "k1", Version: 2}, "approved", at).TraceID +
					`","id":"k1","version":2,"kind":"judgment","status":"approved","time":"2026-10-06T09:00:00.123456789Z","veto":{"tool":"Bash"},"schema_version":1}`}},
		{"a guard entry is named by its session, time, tool and veto",
			args{"guard", `{"time":"2026-10-06T09:00:00.123456789Z","session_id":"s1","cwd":"/repo","tool":"Bash","veto":"no-rm","action":"block"}`},
			want{id: "2043ca4693867201c8e01e5cbb12a60e",
				json: `{"record_kind":"guard","record_id":"2043ca4693867201c8e01e5cbb12a60e","time":"2026-10-06T09:00:00.123456789Z","session_id":"s1","cwd":"/repo","tool":"Bash","veto":"no-rm","action":"block","schema_version":1}`}},
		{"an empty record of a store keyed by its fields gains its version alone",
			args{"verdict", `{}`},
			want{id: "d04b27f2c8930e2c634e19d088fb1a89", json: `{"record_kind":"verdict","record_id":"d04b27f2c8930e2c634e19d088fb1a89","schema_version":1}`}},
		{"a trace without an id is refused", args{"run", `{"name":"run"}`}, want{err: schema.ErrRecordInvalid}},
		{"a record that is no object is refused", args{"run", `["run"]`}, want{err: schema.ErrRecordInvalid}},
		{"a field of the key of another type is refused", args{"knowledge", `{"id":"k1","version":"two"}`}, want{err: schema.ErrRecordInvalid}},
		{"a guard entry whose veto is no string is refused", args{"guard", `{"veto":{"tool":"Bash"}}`}, want{err: schema.ErrRecordInvalid}},
		{"a guard entry without a veto is refused", args{"guard", `{"tool":"Bash"}`}, want{err: schema.ErrRecordInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := kind(t, tc.args.kind).Record(json.RawMessage(tc.args.stored))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.id, got.ID)
			assert.Equal(t, tc.want.json, string(got.JSON))
		})
	}
}

// A record that does not encode or names an unknown store is refused
func TestKindRecordRefuses(t *testing.T) {
	type args struct {
		kind   schema.Kind
		stored any
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a value that does not encode", args{schema.Kind{Name: "run", Store: schema.StoreTraces}, func() {}}, schema.ErrRecordInvalid},
		{"a store nodloop does not keep", args{schema.Kind{Name: "run", Store: "runs.csv"}, json.RawMessage(`{"id":"t1"}`)}, schema.ErrStoreUnknown},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := tc.args.kind.Record(tc.args.stored)

			assert.ErrorIs(t, err, tc.want)
		})
	}
}
