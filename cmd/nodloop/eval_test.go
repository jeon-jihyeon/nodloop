package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestRunEval(t *testing.T) {
	hold, err := json.Marshal(diagnose.Diagnosis{
		Status: diagnose.StatusHold, Observations: []string{}, Causes: []diagnose.Cause{}, Checks: []diagnose.Check{},
		OpenQuestions: []string{}, HoldReasons: []string{"always hold"},
	})
	require.NoError(t, err)
	regular := filepath.Join(t.TempDir(), "regular")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	data := testkit.DemoDir(t)
	table, err := os.ReadFile("testdata/eval_report.md")
	require.NoError(t, err)
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	const (
		seedLine    = `tq-\d{3}\tseed\thold\tforced=false\t\$0\.0100\t0ms\n`
		holdoutLine = `tq-\d{3}\t(feedback|knowledge):(off|on|all)\thold\tforced=false\t\$0\.0100\t0ms\n`
	)
	type args struct {
		// Commands that must succeed on the same record dir before the one under test
		setup [][]string
		args  []string
	}
	type want struct {
		code   int
		stdout string
		// Regexp matched against stderr
		stderr string
		// Error of reading the report file from the record dir
		report error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"seed reviews every seed event",
			args{nil, []string{"seed", "--session", "t"}},
			want{0, "seed traces 12\n", "^(" + seedLine + "){12}$", os.ErrNotExist},
		},
		{
			"seed runs only the listed events",
			args{nil, []string{"seed", "--session", "t", "--events", "tq-001,tq-002"}},
			want{0, "seed traces 2\n", "^(" + seedLine + "){2}$", os.ErrNotExist},
		},
		{
			"holdout reviews every condition",
			args{nil, []string{"holdout", "--session", "t"}},
			want{0, "holdout traces 48\n", "^(" + holdoutLine + "){48}$", os.ErrNotExist},
		},
		{
			"holdout runs only the listed conditions",
			args{nil, []string{"holdout", "--session", "t", "--events", "tq-003", "--conditions", "feedback:off,knowledge:on"}},
			want{0, "holdout traces 2\n", "^(" + holdoutLine + "){2}$", os.ErrNotExist},
		},
		{
			"holdout of an unknown condition fails",
			args{nil, []string{"holdout", "--session", "t", "--conditions", "bogus"}},
			want{1, "", `^nodloop eval: .*"bogus"\n$`, os.ErrNotExist},
		},
		{
			"report prints the table and writes the file",
			args{[][]string{{"seed", "--session", "t"}, {"holdout", "--session", "t"}}, []string{"report", "--session", "t"}},
			want{0, string(table), `^report .*eval-t\.json\n$`, nil},
		},
		{
			"seed refuses conditions",
			args{nil, []string{"seed", "--session", "t", "--conditions", "feedback:on"}},
			want{1, "", `^nodloop eval: eval: conditions apply to holdout only\n$`, os.ErrNotExist},
		},
		{
			"missing data dir fails",
			args{nil, []string{"seed", "--session", "t", "--data-dir", "/nonexistent"}},
			want{1, "", `^nodloop eval: evidence file source: stat /nonexistent: no such file or directory\n$`, os.ErrNotExist},
		},
		{
			"missing session fails",
			args{nil, []string{"seed"}},
			want{1, "", `^nodloop eval: --session is required\n\nusage:`, os.ErrNotExist},
		},
		{
			"negative parallel fails",
			args{nil, []string{"seed", "--session", "t", "--parallel", "-1"}},
			want{1, "", `^nodloop eval: eval: negative parallel`, os.ErrNotExist},
		},
		{
			"record dir that is a file fails",
			args{nil, []string{"seed", "--session", "t", "--record-dir", regular}},
			want{1, "", `^nodloop eval: record dir: `, os.ErrNotExist},
		},
		{
			"unknown source fails",
			args{nil, []string{"seed", "--session", "t", "--source", "postgres"}},
			want{1, "", `^nodloop eval: unknown source: "postgres"\n$`, os.ErrNotExist},
		},
		{
			"unknown flag fails",
			args{nil, []string{"seed", "--nope"}},
			want{1, "", `^flag provided but not defined: -nope\n`, os.ErrNotExist},
		},
		{
			"unknown action fails",
			args{nil, []string{"bogus", "--session", "t"}},
			want{1, "", `^nodloop eval: unknown action "bogus"\n\nusage:`, os.ErrNotExist},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			getenv := func(k string) string {
				return map[string]string{envFileDir: data, envRecordDir: records}[k]
			}
			now := func() time.Time { return at }
			client := llmmock.NewMockClient(gomock.NewController(t))
			req := gomock.Cond(func(req llm.Request) bool {
				return req.Model == "" && string(req.Schema) == diagnose.Schema && req.System == diagnose.Rules
			})
			client.EXPECT().Complete(gomock.Any(), req).Return(llm.Response{Output: hold, CostUSD: 0.01}, nil).AnyTimes()
			for _, args := range tc.args.setup {
				var stdout, stderr bytes.Buffer
				code := runEval(args, getenv, client, now, &stdout, &stderr)
				require.Equal(t, 0, code, "%s: stderr = %s", strings.Join(args, " "), stderr.String())
			}
			var stdout, stderr bytes.Buffer

			got := runEval(tc.args.args, getenv, client, now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
			_, err := os.Stat(filepath.Join(records, "eval-t.json"))
			assert.ErrorIs(t, err, tc.want.report)
		})
	}
}
