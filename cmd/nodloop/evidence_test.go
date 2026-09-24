package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestRunEvidence(t *testing.T) {
	data := testkit.DemoDir(t)
	withDir := map[string]string{envFileDir: data}
	events, err := os.ReadFile("testdata/evidence_events.txt")
	require.NoError(t, err)
	paragraphs, err := os.ReadFile("testdata/evidence_paragraphs.txt")
	require.NoError(t, err)
	labels, err := os.ReadFile("testdata/evidence_labels.txt")
	require.NoError(t, err)
	var series string
	for _, metric := range []string{"click_count", "conversion_count"} {
		for _, src := range []string{"source-a", "source-b", "source-c"} {
			series += metric + "|source=" + src + "|topic=shopping\tpoints=48\n"
		}
	}
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	type args struct {
		args []string
		env  map[string]string
	}
	type want struct {
		code   int
		stdout string
		// Regexp matched against stderr with `{empty}` for the empty directory
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"events lists every event", args{[]string{"events"}, withDir}, want{0, string(events), `^$`}},
		{
			"events without events.csv fails",
			args{[]string{"events", "--data-dir", "{empty}"}, nil},
			want{1, "", `^nodloop evidence: open {empty}/events.csv: no such file or directory\n$`},
		},
		{
			"event prints the context and the series",
			args{[]string{"event", "--id", "tq-019"}, withDir},
			want{0, "tq-019\tcontext=measurement_context_changed\n" + series, `^$`},
		},
		{
			"event without an id fails",
			args{[]string{"event"}, withDir},
			want{1, "", `^nodloop evidence: --id is required\n\nusage:`},
		},
		{
			"event unknown fails",
			args{[]string{"event", "--id", "nope"}, withDir},
			want{1, "", `^nodloop evidence: event "nope": .*not found\n$`},
		},
		{
			"paragraphs lists every paragraph id",
			args{[]string{"paragraphs"}, withDir},
			want{0, string(paragraphs), `^$`},
		},
		{"labels lists every label", args{[]string{"labels"}, withDir}, want{0, string(labels), `^$`}},
		{
			"labels without a label file print nothing",
			args{[]string{"labels", "--data-dir", "{empty}"}, nil},
			want{0, "", `^$`},
		},
		{
			"missing data dir env fails",
			args{[]string{"events"}, nil},
			want{1, "", `^nodloop evidence: ` + envFileDir + ` is not set: run nodloop setup`},
		},
		{
			"missing data dir fails",
			args{[]string{"events", "--data-dir", "/nonexistent"}, nil},
			want{1, "", `^nodloop evidence: evidence file source: stat /nonexistent: no such file or directory\n$`},
		},
		{
			"data dir flag overrides the env",
			args{[]string{"events", "--data-dir", data}, nil},
			want{0, string(events), `^$`},
		},
		{
			"unknown source fails",
			args{[]string{"events", "--source", "postgres"}, withDir},
			want{1, "", `^nodloop evidence: unknown source: "postgres"\n$`},
		},
		{
			"unknown flag fails",
			args{[]string{"events", "--nope"}, withDir},
			want{1, "", `^flag provided but not defined: -nope\n`},
		},
		{
			"unknown action fails",
			args{[]string{"bogus"}, withDir},
			want{1, "", `^nodloop evidence: unknown action "bogus"\n\nusage:`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := strings.NewReplacer("{empty}", t.TempDir())
			args := make([]string, 0, len(tc.args.args))
			for _, a := range tc.args.args {
				args = append(args, r.Replace(a))
			}
			getenv := func(k string) string { return tc.args.env[k] }
			var stdout, stderr bytes.Buffer

			got := runEvidence(args, getenv, func() time.Time { return at }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, r.Replace(tc.want.stderr), stderr.String())
		})
	}
}
