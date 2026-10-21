package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
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
			want{input: `{"applied":[]}`, output: `{"answer":1}`},
		},
		{"refuses a run without output", []string{"record", "--producer", "session"}, want{code: 1, stderr: `^nodloop run: record: --output is required\n`}},
		{
			"refuses an output that names no regular file",
			[]string{"record", "--producer", "session", "--output", os.DevNull},
			want{code: 1, stderr: `^nodloop run: --output: ` + regexp.QuoteMeta(atomicfile.ErrNotRegular.Error())},
		},
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
			found, err := store.Get(t.Context(), strings.TrimSpace(stdout.String()))
			require.NoError(t, err)
			got := trace.Restore(found)
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
		{"an edit of a run is any JSON", []string{"add", "--trace", run, "--verdict", "edit", "--reason-code", "other", "--edited", edited, "--reviewer", "ann"},
			run + "\tedit\tann\n"},
		{"an outcome of a run", []string{"outcome", "--trace", run, "--result", "confirmed", "--reviewer", "ann"}, run + "\tconfirmed\tann\n"},
		{"a verdict without a reviewer is refused", []string{"add", "--trace", run, "--verdict", "approve"}, ""},
		{"an outcome without a reviewer is refused", []string{"outcome", "--trace", run, "--result", "confirmed"}, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := runFeedback(tc.args, getenv, now, trace.ProofNone, &stdout, &stderr)

			assert.Equal(t, tc.want == "", code == 1, stderr.String())
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

// The loop of a run with no data dir: a run, a proposal scoped to its labels, an approval and the items the next run gets
func TestRunKnowledgeForRuns(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	type args struct {
		// The knowledge commands that succeed before the one under test with {run} for the recorded run
		before [][]string
		args   []string
	}
	type want struct {
		code int
		// Regexps matched against stdout and stderr
		stdout, stderr string
	}
	propose := []string{"propose", "--id", "git-c", "--kind", "judgment", "--content", "use git -C instead of cd", "--trace", "{run}", "--producer", "session"}
	scoped := slices.Concat(propose, []string{"--label", "repo=nodloop"})
	approved := [][]string{scoped, {"approve", "git-c", "--version", "1", "--approver", "ann"}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a label no run carries is refused",
			args{args: slices.Concat(propose, []string{"--label", "repo=nodlop"})},
			want{1, `^$`, `no run of session carries repo=nodlop`},
		},
		{
			"a proposal without a producer is refused",
			args{args: []string{"propose", "--kind", "judgment", "--content", "x", "--trace", "{run}", "--label", "repo=nodloop"}},
			want{1, `^$`, `propose: --producer or --from is required`},
		},
		{
			"a verified basis without an outcome is refused",
			args{args: slices.Concat(scoped, []string{"--basis", "verified"})},
			want{1, `^$`, `propose: --evidence-outcome is required with --basis verified`},
		},
		{
			"a proposal is scoped to the labels of the run and counts the chars of the place",
			args{args: scoped},
			want{0, `(?s)scope\truns of session\. repo=nodloop\n.*of 9000 chars in runs of session`, `^$`},
		},
		{"the proposal is approved", args{before: approved[:1], args: approved[1]}, want{0, ``, `^$`}},
		{
			"a run of the repo gets the item",
			args{before: approved, args: []string{"for", "--producer", "session", "--label", "repo=nodloop"}},
			want{0, `^git-c\tv1\tjudgment\tuse git -C instead of cd\ntotal\t1 items\t`, `^$`},
		},
		{
			"a run of another repo gets nothing",
			args{before: approved, args: []string{"for", "--producer", "session", "--label", "repo=other"}},
			want{0, `^total\t0 items\t0 chars\n`, `^$`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
			now := func() time.Time { return at }
			out := filepath.Join(dir, "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
			var id bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
			place := strings.NewReplacer("{run}", strings.TrimSpace(id.String()))
			for _, args := range tc.args.before {
				var stderr bytes.Buffer
				require.Equal(t, 0, runKnowledge(runPlaced(place, args), getenv, nil, now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
			}
			var stdout, stderr bytes.Buffer

			code := runKnowledge(runPlaced(place, tc.args.args), getenv, nil, now, atTerminal, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Regexp(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}

// The arguments with their placeholders replaced
func runPlaced(place *strings.Replacer, args []string) []string {
	placed := make([]string, len(args))
	for i, a := range args {
		placed[i] = place.Replace(a)
	}
	return placed
}

// knowledge for prints the withheld items of a drawn key and run record with the same key records the run in its arm
func TestRunHoldoutRoundTrip(t *testing.T) {
	home, records, _ := hookHome(t, "use git -C")
	require.Equal(t, 0, runConfig([]string{"holdout", "0.999"}, func(k string) string { return map[string]string{"HOME": home}[k] }, atTerminal, &bytes.Buffer{}, &bytes.Buffer{}))
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	out := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("answer"), 0o600))
	var items, id, stderr bytes.Buffer

	forCode := runKnowledge([]string{"for", "--producer", "session", "--label", "repo=nodloop", "--holdout-key", "w-1"}, getenv, nil, time.Now, atTerminal, &items, &stderr)
	recordCode := runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--withheld", "git-c:1", "--holdout-key", "w-1", "--output", out},
		getenv, time.Now, &id, &stderr)

	require.Equal(t, 0, forCode, stderr.String())
	require.Equal(t, 0, recordCode, stderr.String())
	assert.Equal(t, "withheld\tgit-c\tv1\ntotal\t0 items\t0 chars\n", items.String())
	store, err := tracefile.New(records)
	require.NoError(t, err)
	got, err := store.Get(t.Context(), strings.TrimSpace(id.String()))
	require.NoError(t, err)
	assert.JSONEq(t, `{"applied":[],"withheld":[{"id":"git-c","version":1}],"drawn":true,"share":0.999,"scheme":1,"unit":"284c9f5258848319"}`, string(got.Input))
}
