package schema_test

import (
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

// A stored record validates against the file of its version and one of an earlier release reads as version 1
func TestValidate(t *testing.T) {
	type args struct {
		kind   string
		stored string
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{"a verdict of this release", args{"verdict", `{"trace_id":"r1","time":"2026-10-06T09:00:00Z","verdict":"edit","edited":{"a":1},"reviewer":"ann","proof":"none","schema_version":1}`}, nil},
		{"a verdict of an earlier release without schema_version reads as version 1",
			args{"verdict", `{"trace_id":"r1","time":"2026-10-06T09:00:00Z","verdict":"approve","reviewer":"ann"}`}, nil},
		{"a field a later release adds is no break", args{"outcome", `{"trace_id":"r1","time":"2026-10-06T09:00:00Z","result":"refuted","reviewer":"ann","weight":2}`}, nil},
		{"a verdict outside the set fails", args{"verdict", `{"trace_id":"r1","time":"2026-10-06T09:00:00Z","verdict":"maybe","reviewer":"ann"}`}, schema.ErrInvalid},
		{"a run of another name fails", args{"run", `{"id":"t1","name":"turn","time":"2026-10-06T09:00:00Z","input":{"applied":[]},"usage":{"input_tokens":0,"output_tokens":0,"cache_read":0,"cache_create":0,"cost_usd":0},"duration_ms":0}`}, schema.ErrInvalid},
		{"a version without a file fails", args{"verdict", `{"trace_id":"r1","time":"2026-10-06T09:00:00Z","verdict":"approve","reviewer":"ann","schema_version":2}`}, schema.ErrVersionUnknown},
		{"an unknown kind fails", args{"session", `{}`}, schema.ErrKindUnknown},
		{"a record that is no object of its store fails", args{"run", `{"name":"run"}`}, schema.ErrRecordInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := schema.Validate(tc.args.kind, json.RawMessage(tc.args.stored))

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// A set of files that lacks a file or holds one that does not decode refuses to validate
func TestFilesValidateRefuses(t *testing.T) {
	index := `{"kinds":[{"kind":"verdict","version":1,"versions":[1],"store":"feedback.jsonl"}]}`
	verdict := `{"trace_id":"r1","time":"2026-10-06T09:00:00Z","verdict":"approve","reviewer":"ann"}`
	tcs := []struct {
		name string
		args fstest.MapFS
		want error
	}{
		{"no index", fstest.MapFS{}, schema.ErrIndexInvalid},
		{"an index that does not decode", fstest.MapFS{schema.IndexFile: {Data: []byte("[")}}, schema.ErrIndexInvalid},
		{"no file of the version", fstest.MapFS{schema.IndexFile: {Data: []byte(index)}}, schema.ErrIndexInvalid},
		{"a file that does not decode", fstest.MapFS{schema.IndexFile: {Data: []byte(index)}, "verdict.v1.json": {Data: []byte("{")}}, schema.ErrIndexInvalid},
		{"a file that does not resolve", fstest.MapFS{
			schema.IndexFile: {Data: []byte(index)}, "verdict.v1.json": {Data: []byte(`{"$schema":"` + schema.Dialect + `","$ref":"#/$defs/missing"}`)},
		}, schema.ErrIndexInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := schema.NewFiles(tc.args).Validate("verdict", json.RawMessage(verdict))

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// A second record of one file validates through the schema the first resolved
func TestFilesValidateResolvesOnce(t *testing.T) {
	files := schema.NewFiles(fstest.MapFS{
		schema.IndexFile:  {Data: []byte(`{"kinds":[{"kind":"verdict","version":1,"versions":[1],"store":"feedback.jsonl"}]}`)},
		"verdict.v1.json": {Data: []byte(`{"$schema":"` + schema.Dialect + `","required":["verdict"]}`)},
	})

	first := files.Validate("verdict", json.RawMessage(`{"verdict":"approve"}`))
	second := files.Validate("verdict", json.RawMessage(`{"reviewer":"ann"}`))

	assert.NoError(t, first)
	assert.ErrorIs(t, second, schema.ErrInvalid)
}

// A transported record that is no JSON object with a version number is refused
func TestKindValidateRefuses(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"no JSON", "{", schema.ErrRecordInvalid},
		{"no object", "[]", schema.ErrRecordInvalid},
		{"a version that is no number", `{"schema_version":"1"}`, schema.ErrRecordInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := kind(t, "verdict").Validate(schema.Record{Kind: "verdict", JSON: json.RawMessage(tc.args)})

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// Every kind of the index has the file of each version it lists and that file names itself and the kind
func TestIndexFiles(t *testing.T) {
	ix, err := schema.Published.Index()
	require.NoError(t, err)
	require.NotEmpty(t, ix.Kinds)
	for _, k := range ix.Kinds {
		t.Run(k.Name, func(t *testing.T) {
			var file struct {
				ID         string `json:"$id"`
				Properties struct {
					Kind struct {
						Const string `json:"const"`
					} `json:"record_kind"`
				} `json:"properties"`
			}
			b, err := k.File(k.Version)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(b, &file))
			_, err = k.File(k.Version + 1)

			assert.Equal(t, k.Schema, file.ID)
			assert.Equal(t, schema.Base+schema.FileName(k.Name, k.Version), file.ID)
			assert.Equal(t, k.Name, file.Properties.Kind.Const)
			assert.ErrorIs(t, err, schema.ErrVersionUnknown)
		})
	}
}
