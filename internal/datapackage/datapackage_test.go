package datapackage_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/datapackage"
)

var created = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)

type meta struct {
	Complete bool `json:"complete"`
}

var fields = []datapackage.Field{
	{Name: "record_id", Type: "string"}, {Name: "time", Type: "datetime"}, {Name: "reason", Type: "string"}, {Name: "rows", Type: "integer"},
	{Name: "audit", Type: "boolean"}, {Name: "input", Type: "object"}, {Name: "output", Type: "any"},
}

// A folder of two tables lists both under the final descriptor with their Table Schema while the first descriptor said it was not done
func TestPackage(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "out", "records")
	rows := []json.RawMessage{
		json.RawMessage(`{"record_id":"r1","time":"2026-10-20T09:00:00Z","reason":"","rows":3,"audit":true,"input":{"a":[1]},"output":"text, \"quoted\""}`),
		json.RawMessage(`{"record_id":"r2","time":"2026-10-20T09:00:01Z","reason":null,"input":{},"extra":1}`),
	}
	table := datapackage.Table{
		Name: "run.v1", Fields: fields, PrimaryKey: []string{"record_id"},
		ForeignKeys: []datapackage.ForeignKey{{Fields: []string{"ref"}, Reference: datapackage.Reference{Resource: "run.v1", Fields: []string{"record_id"}}}},
		Nodloop:     map[string]any{"rows": 2},
	}

	p, err := datapackage.Create(folder, "nodloop-20261020t093000z", created, meta{})
	require.NoError(t, err)
	first, err := os.ReadFile(filepath.Join(folder, datapackage.Descriptor))
	require.NoError(t, err)
	require.NoError(t, p.Write(table, rows))
	require.NoError(t, p.Write(datapackage.Table{Name: "turn.v1", Fields: fields[:2]}, nil))
	require.NoError(t, p.Close(meta{Complete: true}))

	final, err := os.ReadFile(filepath.Join(folder, datapackage.Descriptor))
	require.NoError(t, err)
	csv, err := os.ReadFile(filepath.Join(folder, "run.v1.csv"))
	require.NoError(t, err)
	empty, err := os.ReadFile(filepath.Join(folder, "turn.v1.csv"))
	require.NoError(t, err)
	info, err := os.Stat(folder)
	require.NoError(t, err)
	file, err := os.Stat(filepath.Join(folder, "run.v1.csv"))
	require.NoError(t, err)
	names, err := os.ReadDir(folder)
	require.NoError(t, err)
	assert.JSONEq(t, `{"$schema":"`+datapackage.Profile+`","name":"nodloop-20261020t093000z","created":"2026-10-20T09:30:00Z","nodloop":{"complete":false},"resources":[]}`, string(first))
	assert.JSONEq(t, `{"$schema":"`+datapackage.Profile+`","name":"nodloop-20261020t093000z","created":"2026-10-20T09:30:00Z","nodloop":{"complete":true},"resources":[
		{"name":"run.v1","path":"run.v1.csv","type":"table","format":"csv","mediatype":"text/csv","encoding":"utf-8","schema":{
			"fields":[{"name":"record_id","type":"string"},{"name":"time","type":"datetime"},{"name":"reason","type":"string"},{"name":"rows","type":"integer"},
				{"name":"audit","type":"boolean"},{"name":"input","type":"object"},{"name":"output","type":"any"}],
			"missingValues":["\\N"],"primaryKey":["record_id"],"foreignKeys":[{"fields":["ref"],"reference":{"resource":"run.v1","fields":["record_id"]}}]},
		"nodloop":{"rows":2}},
		{"name":"turn.v1","path":"turn.v1.csv","type":"table","format":"csv","mediatype":"text/csv","encoding":"utf-8","schema":{
			"fields":[{"name":"record_id","type":"string"},{"name":"time","type":"datetime"}],"missingValues":["\\N"]}}]}`, string(final))
	assert.Equal(t, "record_id,time,reason,rows,audit,input,output\n"+
		`r1,2026-10-20T09:00:00Z,,3,true,"{""a"":[1]}","""text, \""quoted\"""""`+"\n"+
		`r2,2026-10-20T09:00:01Z,\N,\N,\N,{},\N`+"\n", string(csv))
	assert.Equal(t, "record_id,time\n", string(empty))
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	assert.Equal(t, os.FileMode(0o600), file.Mode().Perm())
	assert.Len(t, names, 3, "the temp file of the descriptor is gone")
}

// The folder must be missing or an empty real folder
func TestCreateRefuses(t *testing.T) {
	base := t.TempDir()
	full := filepath.Join(base, "full")
	require.NoError(t, os.MkdirAll(full, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(full, "x"), nil, 0o600))
	file := filepath.Join(base, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	empty := filepath.Join(base, "empty")
	require.NoError(t, os.MkdirAll(empty, 0o755))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(empty, link))
	locked := filepath.Join(base, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o300))
	readOnly := filepath.Join(base, "read-only")
	require.NoError(t, os.MkdirAll(readOnly, 0o500))
	tcs := []struct {
		name string
		args string
		want error
	}{
		{"a link to an empty folder", link, datapackage.ErrFolderLink},
		{"a folder with a file", full, datapackage.ErrFolderNotEmpty},
		{"a file", file, datapackage.ErrNotFolder},
		{"a folder below a file", filepath.Join(file, "out"), syscall.ENOTDIR},
		{"a folder whose names cannot be read", locked, os.ErrPermission},
		{"a folder below a parent that cannot be made", filepath.Join(readOnly, "a", "out"), os.ErrPermission},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := datapackage.Create(tc.args, "nodloop", created, meta{})

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

// An empty real folder takes the package with mode 0700 and a missing one is made below a missing parent
func TestCreateEmptyFolder(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.MkdirAll(empty, 0o755))

	_, err := datapackage.Create(empty, "nodloop", created, meta{})

	require.NoError(t, err)
	info, err := os.Stat(empty)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// A row that is no object, a field whose value is of another shape, a table written twice and a nodloop key that does not encode are refused
func TestPackageRefuses(t *testing.T) {
	tcs := []struct {
		name string
		args func(p *datapackage.Package) error
		want string
	}{
		{"a row that is no object", func(p *datapackage.Package) error {
			return p.Write(datapackage.Table{Name: "t", Fields: fields}, []json.RawMessage{json.RawMessage(`[]`)})
		}, datapackage.ErrRowInvalid.Error()},
		{"a null row", func(p *datapackage.Package) error {
			return p.Write(datapackage.Table{Name: "t", Fields: fields}, []json.RawMessage{json.RawMessage(`null`)})
		}, datapackage.ErrRowInvalid.Error()},
		{"a string field holding a number", func(p *datapackage.Package) error {
			return p.Write(datapackage.Table{Name: "t", Fields: fields}, []json.RawMessage{json.RawMessage(`{"reason":3}`)})
		}, "row 1 field reason"},
		{"a table written twice", func(p *datapackage.Package) error {
			if err := p.Write(datapackage.Table{Name: "t", Fields: fields}, nil); err != nil {
				return err
			}
			return p.Write(datapackage.Table{Name: "t", Fields: fields}, nil)
		}, "file exists"},
		{"a nodloop key that does not encode", func(p *datapackage.Package) error { return p.Close(func() {}) }, "unsupported type"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := datapackage.Create(filepath.Join(t.TempDir(), "out"), "nodloop", created, meta{})
			require.NoError(t, err)

			err = tc.args(p)

			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// A nodloop key that does not encode writes no first descriptor and a folder removed under the package fails Close
func TestPackageDescriptorRefuses(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "out")
	_, err := datapackage.Create(folder, "nodloop", created, func() {})
	require.ErrorContains(t, err, "unsupported type")
	gone := filepath.Join(t.TempDir(), "gone")
	p, err := datapackage.Create(gone, "nodloop", created, meta{})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(gone))

	err = p.Close(meta{Complete: true})

	assert.ErrorIs(t, err, os.ErrNotExist)
}

// The Table Schema type of each shape of a property of a published schema
func TestFieldOf(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"a string", `{"type":"string"}`, "string"},
		{"a time", `{"type":"string","format":"date-time"}`, "datetime"},
		{"an integer", `{"type":"integer","const":1}`, "integer"},
		{"a number", `{"type":"number"}`, "number"},
		{"a boolean", `{"type":"boolean"}`, "boolean"},
		{"an object", `{"type":"object","properties":{}}`, "object"},
		{"an array that may be null", `{"type":["null","array"]}`, "array"},
		{"an object that may be null", `{"type":["null","object"]}`, "object"},
		{"any value", `true`, "any"},
		{"an empty schema", `{}`, "any"},
		{"two types", `{"type":["string","integer"]}`, "any"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := datapackage.FieldOf("f", json.RawMessage(tc.args))

			require.NoError(t, err)
			assert.Equal(t, datapackage.Field{Name: "f", Type: tc.want}, got)
		})
	}
	_, err := datapackage.FieldOf("f", json.RawMessage(`{`))
	assert.ErrorIs(t, err, datapackage.ErrFieldInvalid)
}
