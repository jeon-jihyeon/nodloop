package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// Every record report prints as text or as one JSON value from records holding one replay
func TestRunReportRecords(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	type args struct {
		args []string
		// A line that fails to decode is appended to traces.jsonl
		corrupt bool
	}
	type want struct {
		code   int
		stdout string
		stderr string
	}
	totals := `{"totals":{"runs":0,"judged":0,"named":0,"inferred":0,"corrected":0,"waiting":0,"approved":0},"scopes":[],"drafts":[],"items":[]}` + "\n"
	added := `"expected_share":0,"rate":null,"rate_low":null,"rate_high":null,"judged_rate":null,"audit_judged":0,"audit_corrected":0,` +
		`"confirmed":0,"refuted":0,"undrawn":0,"srm_p":null,"difference":null,"difference_low":null,"difference_high":null,"insufficient":false,"warnings":[]`
	arms := `[{"arm":"applied","runs":0,"judged":0,"corrected":0,"same_reason":0,"named":0,"inferred":0,"units":0,` + added + `},` +
		`{"arm":"withheld","runs":0,"judged":0,"corrected":0,"same_reason":0,"named":0,"inferred":0,"units":0,` + added + `}]` + "\n"
	replays := `[{"id":"git-c","version":1,"passed":false,"corrected":1,"missed":0,` +
		`"approved":0,"overreach":0,"time":"2026-10-07T12:00:00Z"}]` + "\n"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"loop as JSON", args{[]string{"loop", "--json"}, false}, want{0, totals, ""}},
		{"extract as JSON", args{[]string{"extract", "--json"}, false}, want{0, "[]\n", ""}},
		{"critic as JSON", args{[]string{"critic", "--json"}, false}, want{0, `{"critics":[],"reactions":[],"capture":{"critic":"capture",` +
			`"runs":0,"agree":0,"disputed":0,"capture_only":0,"conversation_only":0}}` + "\n", ""}},
		{"effect as JSON", args{[]string{"effect", "--json"}, false}, want{0, arms, ""}},
		{"replay as JSON", args{[]string{"replay", "--json"}, false}, want{0, replays, ""}},
		{"replay as text", args{[]string{"replay"}, false}, want{0,
			"replay\tgit-c\tv1\tpartial\tmissed 0 of 1\toverreach 0 of 0\t2026-10-07T12:00:00Z\n", ""}},
		{"a report that cannot read its records fails", args{[]string{"replay", "--json"}, true}, want{1, "", "nodloop report: traces.jsonl has corrupt lines: byte 345: "}},
		{"a text report that cannot read its records fails", args{[]string{"replay"}, true}, want{1, "", "nodloop report: traces.jsonl has corrupt lines: byte 345: "}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			store, err := tracefile.New(records)
			require.NoError(t, err)
			require.NoError(t, store.Append(context.Background(), trace.Trace{
				ID: "replay-1", Name: trace.NameReplay, Subject: "git-c", Time: at,
				Output: json.RawMessage(`{"id":"git-c","version":1,"cases":[{"run":"r","expect":"breaks","breaks":true,"why":"w"}],"missed":0,"overreach":0}`),
			}.Record()))
			hookAppend(t, filepath.Join(records, "traces.jsonl"), map[bool]string{true: "not json\n"}[tc.args.corrupt])
			getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
			var stdout, stderr bytes.Buffer

			code := runReport(tc.args.args, getenv, func() time.Time { return at }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
		})
	}
}

// Every text report opens with the window and report effect keeps the arm in the second column with the producer after the counts
func TestRunReportText(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	type run struct {
		id, input string
	}
	type verdict struct {
		run     string
		verdict feedback.Verdict
		code    feedback.ReasonCode
	}
	type args struct {
		report string
		// prune.json and none when empty
		marker   string
		runs     []run
		verdicts []verdict
		// The output of a classify trace of the critic point on r1
		classify string
		// The outputs of classify traces of the reaction point on r1
		reactions []string
	}
	zero := "window\tall\tapplied runs 0\twithheld runs 0\n" +
		"effect\tapplied\truns 0\tjudged 0\tcorrected 0\tsame reason 0\neffect\twithheld\truns 0\tjudged 0\tcorrected 0\tsame reason 0\n"
	drawn := `"drawn":true,"share":0.5,"scheme":1`
	applied := `{"applied":[{"id":"git-c","version":1}],` + drawn
	withheld := `{"applied":[],"withheld":[{"id":"git-c","version":1}],` + drawn
	marker := `{"at":"2026-10-07T00:00:00Z","cutoff":"2026-09-30T12:00:00Z"}`
	member := `{"members":[{"name":"laya","answers":{"q":{"yes":0.9}}}]}`
	// The capture line of records where neither inferred reader judged
	capture := "critic\tcapture\truns 0\tagree 0\tdisputed 0\tcapture only 0\tconversation only 0\n"
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"no run with items prints both arms at zero without a producer", args{"effect", "", []run{{"r1", `{"applied":[]}`}}, nil, `{}`, nil}, zero},
		{
			"each arm of a producer names it after the counts and the new columns follow",
			args{"effect", "", []run{{"r1", applied + `}`}, {"r2", withheld + `}`}, {"r3", `{"applied":[{"id":"git-c","version":1}]}`}}, nil, `{}`, nil},
			"window\tall\tapplied runs 1\twithheld runs 1\n" +
				"effect\tapplied\truns 1\tjudged 0\tcorrected 0\tsame reason 0\tproducer bot\tnamed 0\tinferred 0\tunits 1\trate -\tjudged rate 0.000\texpected share 0.500" +
				"\taudit judged 0 corrected 0\toutcomes confirmed 0 refuted 0\tundrawn 1\tsrm p 1.000\tdifference -\twarnings insufficient\n" +
				"effect\twithheld\truns 1\tjudged 0\tcorrected 0\tsame reason 0\tproducer bot\tnamed 0\tinferred 0\tunits 1\trate -\tjudged rate 0.000\texpected share 0.500" +
				"\taudit judged 0 corrected 0\toutcomes confirmed 0 refuted 0\tundrawn 1\tsrm p 1.000\tdifference -\twarnings insufficient\n",
		},
		{
			"judged arms under a window print rates and the difference with their intervals and every warning",
			args{"effect", marker, []run{
				{"r1", applied + `,"unit":"u1"}`}, {"r2", applied + `,"unit":"u1"}`},
				{"r3", `{"applied":[],"withheld":[{"id":"git-c","version":1}],"drawn":true,"share":0.2,"scheme":1}`},
			}, []verdict{{"r1", feedback.VerdictReject, feedback.ReasonFact}, {"r2", feedback.VerdictApprove, ""}, {"r3", feedback.VerdictApprove, ""}}, `{}`, nil},
			"window\t2026-09-30T12:00:00Z\tapplied runs 2\twithheld runs 1\n" +
				"effect\tapplied\truns 2\tjudged 2\tcorrected 1\tsame reason 1\tproducer bot\tnamed 2\tinferred 0\tunits 1\trate 0.500 [0.095, 0.905]\tjudged rate 1.000" +
				"\texpected share 0.650\taudit judged 0 corrected 0\toutcomes confirmed 0 refuted 0\tundrawn 0\tsrm p 0.657" +
				"\tdifference 0.500 [-0.391, 0.905]\twarnings insufficient,mixed_shares\n" +
				"effect\twithheld\truns 1\tjudged 1\tcorrected 0\tsame reason 0\tproducer bot\tnamed 1\tinferred 0\tunits 1\trate 0.000 [0.000, 0.793]\tjudged rate 1.000" +
				"\texpected share 0.350\taudit judged 0 corrected 0\toutcomes confirmed 0 refuted 0\tundrawn 0\tsrm p 0.657" +
				"\tdifference 0.500 [-0.391, 0.905]\twarnings insufficient,mixed_shares\n",
		},
		{"critic prints the window of every run first", args{"critic", "", []run{{"r1", `{"applied":[]}`}}, nil, member, nil},
			"window\tall\ncritic\tclassifier laya\tjudged 1\tagree 0\tfalse pass 0\tfalse refuse 0\topen 1\n" + capture},
		{"critic prints the window of the last prune first", args{"critic", marker, []run{{"r1", `{"applied":[]}`}}, nil, member, nil},
			"window\t2026-09-30T12:00:00Z\ncritic\tclassifier laya\tjudged 1\tagree 0\tfalse pass 0\tfalse refuse 0\topen 1\n" + capture},
		{"critic without a critic prints the window and the capture line alone", args{"critic", "", nil, nil, `{}`, nil}, "window\tall\n" + capture},
		{
			"a reaction member follows the critics with its answers against the session verdict and the capture line ends it",
			args{"critic", "", []run{{"r1", `{"applied":[]}`}}, nil, member,
				[]string{`{"members":[{"name":"laya","answers":{"corrects":{"yes":0.1},"approves":{"yes":0.1}}}]}`}},
			"window\tall\ncritic\tclassifier laya\tjudged 1\tagree 0\tfalse pass 0\tfalse refuse 0\topen 1\n" +
				"critic\treaction laya\tasked 1\tsure 0\tagree 0\tdisagree 0\tmissed correction 0\tmissed approval 0\tdeferred 0\n" + capture,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(records, "prune.json"), []byte(tc.args.marker), 0o600))
			store, err := tracefile.New(records)
			require.NoError(t, err)
			verdicts, err := feedbackfile.New(records)
			require.NoError(t, err)
			for i, r := range tc.args.runs {
				stamp := at.Add(time.Duration(i) * time.Second)
				require.NoError(t, store.Append(context.Background(), trace.Trace{
					ID: r.id, Name: trace.NameRun, Producer: "bot", Time: stamp, Input: json.RawMessage(r.input), Output: json.RawMessage(`"x"`),
				}.Record()))
			}
			for _, v := range tc.args.verdicts {
				fb, err := feedback.New(v.run, v.verdict, v.code, "why", nil, "ann", at)
				require.NoError(t, err)
				require.NoError(t, verdicts.Append(context.Background(), fb.Record()))
			}
			require.NoError(t, store.Append(context.Background(), trace.Trace{
				ID: "c1", Name: trace.NameClassify, Subject: "critic", Ref: "r1", Time: at, Input: json.RawMessage(`{}`), Output: json.RawMessage(tc.args.classify),
			}.Record()))
			for i, reaction := range tc.args.reactions {
				require.NoError(t, store.Append(context.Background(), trace.Trace{
					ID: fmt.Sprintf("c%d", i+2), Name: trace.NameClassify, Subject: "reaction", Ref: "r1", Time: at, Input: json.RawMessage(`{}`),
					Output: json.RawMessage(reaction),
				}.Record()))
			}
			getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
			var stdout, stderr bytes.Buffer

			code := runReport([]string{tc.args.report}, getenv, func() time.Time { return at }, &stdout, &stderr)

			assert.Equal(t, 0, code, stderr.String())
			assert.Equal(t, tc.want, stdout.String())
		})
	}
}

// A record directory another surface pruned sets the window of the reports without retention in config.json
func TestRunReportReadsTheWindowOfTheLastPrune(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	records := t.TempDir()
	cutoff := at.Add(-7 * 24 * time.Hour)
	require.NoError(t, os.WriteFile(filepath.Join(records, "prune.json"), []byte(`{"at":"2026-10-07T00:00:00Z","cutoff":"2026-09-30T12:00:00Z"}`), 0o600))
	store, err := tracefile.New(records)
	require.NoError(t, err)
	for _, stamp := range []time.Time{cutoff.Add(-time.Hour), cutoff.Add(time.Hour)} {
		run, err := trace.NewRun("bot", "", nil, json.RawMessage(`{"applied":[]}`), []byte("x"), stamp)
		require.NoError(t, err)
		require.NoError(t, store.Append(context.Background(), run.Record()))
	}
	getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
	var stdout, stderr bytes.Buffer

	code := runReport([]string{"loop"}, getenv, func() time.Time { return at }, &stdout, &stderr)

	assert.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "window\t2026-09-30T12:00:00Z\nloop\truns 1\t", stdout.String()[:len("window\t2026-09-30T12:00:00Z\nloop\truns 1\t")])
}
