package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestRunTraceJSON(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	run := `{"id":"r1","time":"2026-01-01T00:00:00.123456789Z","name":"run","session_id":"session-one"}`
	other := `{"id":"r2","time":"2026-01-01T00:00:00.123456789Z","name":"check","session_id":"session-two"}`
	tcs := []struct {
		name string
		args []string
		want string
	}{
		{"precise metadata excludes text", []string{"list", "--json"}, "[" + other + "," + run + "]\n"},
		{"name and session filters compose", []string{"list", "--json", "--name", "run", "--session", "session-one"},
			"[" + run + "]\n"},
		{"empty JSON list is an array", []string{"list", "--json", "--session", "absent"}, "[]\n"},
		{"limit keeps the newest append", []string{"list", "--json", "--limit", "1"}, "[" + other + "]\n"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			store, err := tracefile.New(dir)
			require.NoError(t, err)
			traces := []trace.Trace{
				{ID: "r1", Name: trace.NameRun, Time: at, SessionID: "session-one", Subject: "private\tname\nrow",
					Input: json.RawMessage(`{"private":"input"}`), Output: json.RawMessage(`"private output"`)},
				{ID: "r2", Name: trace.NameCheck, Time: at, SessionID: "session-two", Input: json.RawMessage(`{}`)},
			}
			for _, tr := range traces {
				require.NoError(t, store.Append(t.Context(), tr.Record()))
			}
			var stdout, stderr bytes.Buffer
			code := runTrace(tc.args, envOf(map[string]string{envRecordDir: dir}),
				func() time.Time { return at }, &stdout, &stderr)
			assert.Equal(t, 0, code, stderr.String())
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

func TestRunTraceJSONWriteFailure(t *testing.T) {
	tcs := []struct {
		name string
		args []string
	}{
		{"metadata JSON", []string{"list", "--json"}}, {"shown trace", []string{"show", "r1"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			store, err := tracefile.New(dir)
			require.NoError(t, err)
			run := trace.Trace{ID: "r1", Name: trace.NameRun, Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			require.NoError(t, store.Append(t.Context(), run.Record()))
			reader, writer := io.Pipe()
			require.NoError(t, reader.Close())
			t.Cleanup(func() { assert.NoError(t, writer.Close()) })
			var stderr bytes.Buffer
			code := runTrace(tc.args, envOf(map[string]string{envRecordDir: dir}),
				func() time.Time { return run.Time }, writer, &stderr)
			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), io.ErrClosedPipe.Error())
		})
	}
}

func TestRunTraceCorruptList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "traces.jsonl"), []byte("not JSON\n"), 0600))
	var stdout, stderr bytes.Buffer
	code := runTrace([]string{"list", "--json"}, envOf(map[string]string{envRecordDir: dir}),
		func() time.Time { return time.Time{} }, &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "corrupt lines")
}

func TestRunTrace(t *testing.T) {
	dir := t.TempDir()
	store, err := tracefile.New(dir)
	require.NoError(t, err)
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	older := trace.Trace{
		ID: trace.NewID(base), Name: trace.NameRun, Producer: "session", Subject: "commit", Time: base, Input: json.RawMessage(`{}`),
		Usage: trace.Usage{CostUSD: 0.01},
	}
	newer := trace.Trace{
		ID: trace.NewID(base.Add(time.Second)), Name: trace.NameCheck, SessionID: "c-1", Subject: "c-1", Time: base.Add(time.Second),
		Input: json.RawMessage(`{}`),
	}
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, older.Record()))
	require.NoError(t, store.Append(ctx, newer.Record()))
	shown, err := json.MarshalIndent(older, "", "  ")
	require.NoError(t, err)
	regular := filepath.Join(t.TempDir(), "regular")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	blocked := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(blocked, "traces.jsonl.bak"), 0o700))
	brokenHome := t.TempDir()
	hookSaveConfig(t, brokenHome, "not JSON")
	withDir := map[string]string{envRecordDir: dir}
	olderLine := older.ID + "\t2026-09-22T12:00:00Z\trun\tcommit\t$0.0100\n"
	newerLine := newer.ID + "\t2026-09-22T12:00:01Z\tcheck\tc-1\t$0.0000\n"
	type args struct {
		args []string
		env  map[string]string
	}
	type want struct {
		code   int
		stdout string
		// Regexp matched against stderr
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no action fails", args{nil, withDir}, want{1, "", `^nodloop trace: an action is required`}},
		{"list prints newest first", args{[]string{"list"}, withDir}, want{0, newerLine + olderLine, `^$`}},
		{
			"list by name excludes others",
			args{[]string{"list", "--name", string(trace.NameRun)}, withDir},
			want{0, olderLine, `^$`},
		},
		{
			"list by an unknown name fails",
			args{[]string{"list", "--name", "diagnosis"}, withDir},
			want{1, "", `^nodloop trace: unknown trace name "diagnosis". Use one of run, check, classify, extract, ` +
				`replay, consolidate, refill, turn, calls, compact, instructions, capture, ` +
				`decide, knob, anchor, expose, answer, intervention, rollup\n\n`},
		},
		{"list limit keeps the newest", args{[]string{"list", "--limit", "1"}, withDir}, want{0, newerLine, `^$`}},
		{"show prints the trace", args{[]string{"show", older.ID}, withDir}, want{0, string(shown) + "\n", `^$`}},
		{
			"show takes flags after the id",
			args{[]string{"show", older.ID, "--record-dir", dir}, nil},
			want{0, string(shown) + "\n", `^$`},
		},
		{
			"show takes flags before the id",
			args{[]string{"show", "--record-dir", dir, older.ID}, nil},
			want{0, string(shown) + "\n", `^$`},
		},
		{
			"show of an unknown id fails",
			args{[]string{"show", "nope"}, withDir},
			want{1, "", `^nodloop trace: not found: "nope"\n$`},
		},
		{
			"show without an id fails",
			args{[]string{"show"}, withDir},
			want{1, "", `^nodloop trace: an id is required\n\nusage:`},
		},
		{
			"no home and no record dir fails",
			args{[]string{"list"}, nil},
			want{1, "", `^nodloop trace: home directory unknown: `},
		},
		{
			"record dir that is a file fails",
			args{[]string{"list", "--record-dir", regular}, withDir},
			want{1, "", `^nodloop trace: record dir: `},
		},
		{"trace store cannot open", args{[]string{"list", "--record-dir", blocked}, withDir},
			want{1, "", `^nodloop trace: trace file:`}},
		{"config cannot decode", args{[]string{"list"}, map[string]string{"HOME": brokenHome, envRecordDir: dir}},
			want{1, "", `^nodloop trace: .*config`}},
		{
			"unknown flag fails",
			args{[]string{"list", "--nope"}, withDir},
			want{1, "", `^flag provided but not defined: -nope\n`},
		},
		{
			"unknown action fails",
			args{[]string{"bogus"}, withDir},
			want{1, "", `^nodloop trace: unknown action "bogus"\n\nusage:`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv := func(k string) string { return tc.args.env[k] }
			var stdout, stderr bytes.Buffer

			got := runTrace(tc.args.args, getenv, func() time.Time { return base }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}
