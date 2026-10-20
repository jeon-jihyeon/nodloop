package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The record types both SDKs ship equal what go generate writes from the schemas of schema/
func TestSDKRecordTypes(t *testing.T) {
	files, err := publishedKinds.sdk()
	require.NoError(t, err)
	got, want := map[string]string{}, map[string]string{}
	for name, b := range files {
		shipped, err := os.ReadFile(filepath.Join("..", "..", "sdk", name))
		require.NoError(t, err)
		got[name], want[name] = string(b), string(shipped)
	}

	assert.Equal(t, want, got, "run go generate ./cmd/nodloop and commit sdk/")
	assert.Len(t, files, 2)
}

// The write step of go generate writes the record types of both SDKs under the sdk folder
func TestRunSchemaSDK(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "python", "src", "nodloop"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "typescript", "src"), 0o755))
	var stdout bytes.Buffer

	code := runSchema([]string{"--sdk", dir}, &stdout, &bytes.Buffer{})

	require.Equal(t, 0, code)
	written, err := os.ReadFile(filepath.Join(dir, sdkTypeScript))
	require.NoError(t, err)
	shipped, err := os.ReadFile(filepath.Join("..", "..", "sdk", sdkTypeScript))
	require.NoError(t, err)
	assert.Equal(t, string(shipped), string(written))
	assert.Equal(t, "wrote 2 files to "+dir+"\n", stdout.String())
	assert.Equal(t, 1, runSchema([]string{"--sdk", filepath.Join(dir, "missing")}, &bytes.Buffer{}, &bytes.Buffer{}))
	assert.Equal(t, 1, runSchema([]string{"--write", filepath.Join(dir, "missing"), "--sdk", dir}, &bytes.Buffer{}, &bytes.Buffer{}))
}

// A kind whose types infer no schema writes no SDK types
func TestKindTableSDKRefuses(t *testing.T) {
	bad := publishedKinds[0]
	bad.record = reflect.TypeFor[chan int]()

	_, err := kindTable{bad}.sdk()

	assert.ErrorContains(t, err, "unsupported")
	assert.Error(t, schemaCommand{kinds: kindTable{bad}, out: &bytes.Buffer{}}.writeSDK(t.TempDir()))
}

// A TypedDict of each mix of required and optional fields and of names Python reserves
func TestPyClassRender(t *testing.T) {
	id, from := pyField{name: "id", typ: "str"}, pyField{name: "from", typ: "int"}
	tcs := []struct {
		name string
		args pyClass
		want string
	}{
		{"required fields alone", pyClass{name: "A", doc: "One a", required: []pyField{id}},
			"class A(TypedDict):\n    \"\"\"One a\"\"\"\n\n    id: str\n"},
		{"optional fields alone", pyClass{name: "A", optional: []pyField{id}}, "class A(TypedDict, total=False):\n    id: str\n"},
		{"both over a base of the required", pyClass{name: "A", required: []pyField{id}, optional: []pyField{{name: "n", typ: "int"}}},
			"class _ARequired(TypedDict):\n    id: str\n\n\nclass A(_ARequired, total=False):\n    n: int\n"},
		{"a reserved name alone", pyClass{name: "A", optional: []pyField{from}},
			"A = TypedDict(\n    \"A\",\n    {\n        \"from\": int,\n    },\n    total=False,\n)\n"},
		{"a reserved name beside required fields with a doc", pyClass{name: "A", doc: "One a", required: []pyField{id}, optional: []pyField{from}},
			"class _ARequired(TypedDict):\n    id: str\n\n\n_AOptional = TypedDict(\n    \"_AOptional\",\n    {\n        \"from\": int,\n    },\n    total=False,\n)\n\n\n" +
				"class A(_ARequired, _AOptional):\n    \"\"\"One a\"\"\"\n"},
		{"a reserved name beside required fields", pyClass{name: "A", required: []pyField{from}, optional: []pyField{id}},
			"_ARequired = TypedDict(\n    \"_ARequired\",\n    {\n        \"from\": int,\n    },\n)\n\n\nclass _AOptional(TypedDict, total=False):\n    id: str\n\n\n" +
				"class A(_ARequired, _AOptional):\n    pass\n"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.args.render())
		})
	}
}

// A field line past the width ruff formats to puts each value of a Literal on its own line and keeps any other type whole
func TestPyLine(t *testing.T) {
	long := strings.Repeat("v", 70)
	tcs := []struct {
		name string
		args pyField
		want string
	}{
		{"a short Literal", pyField{name: "k", typ: `Literal["a", "b"]`}, "    k: Literal[\"a\", \"b\"]\n"},
		{"a long Literal", pyField{name: "k", typ: `Literal["` + long + `", "` + long + `"]`}, "    k: Literal[\n        \"" + long + "\",\n        \"" + long + "\",\n    ]\n"},
		{"a long type of another shape", pyField{name: "k", typ: "dict[str, " + long + long + "]"}, "    k: dict[str, " + long + long + "]\n"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, pyLine(tc.args))
		})
	}
}

// The types of the shapes a published schema holds in both languages
func TestSDKTypes(t *testing.T) {
	str := &jsonschema.Schema{Type: "string"}
	tcs := []struct {
		name string
		args *jsonschema.Schema
		want []string
	}{
		{"no schema", nil, []string{"unknown", "Any"}},
		{"any value", &jsonschema.Schema{}, []string{"unknown", "Any"}},
		{"a const", &jsonschema.Schema{Type: "integer", Const: jsonschema.Ptr[any](1)}, []string{"1", "Literal[1]"}},
		{"an array of nullable strings", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Types: []string{"null", "string"}}},
			[]string{"(string | null)[]", "list[str | None]"}},
		{"a map of numbers", &jsonschema.Schema{Type: "object", AdditionalProperties: &jsonschema.Schema{Type: "number"}},
			[]string{"Record<string, number>", "dict[str, float]"}},
		{"any object", &jsonschema.Schema{Type: "object"}, []string{"Record<string, unknown>", "dict[str, Any]"}},
		{"a nullable boolean", &jsonschema.Schema{Types: []string{"null", "boolean"}}, []string{"boolean | null", "bool | None"}},
		{"an object", &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"b": str, "a": str}, PropertyOrder: []string{"b", "gone"}, Required: []string{"a"}},
			[]string{"{\n  b?: string;\n  a: string;\n}", "T"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var classes pyClasses

			got := []string{tsType(tc.args, ""), classes.typ(tc.args, "T")}

			assert.Equal(t, tc.want, got)
		})
	}
}
