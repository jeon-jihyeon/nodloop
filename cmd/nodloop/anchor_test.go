package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The number and cause of every anchor version oldest first
// A cause that names a trace reads as the name of that trace, such as the intervention of an approval
func anchorVersions(t *testing.T, records string) []string {
	t.Helper()
	store, err := tracefile.New(records)
	require.NoError(t, err)
	found, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameAnchor)})
	require.NoError(t, err)
	out := []string{}
	for _, tr := range slices.Backward(found) {
		require.NoError(t, schema.Validate(string(trace.NameAnchor), tr))
		v, err := curate.RestoreAnchor(trace.Restore(tr))
		require.NoError(t, err)
		cause := v.Cause
		if named, err := store.Get(context.Background(), cause); err == nil {
			cause = named.Name
		}
		out = append(out, fmt.Sprintf("v%d %s", v.Version, cause))
	}
	return out
}

// anchor add and remove change the person's direction only from the person's terminal and every change records one version
func TestRunAnchor(t *testing.T) {
	type args struct {
		// The designated files config.json holds before
		saved []string
		// Run at the terminal before the command
		before  [][]string
		proof   trace.Proof
		command []string
	}
	type want struct {
		code   int
		stdout []string
		stderr string
		// The designated files of config.json
		saved []string
		// The number and cause of each anchor version
		versions []string
	}
	none := []string{}
	add := []string{"add", "{plan}"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the anchor holds the lesson a person approved",
			args{proof: trace.ProofNone},
			want{stdout: []string{"anchor\tv1\tcause ", "\t1 lessons you approved\t0 files\n"}, versions: []string{"v1 intervention"}},
		},
		{
			"a second listing records no second version",
			args{before: [][]string{nil}, proof: trace.ProofNone},
			want{stdout: []string{"anchor\tv1\tcause "}, versions: []string{"v1 intervention"}},
		},
		{
			"a file added at the terminal is designated and names the files as the cause",
			args{before: [][]string{nil}, proof: atTerminal, command: add},
			want{
				stdout: []string{"anchor\tv2\tcause files\t1 lessons you approved\t1 files\n", "designated\t{plan}\tevery place\n"},
				saved:  []string{"{plan}"}, versions: []string{"v1 intervention", "v2 files"},
			},
		},
		{"a file added twice is designated once", args{before: [][]string{add}, proof: atTerminal, command: add}, want{saved: []string{"{plan}"}, versions: []string{"v1 intervention"}}},
		{
			"an add from a script is refused",
			args{proof: trace.ProofNone, command: add},
			want{code: 1, stderr: errPersonOnly.Error(), versions: none},
		},
		{
			"a remove from a script is refused",
			args{before: [][]string{add}, proof: trace.ProofNone, command: []string{"remove", "{plan}"}},
			want{code: 1, stderr: errPersonOnly.Error(), saved: []string{"{plan}"}, versions: []string{"v1 intervention"}},
		},
		{
			"a removed file leaves the key and names the files as the cause",
			args{before: [][]string{add}, proof: atTerminal, command: []string{"remove", "{plan}"}},
			want{stdout: []string{"anchor\tv2\tcause files\t1 lessons you approved\t0 files\n"}, versions: []string{"v1 intervention", "v2 files"}},
		},
		{
			"a remove of a file that is not designated is refused",
			args{proof: atTerminal, command: []string{"remove", "{plan}"}},
			want{code: 1, stderr: errAnchorUnknown.Error(), versions: none},
		},
		{"a directory is refused", args{proof: atTerminal, command: []string{"add", "{dir}"}}, want{code: 1, stderr: errAnchorNoFile.Error(), versions: none}},
		{"a missing file is refused", args{proof: atTerminal, command: []string{"add", "{dir}/gone.md"}}, want{code: 1, stderr: "no such file", versions: none}},
		{"an add without a path is refused", args{proof: atTerminal, command: []string{"add"}}, want{code: 1, stderr: "a path is required", versions: none}},
		{"an unknown action is refused", args{proof: atTerminal, command: []string{"set", "{plan}"}}, want{code: 1, stderr: "unknown action", versions: none}},
		{"an unknown flag is refused", args{proof: atTerminal, command: []string{"--plan"}}, want{code: 1, stderr: "flag provided but not defined", versions: none}},
		{
			"a designated file that cannot be read is absent",
			args{saved: []string{"{dir}/gone.md"}, proof: trace.ProofNone},
			want{
				stdout: []string{"absent\t{dir}/gone.md\tcannot be read so it decides nothing\n"}, saved: []string{"{dir}/gone.md"},
				versions: []string{"v1 intervention"},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, _ := hookHome(t, "use git -C")
			dir := t.TempDir()
			plan := filepath.Join(dir, "plan.md")
			require.NoError(t, os.WriteFile(plan, []byte("ship the anchor first\n"), 0o600))
			place := strings.NewReplacer("{plan}", plan, "{dir}", dir)
			fill := func(args []string) []string {
				out := []string{}
				for _, a := range args {
					out = append(out, place.Replace(a))
				}
				return out
			}
			getenv := envOf(map[string]string{"HOME": home, envRecordDir: records})
			saveConfig(t, home, map[string]any{"anchor": map[string]any{"files": fill(tc.args.saved)}})
			for _, before := range tc.args.before {
				var stderr bytes.Buffer
				require.Equal(t, 0, runAnchor(fill(before), getenv, hookClock(), atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
			}
			var stdout, stderr bytes.Buffer

			code := runAnchor(fill(tc.args.command), getenv, hookClock(), tc.args.proof, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			for _, line := range tc.want.stdout {
				assert.Contains(t, stdout.String(), place.Replace(line))
			}
			assert.Contains(t, stderr.String(), tc.want.stderr)
			uc, err := homeDir(home).readConfig()
			require.NoError(t, err)
			assert.Equal(t, place.Replace(strings.Join(tc.want.saved, " ")), strings.Join(uc.Anchor.Files, " "))
			assert.Equal(t, tc.want.versions, anchorVersions(t, records))
		})
	}
}

// The anchor reads the records and config.json of the environment
func TestRunAnchorEnv(t *testing.T) {
	type args struct {
		// HOME and NODLOOP_RECORD_DIR with {home} and {records} for fresh directories and {file} for a regular file
		home, records string
		command       []string
	}
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"records with nothing on record hold no anchor", args{"{home}", "{records}", nil}, want{stdout: "anchor\tnone\tno lesson you approved and no file you keep yet\n"}},
		{"a change of the files without a home is refused", args{"", "{records}", []string{"add", "{file}"}}, want{code: 1, stderr: errHomeUnknown.Error()}},
		{"a relative record directory is refused", args{"{home}", "records", nil}, want{code: 1, stderr: "absolute path"}},
		{"a record directory that cannot be made fails", args{"{home}", "{file}", nil}, want{code: 1, stderr: "record dir"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file := filepath.Join(t.TempDir(), "plan.md")
			require.NoError(t, os.WriteFile(file, []byte("plan\n"), 0o600))
			place := strings.NewReplacer("{home}", t.TempDir(), "{records}", t.TempDir(), "{file}", file)
			command := strings.Fields(place.Replace(strings.Join(tc.args.command, " ")))
			getenv := envOf(map[string]string{"HOME": place.Replace(tc.args.home), envRecordDir: place.Replace(tc.args.records)})
			var stdout, stderr bytes.Buffer

			code := runAnchor(command, getenv, hookClock(), atTerminal, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}

// A record the anchor reads that cannot be read fails the listing and records no version
func TestRunAnchorRecordFailures(t *testing.T) {
	type args struct {
		// The record file and the text appended to it
		file, text string
	}
	instructions := func(input string) string {
		return `{"id":"i-1","name":"instructions","session_id":"s1","time":"2026-10-02T00:30:00Z","input":` + input + "}\n"
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"a knowledge line that does not decode", args{"knowledge.jsonl", "not json\n"}, "corrupt"},
		{"a trace line that does not decode", args{"traces.jsonl", "not json\n"}, "corrupt"},
		{"a key of another length", args{"hmac.key", "short"}, "hmac.key"},
		{
			"an anchor version that does not decode",
			args{"traces.jsonl", `{"id":"a-1","name":"anchor","time":"2026-10-02T00:30:00Z","input":{"version":"two"}}` + "\n"},
			curate.ErrAnchorInvalid.Error(),
		},
		{"a rules file whose input does not decode", args{"traces.jsonl", instructions("[]")}, errSessionTrace.Error()},
		{"a rules file whose HMAC is no hex", args{"traces.jsonl", instructions(`{"path":"/acme/CLAUDE.md","hmac":"zz"}`)}, curate.ErrAnchorInvalid.Error()},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, _ := hookHome(t, "use git -C")
			path := filepath.Join(records, tc.args.file)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			require.NoError(t, err)
			_, err = f.WriteString(tc.args.text)
			require.NoError(t, err)
			require.NoError(t, f.Close())
			var stderr bytes.Buffer

			code := runAnchor(nil, envOf(map[string]string{"HOME": home, envRecordDir: records}), hookClock(), trace.ProofNone, &bytes.Buffer{}, &stderr)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), tc.want)
			traces, err := os.ReadFile(filepath.Join(records, "traces.jsonl"))
			require.NoError(t, err)
			assert.Equal(t, strings.Count(tc.args.text, `"name":"anchor"`), strings.Count(string(traces), `"name":"anchor"`))
		})
	}
}
