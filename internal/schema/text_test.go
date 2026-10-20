package schema_test

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

// The text of a kind leaves the record at every depth and through arrays while the other fields keep their order
func TestKindStrip(t *testing.T) {
	type args struct {
		kind string
		json string
	}
	type want struct {
		json string
		err  error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a top level field goes", args{"verdict", `{"record_kind":"verdict","record_id":"v1","reason":"wrong","edited":{"a":1},"reviewer":"ann"}`},
			want{json: `{"record_kind":"verdict","record_id":"v1","reviewer":"ann"}`}},
		{"a field below an array goes from each element", args{"extract",
			`{"record_kind":"extract","output":{"attempts":[{"draft":{"content":"x"},"critique":"c","error":"e","pass":true},{"draft":null}],"candidate":{"id":"k1"}}}`},
			want{json: `{"record_kind":"extract","output":{"attempts":[{"pass":true},{}],"candidate":{"id":"k1"}}}`}},
		{"a path through a field of another shape leaves it", args{"check", `{"record_kind":"check","output":{"items":"none"}}`},
			want{json: `{"record_kind":"check","output":{"items":"none"}}`}},
		{"a kind without text keeps the record", args{"turn", `{"record_kind":"turn","input":{}}`}, want{json: `{"record_kind":"turn","input":{}}`}},
		{"an object cut short is refused", args{"verdict", `{"record_kind":"verdict","reason":`}, want{err: schema.ErrRecordInvalid}},
		{"an array cut short is refused", args{"extract", `{"output":{"attempts":[{"draft":1},`}, want{err: schema.ErrRecordInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := kind(t, tc.args.kind).Strip(schema.Record{Kind: tc.args.kind, JSON: json.RawMessage(tc.args.json)})

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.json, string(got.JSON))
		})
	}
}

// The top level fields of a published file come in the order the file lists them
func TestKindProperties(t *testing.T) {
	props, err := kind(t, "outcome").Properties(1)

	require.NoError(t, err)
	var names []string
	for _, p := range props {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"record_kind", "record_id", "trace_id", "time", "result", "confirmed_cause", "note", "reviewer", "schema_version"}, names)
	assert.JSONEq(t, `{"type":"string","format":"date-time"}`, string(props[3].Schema))
}

// A file that is missing or is no object or whose properties are no object lists no field
func TestKindPropertiesRefuses(t *testing.T) {
	index := `{"kinds":[{"kind":"verdict","version":1,"versions":[1],"store":"feedback.jsonl"}]}`
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"no object", `[]`, schema.ErrIndexInvalid},
		{"properties that are no object", `{"properties":[]}`, schema.ErrIndexInvalid},
		{"a file cut short", `{"properties":{"a":`, schema.ErrIndexInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := schema.NewFiles(fstest.MapFS{schema.IndexFile: {Data: []byte(index)}, "verdict.v1.json": {Data: []byte(tc.args)}})
			ix, err := files.Index()
			require.NoError(t, err)
			k, err := ix.Kind("verdict")
			require.NoError(t, err)

			_, err = k.Properties(1)

			assert.ErrorIs(t, err, tc.want)
		})
	}
	_, err := kind(t, "verdict").Properties(9)
	assert.ErrorIs(t, err, schema.ErrVersionUnknown)
}
