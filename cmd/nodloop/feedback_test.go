package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	evidencefile "github.com/jeon-jihyeon/nodloop/internal/evidence/file"
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
	// The demo data set with a runbook written after the reviews
	data := t.TempDir()
	require.NoError(t, os.CopyFS(data, os.DirFS(testkit.DemoDir(t))))
	require.NoError(t, os.WriteFile(filepath.Join(data, "procedures", "landing-page-check.md"), []byte(
		"# Landing page check\n\nUse this after a landing page release.\n\n## Load the landing page\n\nOpen the page.\n\n"+
			"## Broken landing page\n\nA broken page drops conversions while clicks hold.\n\n## Decide\n\nReturn the review.\n"), 0o600))
	runbook := filepath.Join(t.TempDir(), "runbook.json")
	require.NoError(t, os.WriteFile(runbook, []byte(`{"status":"ready_for_review","causes":[{"summary":"broken page",`+
		`"paragraph_ids":["landing-page-check#Landing page check/Broken landing page#1"]}]}`), 0o600))
	firstStep := filepath.Join(t.TempDir(), "first.json")
	require.NoError(t, os.WriteFile(firstStep, []byte(`{"status":"ready_for_review","causes":[{"summary":"broken page",`+
		`"paragraph_ids":["landing-page-check#Landing page check/Load the landing page#1"]}]}`), 0o600))
	overCited := filepath.Join(t.TempDir(), "over.json")
	require.NoError(t, os.WriteFile(overCited, []byte(`{"status":"ready_for_review","causes":[{"summary":"broken page",`+
		`"paragraph_ids":["landing-page-check#Landing page check/Load the landing page#1",`+
		`"landing-page-check#Landing page check/Decide#1","landing-page-check#Landing page check/Broken landing page#1"]}]}`), 0o600))
	invalid := filepath.Join(t.TempDir(), "invalid.json")
	require.NoError(t, os.WriteFile(invalid, []byte("not json"), 0o600))
	// Edited reviews a later review could not follow
	misspelled := filepath.Join(t.TempDir(), "misspelled.json")
	require.NoError(t, os.WriteFile(misspelled, []byte(`{"status":"hold","cause":[]}`), 0o600))
	unknownStatus := filepath.Join(t.TempDir(), "status.json")
	require.NoError(t, os.WriteFile(unknownStatus, []byte(`{"status":"needs-review"}`), 0o600))
	invented := filepath.Join(t.TempDir(), "invented.json")
	require.NoError(t, os.WriteFile(invented, []byte(
		`{"status":"ready_for_review","causes":[{"summary":"bot traffic","paragraph_ids":["made-up#1"]}]}`), 0o600))
	cited := filepath.Join(t.TempDir(), "cited.json")
	require.NoError(t, os.WriteFile(cited, []byte(`{"status":"ready_for_review","causes":[{"summary":"bot traffic",`+
		`"paragraph_ids":["metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"]}]}`), 0o600))
	// Cites the one id the context listed and the procedures no longer hold
	stale := filepath.Join(t.TempDir(), "stale.json")
	require.NoError(t, os.WriteFile(stale, []byte(
		`{"status":"ready_for_review","causes":[{"summary":"bot traffic","paragraph_ids":["p#1"]}]}`), 0o600))
	const (
		older = "t1\t2026-09-22T12:00:00Z\treject\tauthor\twrong cause\n"
		newer = "t2\t2026-09-22T12:00:01Z\tapprove\tsession\t\n"
	)
	type args struct {
		args []string
		// `{filled}` holds two records
		// `{traced}` is a fresh record dir with the context trace c1 of tq-005 that lists p#1 which no procedure holds
		// Then the diagnose trace d1 recorded on c1 and the failed diagnose trace f1
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
			"add of an edit citing a paragraph the procedures list is kept",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", cited}, "{traced}"},
			want{0, "d1\tedit\tauthor\n", `^$`},
		},
		{
			"add of an edit citing a paragraph only the context listed names the id and the procedures that apply",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", stale}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + diagnose.ErrEditInvalid.Error() +
				`: paragraph ids the procedures that apply to event tq-005 do not list now: p#1\. ` +
				`The procedures may have changed since the review so cite the id the paragraph has now or drop the id\. ` +
				`Procedures that apply: data-integrity-hold, landing-page-check, metric-anomaly-investigation, ` +
				`outcome-rate-degradation, segment-concentration-review\n$`},
		},
		{
			"add of an edit with a misspelled key names the key",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", misspelled}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + diagnose.ErrEditInvalid.Error() + `: json: unknown field "cause"\n$`},
		},
		{
			"add of an edit with an unknown status names the status",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", unknownStatus}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + diagnose.ErrEditInvalid.Error() + `: status "needs-review" is not no_action`},
		},
		{
			"add of an edit citing a paragraph no procedure holds names the id",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", invented}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + diagnose.ErrEditInvalid.Error() +
				`: paragraph ids the procedures that apply to event tq-005 do not list now: made-up#1\. `},
		},
		{
			"add of an edit citing a procedure written after the review is kept",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", runbook}, "{traced}"},
			want{0, "d1\tedit\tauthor\n", `^$`},
		},
		{
			"add of an edit whose cause cites only a first step names the hold",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", firstStep}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + diagnose.ErrEditInvalid.Error() +
				`: the gate would hold a review that follows it: no paragraph supports: broken page\. `},
		},
		{
			"add of an edit whose cause cites three ids names the cut whatever id comes last",
			args{[]string{"add", "--trace", "d1", "--verdict", "edit", "--edited", overCited}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + diagnose.ErrEditInvalid.Error() +
				`: causes cite more than 2 paragraph ids: broken page\. A later review keeps only the first 2 in citation order\. `},
		},
		{
			"add for a failed review names the failure",
			args{[]string{"add", "--trace", "f1", "--verdict", "reject"}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + trace.ErrFailedReview.Error() + `: f1 failed: model timed out\n$`},
		},
		{
			"outcome for a failed review names the failure",
			args{[]string{"outcome", "--trace", "f1", "--result", "confirmed"}, "{traced}"},
			want{1, "", `^nodloop feedback: ` + trace.ErrFailedReview.Error() + `: f1 failed: model timed out\n$`},
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
			require.NoError(t, traces.Append(ctx, trace.Trace{
				ID: "c1", Name: trace.NameContext, Subject: "tq-005", Time: base, Input: json.RawMessage(`{"paragraph_ids":["p#1"]}`),
				Output: json.RawMessage(`{}`),
			}))
			require.NoError(t, traces.Append(ctx, trace.Trace{ID: "d1", Name: trace.NameDiagnose, Ref: "c1", Time: base}))
			require.NoError(t, traces.Append(ctx, trace.Trace{
				ID: "f1", Name: trace.NameDiagnose, Ref: "c1", Time: base, Error: "model timed out",
			}))
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

// A verdict and the check of an edit read neither the policy nor the events
// So a data dir whose events or policy cannot be read never stops one
// Procedures that cannot be read list nothing so only an edit that cites a paragraph is refused and names why
func TestRunFeedbackWithoutReadableData(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	edited := filepath.Join(t.TempDir(), "edited.json")
	require.NoError(t, os.WriteFile(edited, []byte(
		`{"status":"ready_for_review","causes":[{"summary":"bot traffic","paragraph_ids":["p#1"]}]}`), 0o600))
	hold := filepath.Join(t.TempDir(), "hold.json")
	require.NoError(t, os.WriteFile(hold, []byte(`{"status":"hold","hold_reasons":["the tracking change is unconfirmed"]}`), 0o600))
	refused := `^nodloop feedback: ` + diagnose.ErrEditInvalid.Error() +
		`: paragraph ids the procedures that apply to event tq-005 do not list now: p#1\. .*Procedures that apply: none\n`
	type args struct {
		// Files of the data dir keyed by name
		data map[string]string
		args []string
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
		{
			"an edit citing a paragraph on an empty data dir is refused naming why",
			args{nil, []string{"--verdict", "edit", "--edited", edited}},
			want{1, "", refused + `.*` + evidencefile.ErrNoProcedures.Error()},
		},
		{"a hold on an empty data dir", args{nil, []string{"--verdict", "edit", "--edited", hold}}, want{0, "d1\tedit\tauthor\n", `^$`}},
		{
			"a hold on a data dir whose policy does not parse",
			args{map[string]string{"policy.yaml": "limits: [", "events.csv": "nope\n"}, []string{"--verdict", "edit", "--edited", hold}},
			want{0, "d1\tedit\tauthor\n", `^$`},
		},
		{
			"an edit citing a paragraph on a data dir whose procedures cannot be read is refused naming why",
			args{map[string]string{"procedures": "not a folder"}, []string{"--verdict", "edit", "--edited", edited}},
			want{1, "", refused + `.*procedures`},
		},
		{
			"a hold on a data dir whose procedures cannot be read",
			args{map[string]string{"procedures": "not a folder"}, []string{"--verdict", "edit", "--edited", hold}},
			want{0, "d1\tedit\tauthor\n", `^$`},
		},
		{
			"a reject on an empty data dir",
			args{nil, []string{"--verdict", "reject", "--reason", "wrong"}}, want{0, "d1\treject\tauthor\n", `^$`},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			traces, err := tracefile.New(records)
			require.NoError(t, err)
			require.NoError(t, traces.Append(ctx, trace.Trace{
				ID: "c1", Name: trace.NameContext, Subject: "tq-005", Time: at, Input: json.RawMessage(`{"paragraph_ids":["p#1"]}`),
				Output: json.RawMessage(`{}`),
			}))
			require.NoError(t, traces.Append(ctx, trace.Trace{ID: "d1", Name: trace.NameDiagnose, Ref: "c1", Time: at}))
			data := t.TempDir()
			for name, content := range tc.args.data {
				require.NoError(t, os.WriteFile(filepath.Join(data, name), []byte(content), 0o600))
			}
			getenv := func(k string) string {
				return map[string]string{envFileDir: data, envRecordDir: records}[k]
			}
			var stdout, stderr bytes.Buffer

			got := runFeedback(append([]string{"add", "--trace", "d1"}, tc.args.args...), getenv,
				func() time.Time { return at }, &stdout, &stderr)

			assert.Equal(t, tc.want, want{code: got, stdout: stdout.String(), stderr: tc.want.stderr})
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}
