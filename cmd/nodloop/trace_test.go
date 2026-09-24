package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestRunTrace(t *testing.T) {
	dir := t.TempDir()
	store, err := tracefile.New(dir)
	require.NoError(t, err)
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	older := trace.Trace{
		ID: trace.NewID(base), Name: trace.NameDiagnose, Subject: "tq-001", Time: base, Input: json.RawMessage(`{}`),
		Usage: trace.Usage{CostUSD: 0.01},
	}
	newer := trace.Trace{
		ID: trace.NewID(base.Add(time.Second)), Name: trace.NameContext, Subject: "tq-002", Time: base.Add(time.Second),
		Input: json.RawMessage(`{"mode":"interactive"}`),
	}
	ctx := context.Background()
	require.NoError(t, store.Append(ctx, older))
	require.NoError(t, store.Append(ctx, newer))
	shown, err := json.MarshalIndent(older, "", "  ")
	require.NoError(t, err)
	regular := filepath.Join(t.TempDir(), "regular")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	data := testkit.DemoDir(t)
	withDir := map[string]string{envFileDir: data, envRecordDir: dir}
	olderLine := older.ID + "\t2026-09-22T12:00:00Z\tdiagnose\ttq-001\t$0.0100\n"
	newerLine := newer.ID + "\t2026-09-22T12:00:01Z\tcontext\ttq-002\t$0.0000\n"
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
		{"list prints newest first", args{[]string{"list"}, withDir}, want{0, newerLine + olderLine, `^$`}},
		{
			"list by name excludes others",
			args{[]string{"list", "--name", string(trace.NameDiagnose)}, withDir},
			want{0, olderLine, `^$`},
		},
		{
			"list by an unknown name fails",
			args{[]string{"list", "--name", "diagnosis"}, withDir},
			want{1, "", `^nodloop trace: unknown trace name "diagnosis". Use one of context, select, diagnose, revise\n\n`},
		},
		{"list limit keeps the newest", args{[]string{"list", "--limit", "1"}, withDir}, want{0, newerLine, `^$`}},
		{
			"pending lists unreviewed context traces",
			args{[]string{"pending"}, withDir},
			want{0, newer.ID + "\t2026-09-22T12:00:01Z\ttq-002\n", `^$`},
		},
		{"show prints the trace", args{[]string{"show", older.ID}, withDir}, want{0, string(shown) + "\n", `^$`}},
		{
			"show takes flags after the id",
			args{[]string{"show", older.ID, "--data-dir", data, "--record-dir", dir}, nil},
			want{0, string(shown) + "\n", `^$`},
		},
		{
			"show takes flags before the id",
			args{[]string{"show", "--data-dir", data, "--record-dir", dir, older.ID}, nil},
			want{0, string(shown) + "\n", `^$`},
		},
		{
			"show of an unknown id fails",
			args{[]string{"show", "nope"}, withDir},
			want{1, "", `^nodloop trace: trace "nope": `},
		},
		{
			"show without an id fails",
			args{[]string{"show"}, withDir},
			want{1, "", `^nodloop trace: an id is required\n\nusage:`},
		},
		{
			"missing data dir env fails",
			args{[]string{"list"}, nil},
			want{1, "", `^nodloop trace: ` + envFileDir + ` is not set: `},
		},
		{
			"record dir that is a file fails",
			args{[]string{"list", "--record-dir", regular}, withDir},
			want{1, "", `^nodloop trace: record dir: `},
		},
		{
			"unknown source fails",
			args{[]string{"list", "--source", "postgres"}, withDir},
			want{1, "", `^nodloop trace: unknown source: "postgres"\n$`},
		},
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
