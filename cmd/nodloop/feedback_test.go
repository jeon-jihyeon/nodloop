package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A verdict of feedback add carries the terminal proof of its process and report loop counts it by that proof
func TestRunFeedbackAddProof(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	type args struct {
		reviewer string
		proof    trace.Proof
	}
	type want struct {
		proof string
		// The verdict counts of the loop totals
		totals string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a script proves nobody so its verdict counts as named", args{"ann", trace.ProofNone}, want{"none", `"judged":0,"named":1,"inferred":0`}},
		{"a person's terminal proves the person so the verdict counts as judged", args{"ann", atTerminal}, want{"terminal", `"judged":1,"named":0,"inferred":0`}},
		{"an inferred verdict carries no proof even at a terminal", args{feedback.ReviewerSession, atTerminal}, want{"", `"judged":0,"named":0,"inferred":1`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
			now := func() time.Time { return at }
			out := filepath.Join(dir, "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("git -C repo status"), 0o600))
			var id, report, stderr bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
			args := []string{"add", "--trace", strings.TrimSpace(id.String()), "--verdict", "approve", "--reviewer", tc.args.reviewer}

			code := runFeedback(args, getenv, now, tc.args.proof, &bytes.Buffer{}, &stderr)

			require.Equal(t, 0, code, stderr.String())
			store, err := feedbackfile.New(records)
			require.NoError(t, err)
			found, err := store.List(t.Context(), record.FeedbackFilter{})
			require.NoError(t, err)
			require.Len(t, found, 1)
			assert.Equal(t, tc.want.proof, found[0].Proof)
			require.Equal(t, 0, runReport([]string{"loop", "--json"}, getenv, now, &report, &stderr), stderr.String())
			assert.Contains(t, report.String(), tc.want.totals)
		})
	}
}

// A verdict that names no run, an edit file that cannot be read or a verdict file that cannot take it records nothing
func TestRunFeedbackAddRefused(t *testing.T) {
	type args struct {
		// The flags after the run and the reviewer with {run} for the recorded run and {dir} for the home
		flags []string
		// A directory made under the records
		dir string
	}
	tcs := []struct {
		name string
		args args
		// What stderr holds
		want string
	}{
		{"no verdict", args{[]string{"--trace", "{run}"}, ""}, "add: --trace and a --verdict of approve or edit or reject or withdraw is required"},
		{"an edit file that cannot be read", args{[]string{"--trace", "{run}", "--verdict", "edit", "--edited", "{dir}/missing.json"}, ""}, "--edited: stat "},
		{"an edit file that names a folder", args{[]string{"--trace", "{run}", "--verdict", "edit", "--edited", "{dir}"}, ""}, "--edited: not a regular file"},
		{"a run that is not recorded", args{[]string{"--trace", "r-unknown", "--verdict", "reject"}, ""}, record.ErrTraceNotFound.Error()},
		{"a verdict file that cannot open", args{[]string{"--trace", "{run}", "--verdict", "reject"}, "feedback.jsonl.bak"}, "feedback file: "},
		{"a verdict file that takes no line", args{[]string{"--trace", "{run}", "--verdict", "reject"}, "feedback.jsonl"}, "feedback file: append failed"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			getenv := envOf(map[string]string{"HOME": dir, envRecordDir: records})
			now := func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
			out := filepath.Join(dir, "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("git -C repo status"), 0o600))
			var id, stderr bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
			require.NoError(t, os.MkdirAll(filepath.Join(records, tc.args.dir), 0o755))
			placed := strings.NewReplacer("{run}", strings.TrimSpace(id.String()), "{dir}", dir)
			args := []string{"add", "--reviewer", "ann"}
			for _, f := range tc.args.flags {
				args = append(args, placed.Replace(f))
			}

			code := runFeedback(args, getenv, now, atTerminal, &bytes.Buffer{}, &stderr)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), tc.want)
			// A missing file or a directory in its place reads as no verdict
			verdicts, _ := os.ReadFile(filepath.Join(records, "feedback.jsonl"))
			assert.Empty(t, string(verdicts))
		})
	}
}

// The reserved name of the capture is refused from a person's terminal and from a script and nothing is recorded
func TestRunFeedbackReviewerReserved(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	type args struct {
		action []string
		proof  trace.Proof
	}
	tcs := []struct {
		name string
		args args
	}{
		{"a verdict at a person's terminal", args{[]string{"add", "--verdict", "reject", "--reason", "wrong"}, atTerminal}},
		{"a verdict from a script", args{[]string{"add", "--verdict", "approve"}, trace.ProofNone}},
		{"an outcome", args{[]string{"outcome", "--result", "confirmed"}, trace.ProofNone}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": dir, envRecordDir: records}[k] }
			now := func() time.Time { return at }
			out := filepath.Join(dir, "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("git -C repo status"), 0o600))
			var id, stderr bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
			args := slices.Concat(tc.args.action, []string{"--trace", strings.TrimSpace(id.String()), "--reviewer", feedback.ReviewerNodloop})

			code := runFeedback(args, getenv, now, tc.args.proof, &bytes.Buffer{}, &stderr)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), feedback.ErrReviewerReserved.Error())
			verdicts, err := feedbackfile.New(records)
			require.NoError(t, err)
			found, err := verdicts.List(t.Context(), record.FeedbackFilter{})
			require.NoError(t, err)
			outcomes, err := feedbackfile.NewOutcomeStore(records)
			require.NoError(t, err)
			checks, err := outcomes.List(t.Context(), "")
			require.NoError(t, err)
			assert.Empty(t, found)
			assert.Empty(t, checks)
		})
	}
}

func TestRunFeedbackOutcomeActor(t *testing.T) {
	type args struct {
		actor string
		proof trace.Proof
	}
	type want struct {
		actor, proof  string
		code, records int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a script has unknown provenance", args{"", trace.ProofNone}, want{"", "", 0, 1}},
		{"a terminal defaults to person", args{"", atTerminal}, want{"person", "terminal", 0, 1}},
		{"a code check keeps no terminal proof", args{"code", atTerminal}, want{"code", "", 0, 1}},
		{"a model check keeps no terminal proof", args{"model", atTerminal}, want{"model", "", 0, 1}},
		{"a person's claim proves nobody", args{"person", trace.ProofNone}, want{"person", "none", 0, 1}},
		{"explicit unknown stays unknown at a terminal", args{"unknown", atTerminal}, want{"unknown", "", 0, 1}},
		{"invalid actors record nothing", args{"future", atTerminal}, want{"", "", 1, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, records := t.TempDir(), t.TempDir()
			getenv := envOf(map[string]string{"HOME": dir, envRecordDir: records})
			now := func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
			out := filepath.Join(dir, "output.json")
			require.NoError(t, os.WriteFile(out, []byte(`{"answer":"ok"}`), 0600))
			var id, stderr bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--output", out}, getenv, now, &id, &stderr))
			flags := []string{"outcome", "--trace", strings.TrimSpace(id.String()), "--result", "confirmed", "--reviewer", "ann"}
			flags = append(flags, "--actor", tc.args.actor)
			code := runFeedback(flags, getenv, now, tc.args.proof, &bytes.Buffer{}, &stderr)
			store, err := feedbackfile.NewOutcomeStore(records)
			require.NoError(t, err)
			found, err := store.List(t.Context(), "")
			require.NoError(t, err)
			var actor, proof string
			for _, o := range found {
				actor, proof = o.Actor, o.Proof
			}
			assert.Equal(t, tc.want, want{actor, proof, code, len(found)})
		})
	}
}

func TestRunFeedbackOutcomeFailures(t *testing.T) {
	type args struct {
		flags     []string
		directory string
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"missing trace", args{[]string{"--result", "confirmed"}, ""}, "--trace and a --result"},
		{"missing result", args{[]string{"--trace", "{run}"}, ""}, "--trace and a --result"},
		{"run not found", args{[]string{"--trace", "absent", "--result", "confirmed"}, ""}, "not found"},
		{"outcome store cannot open", args{[]string{"--trace", "{run}", "--result", "confirmed"}, "outcomes.jsonl.bak"},
			"feedback file"},
		{"outcome cannot append", args{[]string{"--trace", "{run}", "--result", "confirmed"}, "outcomes.jsonl"},
			"append failed"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			getenv := envOf(map[string]string{"HOME": home, envRecordDir: records})
			now := func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
			output := filepath.Join(home, "output.json")
			require.NoError(t, os.WriteFile(output, []byte(`"answer"`), 0600))
			var id, stderr bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "acme", "--output", output}, getenv, now, &id, &stderr))
			require.NoError(t, os.MkdirAll(filepath.Join(records, tc.args.directory), 0700))
			flags := []string{"outcome", "--reviewer", "ann"}
			for _, flag := range tc.args.flags {
				flags = append(flags, strings.ReplaceAll(flag, "{run}", strings.TrimSpace(id.String())))
			}
			code := runFeedback(flags, getenv, now, trace.ProofNone, &bytes.Buffer{}, &stderr)
			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), tc.want)
		})
	}
}
