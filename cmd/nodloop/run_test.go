package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// A run is recorded with the record directory alone, no data dir set
func TestRunRunRecord(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	type want struct {
		code int
		// Regexp of stderr
		stderr string
		labels trace.Labels
		input  string
		output string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{
			"records text output with labels and applied items",
			[]string{"record", "--producer", "session", "--label", "repo=nodloop", "--label", "task=commit", "--applied", "k-1:2", "--output", "{text}"},
			want{labels: trace.Labels{"repo": {"nodloop"}, "task": {"commit"}}, input: `{"applied":[{"id":"k-1","version":2}]}`, output: `"use git -C"`},
		},
		{
			"records JSON output as it is",
			[]string{"record", "--producer", "session", "--output", "{json}"},
			want{input: `{"applied":null}`, output: `{"answer":1}`},
		},
		{"refuses a run without output", []string{"record", "--producer", "session"}, want{code: 1, stderr: `^nodloop run: record: --output is required\n`}},
		{
			"refuses a run without producer",
			[]string{"record", "--output", "{text}"},
			want{code: 1, stderr: `^nodloop run: ` + regexp.QuoteMeta(trace.ErrProducerRequired.Error()) + `\n$`},
		},
		{"refuses a label without a value", []string{"record", "--label", "repo"}, want{code: 1, stderr: `bad --label: "repo" is not key=value`}},
		{"refuses an applied item without a version", []string{"record", "--applied", "k-1"}, want{code: 1, stderr: `bad --applied: "k-1" is not id:version`}},
		{"refuses an unknown action", []string{"show"}, want{code: 1, stderr: `^nodloop run: unknown action "show"\n`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			text, data := filepath.Join(dir, "out.txt"), filepath.Join(dir, "out.json")
			require.NoError(t, os.WriteFile(text, []byte("use git -C"), 0o600))
			require.NoError(t, os.WriteFile(data, []byte(`{"answer":1}`), 0o600))
			r := strings.NewReplacer("{text}", text, "{json}", data)
			args := make([]string, 0, len(tc.args))
			for _, a := range tc.args {
				args = append(args, r.Replace(a))
			}
			getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
			var stdout, stderr bytes.Buffer

			code := runRun(args, getenv, func() time.Time { return at }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			if tc.want.code != 0 {
				assert.Regexp(t, tc.want.stderr, stderr.String())
				return
			}
			store, err := tracefile.New(records)
			require.NoError(t, err)
			got, err := store.Get(t.Context(), strings.TrimSpace(stdout.String()))
			require.NoError(t, err)
			assert.Equal(t, trace.NameRun, got.Name)
			assert.Equal(t, "session", got.Producer)
			assert.Equal(t, tc.want.labels, got.Labels)
			assert.JSONEq(t, tc.want.input, string(got.Input))
			assert.JSONEq(t, tc.want.output, string(got.Output))
		})
	}
}

// The loop on a run needs no data dir: a run, an edit of it and an outcome
func TestRunFeedbackOnRun(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	dir, records := t.TempDir(), t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
	now := func() time.Time { return at }
	out, edited := filepath.Join(dir, "out.txt"), filepath.Join(dir, "edited.json")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
	require.NoError(t, os.WriteFile(edited, []byte(`"git -C repo status"`), 0o600))
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
	run := strings.TrimSpace(id.String())
	tcs := []struct {
		name string
		args []string
		want string
	}{
		{"an edit of a run is any JSON", []string{"add", "--trace", run, "--verdict", "edit", "--reason-code", "other", "--edited", edited}, run + "\tedit\tauthor\n"},
		{"an outcome of a run", []string{"outcome", "--trace", run, "--result", "confirmed"}, run + "\tconfirmed\tauthor\n"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := runFeedback(tc.args, getenv, now, &stdout, &stderr)

			require.Equal(t, 0, code, stderr.String())
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}
