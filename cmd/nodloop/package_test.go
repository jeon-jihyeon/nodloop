package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/datapackage"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// The records of every writer the CLI runs with a guard log of the home
type packageRecords struct {
	home, records string
	getenv        func(string) string
}

func newPackageRecords(t *testing.T) packageRecords {
	t.Helper()
	home, records := t.TempDir(), t.TempDir()
	return packageRecords{home: home, records: records, getenv: func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }}
}

// A run with a verdict, an outcome, a candidate, a proposal to approve it, a turn and a guard block, all at the time
func (r packageRecords) write(t *testing.T, at time.Time, output string) string {
	t.Helper()
	now := func() time.Time { return at }
	out := filepath.Join(r.home, "out.txt")
	require.NoError(t, os.WriteFile(out, []byte(output), 0o600))
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", sessionProducer, "--output", out}, r.getenv, now, &id, &bytes.Buffer{}))
	run := strings.TrimSpace(id.String())
	item := "item-" + strconv.FormatInt(at.Unix(), 10)
	cwd := t.TempDir()
	vetoes := "vetoes:\n  - {id: no-sed, tool: Bash, when: [{field: command, match: 'sed -i'}], reason: use Edit}\n"
	require.NoError(t, os.MkdirAll(filepath.Join(r.home, ".claude", "nodloop"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(r.home, vetofile.RelPath), []byte(vetoes), 0o600))
	codes := []int{
		runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "ran cd", "--reviewer", "ann"}, r.getenv, now, atTerminal, &bytes.Buffer{}, &bytes.Buffer{}),
		runFeedback([]string{"outcome", "--trace", run, "--result", "confirmed", "--note", "checked the repo", "--reviewer", "ann"}, r.getenv, now, atTerminal,
			&bytes.Buffer{}, &bytes.Buffer{}),
		runKnowledge([]string{"propose", "--id", item, "--kind", "judgment", "--content", "use git -C", "--from", run}, r.getenv, nil, now, atTerminal,
			&bytes.Buffer{}, &bytes.Buffer{}),
		runKnowledge([]string{"approve", item, "--version", "1", "--approver", "ann"}, r.getenv, nil, now, trace.ProofNone, &bytes.Buffer{}, &bytes.Buffer{}),
		runHook([]string{"prompt"}, r.getenv, nil, now, strings.NewReader(`{"session_id":"s1","prompt":"commit","cwd":"`+cwd+`"}`), &bytes.Buffer{}, &bytes.Buffer{}),
		runGuard(nil, r.getenv, os.Executable, now, strings.NewReader(`{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"sed -i s/a/b/ f"},"cwd":"`+cwd+`"}`),
			&bytes.Buffer{}, &bytes.Buffer{}),
	}
	require.Equal(t, []int{0, 0, 0, 0, 0, 2}, codes)
	return run
}

// Exports the records at the time into the folder and answers what the command printed on stdout and stderr with its code
func (r packageRecords) export(t *testing.T, at time.Time, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runExport(append([]string{"package"}, args...), r.getenv, func() time.Time { return at }, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// A descriptor as a reader of the folder decodes it
type exported struct {
	Schema    string      `json:"$schema"`
	Name      string      `json:"name"`
	Created   string      `json:"created"`
	Nodloop   packageMeta `json:"nodloop"`
	Resources []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Schema struct {
			Fields        []datapackage.Field      `json:"fields"`
			MissingValues []string                 `json:"missingValues"`
			PrimaryKey    []string                 `json:"primaryKey"`
			ForeignKeys   []datapackage.ForeignKey `json:"foreignKeys"`
		} `json:"schema"`
		Nodloop tableMeta `json:"nodloop"`
	} `json:"resources"`
}

func readExport(t *testing.T, folder string) exported {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(folder, datapackage.Descriptor))
	require.NoError(t, err)
	var d exported
	require.NoError(t, json.Unmarshal(b, &d))
	return d
}

// The rows of each resource by name and every cell that does not read as the type of its field
func (d exported) tables(t *testing.T, folder string) (map[string]int, []string) {
	t.Helper()
	rows, problems := map[string]int{}, []string{}
	for _, res := range d.Resources {
		f, err := os.Open(filepath.Join(folder, res.Path))
		require.NoError(t, err)
		lines, err := csv.NewReader(f).ReadAll()
		require.NoError(t, err)
		require.NoError(t, f.Close())
		var head []string
		for _, fd := range res.Schema.Fields {
			head = append(head, fd.Name)
		}
		require.Equal(t, head, lines[0], res.Name)
		for _, line := range lines[1:] {
			for i, fd := range res.Schema.Fields {
				if err := readsAs(fd.Type, line[i]); err != nil {
					problems = append(problems, fmt.Sprintf("%s %s %q: %v", res.Name, fd.Name, line[i], err))
				}
			}
		}
		rows[res.Name] = len(lines) - 1
	}
	return rows, problems
}

// Whether the cell reads as a value of the Table Schema type
func readsAs(typ, cell string) error {
	if cell == datapackage.Null {
		return nil
	}
	var err error
	switch typ {
	case "integer":
		_, err = strconv.ParseInt(cell, 10, 64)
	case "number":
		_, err = strconv.ParseFloat(cell, 64)
	case "boolean":
		_, err = strconv.ParseBool(cell)
	case "datetime":
		_, err = time.Parse(time.RFC3339Nano, cell)
	case "object", "array", "any":
		if !json.Valid([]byte(cell)) {
			err = fmt.Errorf("no JSON")
		}
	}
	return err
}

// Every kind becomes one table of its published fields with the text left out, and a second export with --since holds only newer records
func TestExportPackage(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	r := newPackageRecords(t)
	first := r.write(t, at, "cd repo && git status")
	folder := filepath.Join(t.TempDir(), "first")
	ix, err := schema.Published.Index()
	require.NoError(t, err)
	var names []string
	for _, k := range ix.Kinds {
		names = append(names, k.Name+".v1")
	}

	code, stdout, stderr := r.export(t, at.Add(time.Minute), "--out", folder)
	second := r.write(t, at.Add(time.Hour), "cd other && git log")
	next := filepath.Join(t.TempDir(), "next")
	nextCode, _, nextErr := r.export(t, at.Add(2*time.Hour), "--out", next, "--since", filepath.Join(folder, datapackage.Descriptor))

	require.Equal(t, 0, code, stderr)
	require.Equal(t, 0, nextCode, nextErr)
	d, n := readExport(t, folder), readExport(t, next)
	rows, problems := d.tables(t, folder)
	nextRows, nextProblems := n.tables(t, next)
	runs, err := os.ReadFile(filepath.Join(folder, "run.v1.csv"))
	require.NoError(t, err)
	nextRuns, err := os.ReadFile(filepath.Join(next, "run.v1.csv"))
	require.NoError(t, err)
	var got []string
	for _, res := range d.Resources {
		got = append(got, res.Name)
	}
	assert.Equal(t, "exported 7 records of 22 kinds to "+folder+"\n", stdout)
	assert.Equal(t, datapackage.Profile, d.Schema)
	assert.Equal(t, "nodloop-20261002t120100z", d.Name)
	assert.Equal(t, packageMeta{Version: buildVersion(), Complete: true, Cursors: n.Nodloop.Cursors}, n.Nodloop)
	assert.True(t, d.Nodloop.Complete)
	assert.Equal(t, names, got)
	assert.Empty(t, problems)
	assert.Empty(t, nextProblems)
	assert.Equal(t, map[string]int{"run.v1": 1, "verdict.v1": 1, "outcome.v1": 1, "knowledge.v1": 1, "intervention.v1": 1, "turn.v1": 1, "guard.v1": 1, "refill.v1": 0},
		pick(rows, "run.v1", "verdict.v1", "outcome.v1", "knowledge.v1", "intervention.v1", "turn.v1", "guard.v1", "refill.v1"))
	assert.Equal(t, map[string]int{"run.v1": 1, "verdict.v1": 1, "guard.v1": 1}, pick(nextRows, "run.v1", "verdict.v1", "guard.v1"))
	assert.Contains(t, string(runs), first)
	assert.NotContains(t, string(runs), "git status", "an output is text")
	assert.Contains(t, string(nextRuns), second)
	assert.NotContains(t, string(nextRuns), first)
	assert.NotEqual(t, d.Nodloop.Cursors["run"], n.Nodloop.Cursors["run"])
	assert.Equal(t, d.Nodloop.Cursors["refill"], n.Nodloop.Cursors["refill"], "a kind with nothing new keeps its cursor")
}

// The resources name their published schema and the foreign keys of the kinds they name
func TestExportPackageResources(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	r := newPackageRecords(t)
	r.write(t, at, "cd repo && git status")
	folder := filepath.Join(t.TempDir(), "out")

	code, _, stderr := r.export(t, at.Add(time.Minute), "--out", folder, "--kind", "verdict", "--kind", "answer", "--kind", "run", "--with-content")

	require.Equal(t, 0, code, stderr)
	d := readExport(t, folder)
	require.Len(t, d.Resources, 3)
	verdicts, err := os.ReadFile(filepath.Join(folder, "verdict.v1.csv"))
	require.NoError(t, err)
	assert.Equal(t, []string{"run.v1", "answer.v1", "verdict.v1"}, []string{d.Resources[0].Name, d.Resources[1].Name, d.Resources[2].Name})
	assert.Equal(t, tableMeta{Kind: "verdict", SchemaVersion: 1, Rows: 1, ID: schema.Base + "verdict.v1.json"}, d.Resources[2].Nodloop)
	assert.Equal(t, []string{"record_id"}, d.Resources[2].Schema.PrimaryKey)
	assert.Equal(t, []string{datapackage.Null}, d.Resources[2].Schema.MissingValues)
	assert.Equal(t, []datapackage.ForeignKey{{Fields: []string{"trace_id"}, Reference: datapackage.Reference{Resource: "run.v1", Fields: []string{"record_id"}}}},
		d.Resources[2].Schema.ForeignKeys)
	assert.Empty(t, d.Resources[1].Schema.ForeignKeys, "an answer names an expose the package does not hold")
	assert.Contains(t, string(verdicts), "ran cd", "content keeps the reason")
	assert.Contains(t, string(verdicts), `,\N,`, "a field the verdict lacks is null apart from an empty string")
}

// An export refuses what it cannot read before it makes the folder and records a failure halfway as complete false
func TestExportPackageRefuses(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	r := newPackageRecords(t)
	r.write(t, at, "cd repo && git status")
	base := t.TempDir()
	notJSON, badCursor := filepath.Join(base, "not.json"), filepath.Join(base, "bad.json")
	require.NoError(t, os.WriteFile(notJSON, []byte("{"), 0o600))
	require.NoError(t, os.WriteFile(badCursor, []byte(`{"nodloop":{"cursors":{"run":"%%%"}}}`), 0o600))
	full := filepath.Join(base, "full")
	require.NoError(t, os.MkdirAll(full, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(full, "x"), nil, 0o600))
	tcs := []struct {
		name string
		args []string
		want string
	}{
		{"no folder", nil, "--out is required"},
		{"an unknown kind", []string{"--out", filepath.Join(base, "a"), "--kind", "session"}, schema.ErrKindUnknown.Error()},
		{"a descriptor that is missing", []string{"--out", filepath.Join(base, "b"), "--since", filepath.Join(base, "missing.json")}, "no such file"},
		{"a descriptor that is no JSON", []string{"--out", filepath.Join(base, "c"), "--since", notJSON}, errSinceInvalid.Error()},
		{"a descriptor with a cursor no page answered", []string{"--out", filepath.Join(base, "d"), "--since", badCursor}, schema.ErrCursorInvalid.Error()},
		{"a folder that is not empty", []string{"--out", full}, datapackage.ErrFolderNotEmpty.Error()},
		{"a flag it does not know", []string{"--nope"}, "flag provided but not defined"},
		{"a record dir that is a file", []string{"--out", filepath.Join(base, "e"), "--record-dir", notJSON}, "record dir: "},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := r.export(t, at.Add(time.Minute), tc.args...)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr, tc.want)
		})
	}
	assert.NoDirExists(t, filepath.Join(base, "a"))
	assert.NoDirExists(t, filepath.Join(base, "d"))
	assert.NoDirExists(t, filepath.Join(base, "e"))
}

// A config.json that does not decode fails the export before it makes the folder
func TestExportPackageBrokenConfig(t *testing.T) {
	home, folder := t.TempDir(), filepath.Join(t.TempDir(), "out")
	hookSaveConfig(t, home, "{broken")
	var stderr bytes.Buffer

	code := runExport([]string{"package", "--out", folder}, envOf(map[string]string{"HOME": home}), time.Now, &bytes.Buffer{}, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "config.json is not valid JSON")
	assert.NoDirExists(t, folder)
}

// A store that fails halfway leaves the tables written before it with complete false, the error and their cursors
func TestExportPackageFailsHalfway(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	r := newPackageRecords(t)
	r.write(t, at, "cd repo && git status")
	f, err := os.OpenFile(filepath.Join(r.records, "knowledge.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("{broken\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	folder := filepath.Join(t.TempDir(), "out")

	code, _, stderr := r.export(t, at.Add(time.Minute), "--out", folder)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "knowledge.jsonl")
	d := readExport(t, folder)
	assert.False(t, d.Nodloop.Complete)
	assert.Contains(t, d.Nodloop.Error, "knowledge.jsonl")
	assert.Equal(t, "outcome.v1", d.Resources[len(d.Resources)-1].Name)
	assert.NotEmpty(t, d.Nodloop.Cursors["run"])
	assert.Empty(t, d.Nodloop.Cursors["knowledge"])
}

// Published files without an index refuse before the folder is made and a kind without its file stops the export after the folder exists
func TestPackageCommandFiles(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	r := newPackageRecords(t)
	r.write(t, at, "cd repo && git status")
	a, err := recordFlags{}.app(r.getenv, func() time.Time { return at })
	require.NoError(t, err)
	source, err := a.source()
	require.NoError(t, err)
	index := fstest.MapFS{schema.IndexFile: {Data: []byte(`{"kinds":[{"kind":"verdict","version":1,"versions":[1],"store":"feedback.jsonl"}]}`)}}
	base := t.TempDir()
	command := func(files fstest.MapFS) packageCommand {
		return packageCommand{source: source, files: schema.NewFiles(files), kinds: publishedKinds, now: func() time.Time { return at.Add(time.Minute) }, out: &bytes.Buffer{}}
	}

	noIndex := command(fstest.MapFS{}).export(context.Background(), packageExport{folder: filepath.Join(base, "none")})
	noFile := command(index).export(context.Background(), packageExport{folder: filepath.Join(base, "verdicts")})

	assert.ErrorIs(t, noIndex, schema.ErrIndexInvalid)
	assert.NoDirExists(t, filepath.Join(base, "none"))
	assert.ErrorIs(t, noFile, schema.ErrIndexInvalid)
	assert.False(t, readExport(t, filepath.Join(base, "verdicts")).Nodloop.Complete)
}

// DuckDB reads every resource of an exported folder with the types of its Table Schema where DuckDB is installed
func TestExportPackageDuckDB(t *testing.T) {
	duckdb, err := exec.LookPath("duckdb")
	if err != nil {
		t.Skip("duckdb is not installed")
	}
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	r := newPackageRecords(t)
	r.write(t, at, "cd repo && git status")
	folder := filepath.Join(t.TempDir(), "out")
	code, _, stderr := r.export(t, at.Add(time.Minute), "--out", folder, "--with-content")
	require.Equal(t, 0, code, stderr)
	d := readExport(t, folder)
	var queries []string
	want := map[string]int{}
	for _, res := range d.Resources {
		queries = append(queries, fmt.Sprintf("SELECT '%s' AS resource, count(*) AS n FROM %s", res.Name, duckRead(folder, res.Path, res.Schema.Fields)))
		want[res.Name] = res.Nodloop.Rows
	}

	out, err := exec.Command(duckdb, "-json", "-c", strings.Join(queries, " UNION ALL ")).CombinedOutput()

	require.NoError(t, err, string(out))
	var counts []struct {
		Resource string `json:"resource"`
		N        int    `json:"n"`
	}
	require.NoError(t, json.Unmarshal(out, &counts))
	got := map[string]int{}
	for _, c := range counts {
		got[c.Resource] = c.N
	}
	assert.Equal(t, want, got)
}

// The read_csv call of DuckDB with the columns of the Table Schema
func duckRead(folder, path string, fields []datapackage.Field) string {
	types := map[string]string{"string": "VARCHAR", "datetime": "TIMESTAMPTZ", "integer": "BIGINT", "number": "DOUBLE", "boolean": "BOOLEAN"}
	var columns []string
	for _, f := range fields {
		typ, ok := types[f.Type]
		if !ok {
			typ = "JSON"
		}
		columns = append(columns, fmt.Sprintf("'%s': '%s'", f.Name, typ))
	}
	return fmt.Sprintf("read_csv('%s', header = true, nullstr = '%s', columns = {%s})", filepath.Join(folder, path), datapackage.Null, strings.Join(columns, ", "))
}

func pick(m map[string]int, keys ...string) map[string]int {
	out := map[string]int{}
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}
