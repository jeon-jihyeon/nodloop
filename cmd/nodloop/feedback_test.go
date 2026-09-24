package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestRunFeedback(t *testing.T) {
	dir := t.TempDir()
	store, err := feedbackfile.New(dir)
	require.NoError(t, err)
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	rejected := feedback.Feedback{
		TraceID: "t1", Time: base, Verdict: feedback.VerdictReject, Reason: "wrong cause", Reviewer: "author",
	}
	require.NoError(t, store.Append(ctx, rejected))
	approved := feedback.Feedback{
		TraceID: "t2", Time: base.Add(time.Second), Verdict: feedback.VerdictApprove, Reviewer: "session",
	}
	require.NoError(t, store.Append(ctx, approved))
	edited := filepath.Join(t.TempDir(), "edited.json")
	require.NoError(t, os.WriteFile(edited, []byte(`{"status":"hold"}`), 0o600))
	regular := filepath.Join(t.TempDir(), "regular")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	data := testkit.DemoDir(t)
	invalid := filepath.Join(t.TempDir(), "invalid.json")
	require.NoError(t, os.WriteFile(invalid, []byte("not json"), 0o600))
	const (
		older = "t1\t2026-09-22T12:00:00Z\treject\tauthor\twrong cause\n"
		newer = "t2\t2026-09-22T12:00:01Z\tapprove\tsession\t\n"
	)
	type args struct {
		args []string
		// `{filled}` holds two records
		// `{traced}` is a fresh record dir with the diagnose trace d1 and the context trace c1
		records string
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
		{"list prints newest first", args{[]string{"list"}, "{filled}"}, want{0, newer + older, `^$`}},
		{"list by trace keeps its records", args{[]string{"list", "--trace", "t1"}, "{filled}"}, want{0, older, `^$`}},
		{
			"list by verdict keeps its records",
			args{[]string{"list", "--verdict", "reject"}, "{filled}"},
			want{0, older, `^$`},
		},
		{
			"list by reviewer keeps its records",
			args{[]string{"list", "--reviewer", "session"}, "{filled}"},
			want{0, newer, `^$`},
		},
		{"list limit keeps the newest", args{[]string{"list", "--limit", "1"}, "{filled}"}, want{0, newer, `^$`}},
		{
			"list with a record dir that is a file fails",
			args{[]string{"list"}, regular},
			want{1, "", `^nodloop feedback: record dir: `},
		},
		{
			"list without home or record dir fails",
			args{[]string{"list"}, ""},
			want{1, "", `^nodloop feedback: home directory unknown: set --record-dir or ` + envRecordDir + `\n$`},
		},
		{
			"add of an approval defaults the reviewer",
			args{[]string{"add", "--trace", "d1", "--verdict", "approve"}, "{traced}"},
			want{0, "d1\tapprove\tauthor\n", `^$`},
		},
		{
			"add of an edit reads the edited file",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", edited, "--reviewer", "me"}, "{traced}"},
			want{0, "d1\tedit\tme\n", `^$`},
		},
		{
			"add without a verdict fails before reading the edited file",
			args{[]string{"add", "--trace", "d1", "--edited", "/nonexistent.json"}, "{traced}"},
			want{1, "", `^nodloop feedback: add: --trace and a --verdict of approve or edit or reject is required\n\nusage:`},
		},
		{
			"add with a missing edited file fails",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", "/nonexistent.json"}, "{traced}"},
			want{1, "", `^nodloop feedback: --edited: open /nonexistent.json: no such file or directory\n$`},
		},
		{
			"add with an invalid edited file names the reason",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", invalid}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + feedback.ErrEditedInvalid.Error()},
		},
		{
			"add of an edit without the edited file names the reason",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit"}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + feedback.ErrEditedRequired.Error() + `\n$`},
		},
		{
			"add of an approval with an edited file names the reason",
			args{[]string{"add", "--trace", "d1", "--verdict", "approve", "--edited", edited}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + feedback.ErrEditedUnexpected.Error() + `: approve\n$`},
		},
		{
			"add of an unknown verdict fails",
			args{[]string{"add", "--trace", "d1", "--verdict", "maybe"}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + feedback.ErrVerdictUnknown.Error()},
		},
		{
			"add for an unknown trace fails",
			args{[]string{"add", "--trace", "nope", "--verdict", "approve"}, "{traced}"},
			want{1, "", `^nodloop feedback: trace "nope": ` + trace.ErrNotFound.Error() + `\n$`},
		},
		{
			"add for a context trace fails",
			args{[]string{"add", "--trace", "c1", "--verdict", "approve"}, "{traced}"},
			want{1, "", `^nodloop feedback: .*not a diagnose trace: c1 is a context trace\n$`},
		},
		{
			"add with a record dir that is a file fails",
			args{[]string{"add", "--trace", "d1", "--verdict", "approve"}, regular},
			want{1, "", `^nodloop feedback: record dir: `},
		},
		{
			"outcome records a confirmed cause",
			args{[]string{"outcome", "--trace", "d1", "--result", "confirmed", "--cause", "tracking change"}, "{traced}"},
			want{0, "d1\tconfirmed\tauthor\n", `^$`},
		},
		{
			"outcome without a result fails",
			args{[]string{"outcome", "--trace", "d1"}, "{traced}"},
			want{1, "", `^nodloop feedback: outcome: --trace and a --result of confirmed, .* is required\n\nusage:`},
		},
		{
			"outcome of an unknown result names the reason",
			args{[]string{"outcome", "--trace", "d1", "--result", "maybe"}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + feedback.ErrResultUnknown.Error() + `: "maybe"\n$`},
		},
		{
			"outcome of a refutation with a cause names the reason",
			args{[]string{"outcome", "--trace", "d1", "--result", "refuted", "--cause", "x"}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + feedback.ErrCauseUnexpected.Error() + `: refuted\n$`},
		},
		{
			"outcome for an unknown trace fails",
			args{[]string{"outcome", "--trace", "nope", "--result", "confirmed"}, "{traced}"},
			want{1, "", `^nodloop feedback: trace "nope": ` + trace.ErrNotFound.Error() + `\n$`},
		},
		{
			"outcome for a context trace fails",
			args{[]string{"outcome", "--trace", "c1", "--result", "confirmed"}, "{traced}"},
			want{1, "", `^nodloop feedback: .*not a diagnose trace: c1 is a context trace\n$`},
		},
		{
			"outcome with a record dir that is a file fails",
			args{[]string{"outcome", "--trace", "d1", "--result", "confirmed"}, regular},
			want{1, "", `^nodloop feedback: record dir: `},
		},
		{
			"unknown source fails",
			args{[]string{"list", "--source", "postgres"}, "{filled}"},
			want{1, "", `^nodloop feedback: unknown source: "postgres"\n$`},
		},
		{
			"unknown flag fails",
			args{[]string{"list", "--nope"}, "{filled}"},
			want{1, "", `^flag provided but not defined: -nope\n`},
		},
		{
			"unknown action fails",
			args{[]string{"bogus"}, "{filled}"},
			want{1, "", `^nodloop feedback: unknown action "bogus"\n\nusage:`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			traced := t.TempDir()
			traces, err := tracefile.New(traced)
			require.NoError(t, err)
			require.NoError(t, traces.Append(ctx, trace.Trace{ID: "d1", Name: trace.NameDiagnose, Time: base}))
			require.NoError(t, traces.Append(ctx, trace.Trace{ID: "c1", Name: trace.NameContext, Time: base}))
			records := strings.NewReplacer("{filled}", dir, "{traced}", traced).Replace(tc.args.records)
			getenv := func(k string) string {
				return map[string]string{envFileDir: data, envRecordDir: records}[k]
			}
			var stdout, stderr bytes.Buffer

			got := runFeedback(tc.args.args, getenv, func() time.Time { return base }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}
