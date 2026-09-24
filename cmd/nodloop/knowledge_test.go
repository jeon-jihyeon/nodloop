package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestRunKnowledge(t *testing.T) {
	files := t.TempDir()
	valid := filepath.Join(files, "valid.jsonl")
	require.NoError(t, os.WriteFile(valid, []byte(`{"id":"k-imp","version":1,"kind":"judgment",`+
		`"content":"check tracking first","scope":{},"evidence":{"paragraph_ids":["p#1"]},"basis":"stated",`+
		`"status":"approved","approver":"demo","author":"demo","time":"2026-09-23T00:00:00Z"}`+"\n"), 0o600))
	invalid := filepath.Join(files, "invalid.jsonl")
	require.NoError(t, os.WriteFile(invalid, []byte(`{"id":"k-imp","version":1,"kind":"meaning","content":"c",`+
		`"basis":"stated",`+
		`"status":"approved","author":"demo"}`+"\n"), 0o600))
	broken := filepath.Join(files, "broken.jsonl")
	require.NoError(t, os.WriteFile(broken, []byte("nope\n"), 0o600))
	regular := filepath.Join(files, "regular")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	data := testkit.DemoDir(t)
	agg := []string{"--kind", "meaning", "--scope-metric", "conversion_count", "--evidence-paragraph", "p#1"}
	var (
		proposeAgg    = append([]string{"propose", "--id", "k-agg", "--content", "time bases differ"}, agg...)
		proposeAgg2   = append([]string{"propose", "--id", "k-agg2", "--content", "a second item"}, agg...)
		proposeAggV2  = append([]string{"propose", "--id", "k-agg", "--content", "time bases differ by an hour"}, agg...)
		approveAgg    = []string{"approve", "k-agg", "--version", "1", "--approver", "reviewer"}
		approveAggV2  = []string{"approve", "k-agg", "--version", "2", "--approver", "reviewer"}
		secondVersion = [][]string{proposeAgg, approveAgg, proposeAggV2, approveAggV2}
		checkTracking = []string{"propose", "--id", "k-t", "--kind", "judgment", "--content", "check tracking"}
	)
	type args struct {
		// Commands that must succeed before the one under test
		// The record dir holds the diagnose trace d1 and the context trace c1
		setup [][]string
		args  []string
	}
	type want struct {
		code int
		// Regexp matched against stdout
		stdout string
		// Regexp matched against stderr
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"propose without evidence fails",
			args{nil, []string{"propose", "--id", "k-new", "--kind", "meaning", "--content", "time bases differ"}},
			want{1, `^$`, `^nodloop knowledge: ` + knowledge.ErrEvidenceRequired.Error()},
		},
		{"propose adds a candidate", args{nil, proposeAgg}, want{0, "^k-agg\tv1\tcandidate\n$", `^$`}},
		{
			"propose without an id generates one",
			args{nil, []string{"propose", "--kind", "meaning", "--content", "time bases differ", "--evidence-paragraph", "p#1"}},
			want{0, "^k-[0-9a-f]+\tv1\tcandidate\n$", `^$`},
		},
		{
			"propose lists overlaps",
			args{[][]string{proposeAgg}, proposeAgg2},
			want{0, "^k-agg2\tv1\tcandidate\noverlaps\tk-agg\tv1\tcandidate\n$", `^$`},
		},
		{
			"propose of a known id adds the next version",
			args{[][]string{proposeAgg}, proposeAggV2},
			want{0, "^k-agg\tv2\tcandidate\n$", `^$`},
		},
		{
			"propose takes the trace as feedback evidence",
			args{nil, append(checkTracking, "--trace", "d1", "--scope-context", "launch", "--exception", "other")},
			want{0, "^k-t\tv1\tcandidate\n$", `^$`},
		},
		{
			"propose with an unknown trace fails",
			args{nil, append(checkTracking, "--trace", "nope")},
			want{1, `^$`, `^nodloop knowledge: trace "nope": ` + trace.ErrNotFound.Error() + `\n$`},
		},
		{
			"propose with a context trace as outcome evidence fails",
			args{nil, append(checkTracking, "--evidence-outcome", "c1")},
			want{1, `^$`, `^nodloop knowledge: .*not a diagnose trace: c1 is a context trace\n$`},
		},
		{
			"approve without an approver fails",
			args{[][]string{proposeAgg}, []string{"approve", "k-agg", "--version", "1"}},
			want{1, `^$`, `^nodloop knowledge: approve: an id, --version and --approver is required\n\nusage:`},
		},
		{
			"approve names the approver",
			args{[][]string{proposeAgg}, approveAgg},
			want{0, "^k-agg\tv1\tapproved\treviewer\n$", `^$`},
		},
		{
			"approve twice fails",
			args{[][]string{proposeAgg, approveAgg}, approveAgg},
			want{1, `^$`, `^nodloop knowledge: .*cannot become`},
		},
		{
			"approve of the second version",
			args{[][]string{proposeAgg, approveAgg, proposeAggV2}, approveAggV2},
			want{0, "^k-agg\tv2\tapproved\treviewer\n$", `^$`},
		},
		{
			"list shows only the current version",
			args{secondVersion, []string{"list"}},
			want{0, "^k-agg\tv2\tapproved\tmeaning\ttime bases differ by an hour\n$", `^$`},
		},
		{
			"list filters by status and kind",
			args{
				[][]string{proposeAgg, approveAgg, proposeAgg2},
				[]string{"list", "--status", "candidate", "--kind", "meaning"},
			},
			want{0, "^k-agg2\tv1\tcandidate\tmeaning\ta second item\n$", `^$`},
		},
		{
			"show keeps the superseded first version",
			args{secondVersion, []string{"show", "k-agg"}},
			want{0, `"status": "superseded"`, `^$`},
		},
		{
			"show links the second version to the first",
			args{secondVersion, []string{"show", "k-agg"}},
			want{0, `"supersedes": 1`, `^$`},
		},
		{
			"show of an unknown id fails",
			args{nil, []string{"show", "nope"}},
			want{1, `^$`, `^nodloop knowledge: .*not found`},
		},
		{
			"show without an id fails",
			args{nil, []string{"show"}},
			want{1, `^$`, `^nodloop knowledge: an id is required\n\nusage:`},
		},
		{
			"overlaps lists items of the same scope",
			args{[][]string{proposeAgg, proposeAgg2}, []string{"overlaps", "k-agg"}},
			want{0, "^k-agg2\tv1\tcandidate\ta second item\n$", `^$`},
		},
		{
			"overlaps of an unknown id fails",
			args{nil, []string{"overlaps", "nope"}},
			want{1, `^$`, `^nodloop knowledge: .*not found`},
		},
		{
			"overlaps without an id fails",
			args{nil, []string{"overlaps"}},
			want{1, `^$`, `^nodloop knowledge: an id is required\n\nusage:`},
		},
		{
			"retire marks the item retired",
			args{[][]string{proposeAgg2}, []string{"retire", "k-agg2", "--version", "1", "--approver", "reviewer"}},
			want{0, "^k-agg2\tv1\tretired\treviewer\n$", `^$`},
		},
		{
			"retire without a version fails",
			args{nil, []string{"retire", "k-agg2", "--approver", "reviewer"}},
			want{1, `^$`, `^nodloop knowledge: retire: an id, --version and --approver is required\n\nusage:`},
		},
		{
			"retire of an unknown id fails",
			args{nil, []string{"retire", "nope", "--version", "1", "--approver", "reviewer"}},
			want{1, `^$`, `^nodloop knowledge: .*not found`},
		},
		{
			"import appends the records of a file",
			args{nil, []string{"import", "--file", valid}},
			want{0, "^imported 1\n$", `^$`},
		},
		{
			"import without a file fails",
			args{nil, []string{"import"}},
			want{1, `^$`, `^nodloop knowledge: --file is required\n\nusage:`},
		},
		{
			"import of a missing file fails",
			args{nil, []string{"import", "--file", filepath.Join(files, "missing.jsonl")}},
			want{1, `^$`, `^nodloop knowledge: stat .*missing.jsonl: no such file or directory\n$`},
		},
		{
			"import of a broken file fails",
			args{nil, []string{"import", "--file", broken}},
			want{1, `^$`, `^nodloop knowledge: broken.jsonl line 1`},
		},
		{
			"import of an invalid record fails",
			args{nil, []string{"import", "--file", invalid}},
			want{1, `^$`, `^nodloop knowledge: .*invalid.jsonl: record 1`},
		},
		{
			"record dir that is a file fails",
			args{nil, []string{"list", "--record-dir", regular}},
			want{1, `^$`, `^nodloop knowledge: record dir: `},
		},
		{
			"unknown source fails",
			args{nil, []string{"list", "--source", "postgres"}},
			want{1, `^$`, `^nodloop knowledge: unknown source: "postgres"\n$`},
		},
		{
			"unknown flag fails",
			args{nil, []string{"list", "--nope"}},
			want{1, `^$`, `^flag provided but not defined: -nope\n`},
		},
		{
			"unknown action fails",
			args{nil, []string{"bogus"}},
			want{1, `^$`, `^nodloop knowledge: unknown action "bogus"\n\nusage:`},
		},
	}
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			traces, err := tracefile.New(records)
			require.NoError(t, err)
			require.NoError(t, traces.Append(ctx, trace.Trace{ID: "d1", Name: trace.NameDiagnose, Time: at}))
			require.NoError(t, traces.Append(ctx, trace.Trace{ID: "c1", Name: trace.NameContext, Time: at}))
			getenv := func(k string) string {
				return map[string]string{envFileDir: data, envRecordDir: records}[k]
			}
			now := testkit.Open(t).Clock.Now
			var stderr bytes.Buffer
			for _, args := range tc.args.setup {
				code := runKnowledge(args, getenv, now, io.Discard, &stderr)
				require.Equal(t, 0, code, "%s: stderr = %s", strings.Join(args, " "), stderr.String())
			}
			var stdout bytes.Buffer

			got := runKnowledge(tc.args.args, getenv, now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}
