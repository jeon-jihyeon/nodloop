package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

var pruneNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// A home whose config.json holds the retention and a record directory holding
// an old unjudged run with its classify trace and an old judged run and a recent run
// An empty retention writes no config.json
func pruneHome(t *testing.T, retention string) (getenv func(string) string, records string) {
	t.Helper()
	home, records := t.TempDir(), t.TempDir()
	if retention != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".nodloop"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(home, ".nodloop", "config.json"), []byte(`{"retention":`+retention+`}`), 0o600))
	}
	ctx := context.Background()
	traces, err := tracefile.New(records)
	require.NoError(t, err)
	verdicts, err := feedbackfile.New(records)
	require.NoError(t, err)
	old := pruneNow.Add(-40 * 24 * time.Hour)
	for _, tr := range []trace.Trace{
		{ID: "old-unjudged", Name: trace.NameRun, Producer: "bot", Time: old, Input: []byte(`{}`)},
		{ID: "old-classify", Name: trace.NameClassify, Ref: "old-unjudged", Time: old, Input: []byte(`{}`)},
		{ID: "old-judged", Name: trace.NameRun, Producer: "bot", Time: old, Input: []byte(`{}`)},
		{ID: "recent", Name: trace.NameRun, Producer: "bot", Time: pruneNow.Add(-time.Hour), Input: []byte(`{}`)},
	} {
		require.NoError(t, traces.Append(ctx, tr.Record()))
	}
	fb, err := feedback.New("old-judged", feedback.VerdictApprove, "", "", nil, "ann", old)
	require.NoError(t, err)
	require.NoError(t, verdicts.Append(ctx, fb.Record()))
	return func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }, records
}

// The ids of the traces left in the record directory in file order
func traceIDs(t *testing.T, records string) []string {
	t.Helper()
	store, err := tracefile.New(records)
	require.NoError(t, err)
	all, err := store.List(context.Background(), record.TraceFilter{})
	require.NoError(t, err)
	var ids []string
	for i := len(all) - 1; i >= 0; i-- {
		ids = append(ids, all[i].ID)
	}
	return ids
}

func TestRunRecordsPrune(t *testing.T) {
	type args struct {
		retention string
		args      []string
		// Appended to feedback.jsonl so a line that fails to decode can fail the plan
		feedbackTail string
	}
	type want struct {
		code   int
		stdout string
		// A pattern of stderr
		stderr string
		left   []string
		// Whether prune.json holds a mark afterwards
		marked bool
	}
	every := []string{"old-unjudged", "old-classify", "old-judged", "recent"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a prune removes the old unjudged run and its classify trace", args{"30", []string{"prune"}, ""}, want{
			0, "pruned 2 traces older than 2026-09-12T12:00:00Z\n", "^$", []string{"old-judged", "recent"}, true,
		}},
		{"a dry run removes nothing", args{"30", []string{"prune", "--dry-run"}, ""}, want{
			0, "would roll up 0 closed cycles and prune 1 runs, 1 classify traces and 0 turn, instructions and capture traces older than " +
				"2026-09-12T12:00:00Z, kept 1 judged, cited or first or last to use an item\n", "^$",
			every, false,
		}},
		{"a longer retention keeps the old runs", args{"60", []string{"prune"}, ""}, want{
			0, "pruned 0 traces older than 2026-08-13T12:00:00Z\n", "^$", every, true,
		}},
		{"no retention names the setting", args{"", []string{"prune"}, ""}, want{1, "", "^nodloop records: .*nodloop config retention", every, false}},
		{"a retention under a week is refused", args{"3", []string{"prune"}, ""}, want{1, "", "^nodloop records: retain: retention too short: 3", every, false}},
		{"a plan that cannot read a verdict fails", args{"30", []string{"prune"}, "not json\n"}, want{1, "", "^nodloop records: .*corrupt", every, false}},
		{"a dry run that cannot read a verdict fails", args{"30", []string{"prune", "--dry-run"}, "not json\n"}, want{1, "", "^nodloop records: .*corrupt", every, false}},
		{"an unknown action is refused", args{"30", []string{"compact"}, ""}, want{1, "", `^nodloop records: unknown action "compact"`, every, false}},
		{"a dry run of the daily prune is refused and removes nothing", args{"30", []string{"prune", "--dry-run", "--daily"}, ""}, want{
			1, "", "^nodloop records: --dry-run and --daily exclude each other\n\nusage:", every, false,
		}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records := pruneHome(t, tc.args.retention)
			appendTo(t, filepath.Join(records, "feedback.jsonl"), tc.args.feedbackTail)
			var stdout, stderr bytes.Buffer

			code := runRecords(tc.args.args, getenv, func() time.Time { return pruneNow }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
			assert.Equal(t, tc.want.left, traceIDs(t, records))
			mark, err := retain.NewMarker(records).Last(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want.marked, !mark.At.IsZero())
		})
	}
}

// A removal that fails after its plan fails the command and leaves every trace
func TestRunRecordsPruneRemovalFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not stop root")
	}
	getenv, records := pruneHome(t, "30")
	require.NoError(t, os.WriteFile(filepath.Join(records, retain.MarkerFile), nil, 0o600))
	// A directory that takes no new file refuses the temp file of the rewrite
	require.NoError(t, os.Chmod(records, 0o500))
	t.Cleanup(func() { _ = os.Chmod(records, 0o700) })
	var stdout, stderr bytes.Buffer

	code := runRecords([]string{"prune"}, getenv, func() time.Time { return pruneNow }, &stdout, &stderr)

	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())
	assert.Regexp(t, "^nodloop records: trace file: prune failed: ", stderr.String())
	assert.Equal(t, []string{"old-unjudged", "old-classify", "old-judged", "recent"}, traceIDs(t, records))
}

// Appends the text to the file and appends nothing for an empty text
func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(text)
	require.NoError(t, errors.Join(err, f.Close()))
}

// Recording a run starts the daily prune and its failure goes to stderr while the run stays recorded
func TestRunRecordStartsTheDailyPrune(t *testing.T) {
	type args struct {
		retention string
		// Appended to feedback.jsonl so a line that fails to decode can fail the prune
		feedbackTail string
	}
	type want struct {
		left int
		// A pattern of stderr
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the old unjudged run and its classify trace go once the run is recorded", args{"30", ""}, want{3, "^$"}},
		{"a prune that cannot read a verdict removes nothing and says so", args{"30", "not json\n"}, want{5, "^nodloop: retention: .*corrupt"}},
		{"a retention under a week removes nothing and says so", args{"3", ""}, want{5, "^nodloop: retention: retain: retention too short"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records := pruneHome(t, tc.args.retention)
			appendTo(t, filepath.Join(records, "feedback.jsonl"), tc.args.feedbackTail)
			out := filepath.Join(t.TempDir(), "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("answer"), 0o600))
			var stdout, stderr bytes.Buffer

			code := runRun([]string{"record", "--producer", "bot", "--output", out}, getenv, func() time.Time { return pruneNow }, &stdout, &stderr)

			assert.Equal(t, 0, code)
			assert.Contains(t, traceIDs(t, records), strings.TrimSpace(stdout.String()))
			assert.Len(t, traceIDs(t, records), tc.want.left)
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}

// The stop hook starts records prune --daily in a process of its own only when the marker says a prune is due
// The started command prunes and a second one within the day finds the marker fresh
func TestRunHookStopStartsTheDailyPrune(t *testing.T) {
	type args struct {
		retention string
		// The marker before the stop and none when empty
		marker string
	}
	type want struct {
		started [][]string
		// The exit codes of each started command run twice
		codes []int
		left  int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a due prune is started and prunes", args{"30", ""}, want{[][]string{{"records", "prune", "--daily"}}, []int{0, 0}, 3}},
		{"a fresh marker starts nothing", args{"30", `{"at":"2026-10-12T06:00:00Z"}`}, want{nil, nil, 5}},
		{"no retention starts nothing", args{"", ""}, want{nil, nil, 5}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records := pruneHome(t, tc.args.retention)
			// An empty marker reads as no marker so every case writes one
			require.NoError(t, os.WriteFile(filepath.Join(records, retain.MarkerFile), []byte(tc.args.marker), 0o600))
			repo := filepath.Join(t.TempDir(), "nodloop")
			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
			stdin := `{"session_id":"s1","cwd":"` + repo + `","last_assistant_message":"done"}`
			now := func() time.Time { return pruneNow }
			var started [][]string
			start := besideCurate(func(args []string) error {
				started = append(started, args)
				return nil
			})
			var stderr, out bytes.Buffer

			code := runHook([]string{"stop"}, getenv, start, now, strings.NewReader(stdin), &bytes.Buffer{}, &stderr)
			// Each started command runs twice and the second finds the marker fresh
			var codes []int
			for _, args := range started {
				codes = append(codes, runRecords(args[1:], getenv, now, &out, &stderr), runRecords(args[1:], getenv, now, &out, &stderr))
			}

			assert.Equal(t, 0, code)
			assert.Empty(t, stderr.String())
			assert.Equal(t, tc.want.started, started)
			assert.Equal(t, tc.want.codes, codes)
			assert.Empty(t, out.String(), "the daily prune prints nothing")
			assert.Len(t, traceIDs(t, records), tc.want.left)
		})
	}
}

// report loop names the start of the retention window in its first line
func TestRunReportLoopWindow(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"retention prints the window first and counts its runs", "30", "window\t2026-09-12T12:00:00Z\nloop\truns 1\t"},
		{"no retention counts every run", "", "window\tall\nloop\truns 3\t"},
		{"a retention under a week counts every run", "3", "window\tall\nloop\truns 3\t"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, _ := pruneHome(t, tc.args)
			var stdout, stderr bytes.Buffer

			code := runReport([]string{"loop"}, getenv, func() time.Time { return pruneNow }, &stdout, &stderr)

			assert.Equal(t, 0, code, stderr.String())
			assert.True(t, strings.HasPrefix(stdout.String(), tc.want), stdout.String())
		})
	}
}

// With retention 30 a stream of sixty days at one run per 5 minutes keeps thirty days of runs and every judged run
// report loop reads the same before and after the prune
func TestRecordsPruneSixtyDaysOfStream(t *testing.T) {
	getenv, records := pruneHome(t, "30")
	var traces, verdicts bytes.Buffer
	// One judged run every 500 runs and 8640 runs in the thirty days before the cutoff so 18 of them are judged
	runs, window, judgedOld := 60*288, 30*288, 18
	for i := range runs {
		at := pruneNow.Add(-60*24*time.Hour + time.Duration(i)*5*time.Minute)
		tr := trace.Trace{ID: trace.NewID(at), Name: trace.NameRun, Producer: "stream-bot", Time: at, Input: []byte(`{"applied":[]}`)}
		require.NoError(t, json.NewEncoder(&traces).Encode(tr))
		if i%500 != 0 {
			continue
		}
		fb, err := feedback.New(tr.ID, feedback.VerdictApprove, "", "", nil, "ann", at)
		require.NoError(t, err)
		require.NoError(t, json.NewEncoder(&verdicts).Encode(fb))
	}
	require.NoError(t, os.WriteFile(filepath.Join(records, "traces.jsonl"), traces.Bytes(), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(records, "feedback.jsonl"), verdicts.Bytes(), 0o600))
	now := func() time.Time { return pruneNow }
	run := func(cmd func([]string, func(string) string, func() time.Time, io.Writer, io.Writer) int, args ...string) string {
		var stdout, stderr bytes.Buffer
		require.Equal(t, 0, cmd(args, getenv, now, &stdout, &stderr), stderr.String())
		return stdout.String()
	}
	before := run(runReport, "loop")

	planned := run(runRecords, "prune", "--dry-run")
	pruned := run(runRecords, "prune")

	assert.Equal(t, before, run(runReport, "loop"))
	assert.Len(t, traceIDs(t, records), window+judgedOld)
	assert.Contains(t, planned, fmt.Sprintf("prune %d runs, 0 classify traces and 0 turn, instructions and capture traces", runs-window-judgedOld))
	assert.Contains(t, planned, fmt.Sprintf("kept %d judged", judgedOld))
	assert.Contains(t, pruned, fmt.Sprintf("pruned %d traces", runs-window-judgedOld))
}
