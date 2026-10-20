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
