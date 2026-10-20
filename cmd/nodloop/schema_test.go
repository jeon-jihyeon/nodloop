package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
	published "github.com/jeon-jihyeon/nodloop/schema"
)

// The files go generate writes from the Go types equal the published ones and nothing else is published
func TestPublishedSchemaFiles(t *testing.T) {
	files, err := publishedKinds.files()
	require.NoError(t, err)
	names, err := fs.Glob(published.Files, "*.json")
	require.NoError(t, err)

	embedded := map[string][]byte{}
	for _, name := range names {
		b, err := fs.ReadFile(published.Files, name)
		require.NoError(t, err)
		embedded[name] = b
	}
	got := map[string]string{}
	for name, b := range files {
		got[name] = string(b)
	}
	want := map[string]string{}
	for name, b := range embedded {
		want[name] = string(b)
	}
	assert.Equal(t, want, got, "run go generate ./cmd/nodloop and commit schema/")
}

// Every kind the stores write has an entry in the index and the file of its version and the index holds no other kind
func TestPublishedKindsComplete(t *testing.T) {
	stored := []string{"verdict", "outcome", "knowledge", "guard"}
	for _, n := range trace.Names() {
		stored = append(stored, string(n))
	}
	ix, err := schema.Published.Index()
	require.NoError(t, err)

	var indexed, filed []string
	for _, k := range ix.Kinds {
		indexed = append(indexed, k.Name)
		if _, err := k.File(k.Version); err == nil {
			filed = append(filed, k.Name)
		}
	}
	assert.ElementsMatch(t, stored, indexed)
	assert.ElementsMatch(t, stored, filed)
}

func TestRunSchema(t *testing.T) {
	verdict, err := fs.ReadFile(published.Files, "verdict.v1.json")
	require.NoError(t, err)
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"a kind prints its newest file", []string{"verdict"}, want{0, string(verdict), ""}},
		{"no kind lists every kind", nil, want{0, "run\tv1\ttraces.jsonl\n", ""}},
		{"an unknown kind names the kinds", []string{"session"}, want{1, "", `nodloop schema: unknown record kind: "session". Use one of [run check`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer

			code := runSchema(tc.args, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stdout.String(), tc.want.stdout)
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}

// The write step of go generate writes the published files into a folder
func TestRunSchemaWrite(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	code := runSchema([]string{"--write", dir}, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	written, err := os.ReadFile(filepath.Join(dir, "guard.v1.json"))
	require.NoError(t, err)
	want, err := fs.ReadFile(published.Files, "guard.v1.json")
	require.NoError(t, err)
	assert.Equal(t, string(want), string(written))
	assert.Equal(t, "wrote 23 files to "+dir+"\n", stdout.String())
	assert.Equal(t, 1, runSchema([]string{"--write", filepath.Join(dir, "missing")}, &bytes.Buffer{}, &bytes.Buffer{}))
}

// Every line the CLI, the hooks and the guard write validates against the published schema of its kind
func TestWrittenRecordsValidate(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	home, records, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	now := func() time.Time { return at }
	out := filepath.Join(home, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", sessionProducer, "--output", out}, getenv, now, &id, &bytes.Buffer{}))
	run := strings.TrimSpace(id.String())
	steps := []func() int{
		func() int {
			return runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "ran cd", "--reviewer", "ann"},
				getenv, now, atTerminal, &bytes.Buffer{}, &bytes.Buffer{})
		},
		func() int {
			return runFeedback([]string{"outcome", "--trace", run, "--result", "confirmed", "--reviewer", "ann"}, getenv, now, atTerminal, &bytes.Buffer{}, &bytes.Buffer{})
		},
		func() int {
			return runKnowledge([]string{"propose", "--id", "git-c", "--kind", "judgment", "--content", "use git -C", "--from", run},
				getenv, nil, now, atTerminal, &bytes.Buffer{}, &bytes.Buffer{})
		},
		func() int {
			return runKnowledge([]string{"approve", "git-c", "--version", "1", "--approver", "ann"}, getenv, nil, now, trace.ProofNone, &bytes.Buffer{}, &bytes.Buffer{})
		},
		func() int {
			return runKnowledge([]string{"approve", "git-c", "--version", "1", "--approver", "ann"}, getenv, nil, now, atTerminal, &bytes.Buffer{}, &bytes.Buffer{})
		},
		func() int {
			return runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(`{"session_id":"s1","prompt":"commit","cwd":"`+cwd+`"}`), &bytes.Buffer{}, &bytes.Buffer{})
		},
		func() int {
			return runHook([]string{"start"}, getenv, nil, now, strings.NewReader(`{"session_id":"s1","source":"compact","cwd":"`+cwd+`"}`), &bytes.Buffer{}, &bytes.Buffer{})
		},
		func() int {
			vetoes := "vetoes:\n  - {id: no-sed, tool: Bash, when: [{field: command, match: 'sed -i'}], reason: use Edit}\n"
			if err := os.MkdirAll(filepath.Join(home, ".claude", "nodloop"), 0o755); err != nil {
				return -1
			}
			if err := os.WriteFile(filepath.Join(home, vetofile.RelPath), []byte(vetoes), 0o600); err != nil {
				return -1
			}
			stdin := strings.NewReader(`{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"sed -i s/a/b/ f"},"cwd":"` + cwd + `"}`)
			return runGuard(nil, getenv, os.Executable, now, stdin, &bytes.Buffer{}, &bytes.Buffer{})
		},
	}
	var codes []int
	for _, step := range steps {
		codes = append(codes, step())
	}
	require.Equal(t, []int{0, 0, 0, 0, 0, 0, 0, 2}, codes)

	kinds := map[string]int{}
	var failures []string
	for path, kindOf := range map[string]func(line []byte) string{
		filepath.Join(records, "traces.jsonl"):          traceName,
		filepath.Join(records, "feedback.jsonl"):        func([]byte) string { return "verdict" },
		filepath.Join(records, "outcomes.jsonl"):        func([]byte) string { return "outcome" },
		filepath.Join(records, "knowledge.jsonl"):       func([]byte) string { return "knowledge" },
		filepath.Join(homeDir(home).dir(), decisionLog): func([]byte) string { return "guard" },
	} {
		f, err := os.Open(path)
		require.NoError(t, err)
		lines := bufio.NewScanner(f)
		for lines.Scan() {
			kind := kindOf(lines.Bytes())
			kinds[kind]++
			if err := schema.Validate(kind, json.RawMessage(slices.Clone(lines.Bytes()))); err != nil {
				failures = append(failures, err.Error())
			}
		}
		require.NoError(t, f.Close())
	}

	assert.Empty(t, failures)
	assert.Equal(t, map[string]int{
		"run": 1, "verdict": 1, "outcome": 1, "knowledge": 2, "intervention": 2, "turn": 1, "refill": 1, "guard": 1,
	}, kinds)
}

func traceName(line []byte) string {
	var tr struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(line, &tr) != nil {
		return ""
	}
	return tr.Name
}

// A kind whose types infer no schema or whose paths name no field publishes nothing
func TestKindTableFilesRefuses(t *testing.T) {
	good := publishedKinds[0]
	withRecord, withInput, withOutput, withLink, withFree := good, good, good, good, good
	withRecord.record = reflect.TypeFor[chan int]()
	withInput.input = reflect.TypeFor[func()]()
	withOutput.output = reflect.TypeFor[map[int]string]()
	withLink.links = []string{"input.applied.missing"}
	withFree.free = []string{"output.missing"}
	tcs := []struct {
		name string
		args publishedKind
		want string
	}{
		{"a record type with no schema", withRecord, "unsupported"},
		{"an input type with no schema", withInput, "unsupported"},
		{"an output type with no schema", withOutput, "unsupported map key"},
		{"a link that names no field", withLink, errSchemaPath.Error()},
		{"a free path that names no field", withFree, errSchemaPath.Error()},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := kindTable{tc.args}.files()

			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// A schema that does not encode publishes no file
func TestHeadedRefuses(t *testing.T) {
	_, err := headed(&jsonschema.Schema{Type: "string", Types: []string{"null", "string"}})

	assert.Error(t, err)
}

// Files that lack the index or the file of a kind and kinds that infer no schema fail the command
func TestSchemaCommandRefuses(t *testing.T) {
	index := fstest.MapFS{schema.IndexFile: {Data: []byte(`{"kinds":[{"kind":"verdict","version":1,"versions":[1],"store":"feedback.jsonl"}]}`)}}
	bad := publishedKinds[0]
	bad.record = reflect.TypeFor[chan int]()
	tcs := []struct {
		name string
		args func(c schemaCommand) error
		want string
	}{
		{"a list without an index", func(c schemaCommand) error { return c.list() }, schema.ErrIndexInvalid.Error()},
		{"a kind without an index", func(c schemaCommand) error { return c.show("verdict") }, schema.ErrIndexInvalid.Error()},
		{"a kind without its file", func(c schemaCommand) error {
			c.files = schema.NewFiles(index)
			return c.show("verdict")
		}, schema.ErrIndexInvalid.Error()},
		{"a write of a kind that infers no schema", func(c schemaCommand) error {
			c.kinds = kindTable{bad}
			return c.write(t.TempDir())
		}, "unsupported by jsonschema"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer

			err := tc.args(schemaCommand{files: schema.NewFiles(fstest.MapFS{}), out: &out})

			assert.ErrorContains(t, err, tc.want)
			assert.Empty(t, out.String())
		})
	}
	assert.Equal(t, 1, runSchema([]string{"--nope"}, &bytes.Buffer{}, &bytes.Buffer{}))
}
