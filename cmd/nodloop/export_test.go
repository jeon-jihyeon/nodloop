package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestRunExportOtel(t *testing.T) {
	records := t.TempDir()
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	out := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("steps"), 0o600))
	base := func(k string) string { return map[string]string{envRecordDir: records}[k] }
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", "bot", "--output", out}, base, func() time.Time { return at }, &id, &bytes.Buffer{}))
	run := strings.TrimSpace(id.String())
	require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "no window"}, base, func() time.Time { return at.Add(time.Minute) }, &bytes.Buffer{}, &bytes.Buffer{}))
	// Two outcomes of the run send one evaluation event so the line counts one
	for _, result := range []string{"refuted", "confirmed"} {
		require.Equal(t, 0, runFeedback([]string{"outcome", "--trace", run, "--result", result}, base, func() time.Time { return at.Add(time.Minute) }, &bytes.Buffer{}, &bytes.Buffer{}))
	}
	type args struct {
		// {srv} stands for the URL of the collector of the row
		args []string
		env  map[string]string
	}
	type want struct {
		code   int
		stdout string
		stderr string
		// The paths the collector saw and the Authorization header of each request
		paths []string
		auths []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the flags name the endpoint and the headers",
			args{[]string{"otel", "--endpoint", "{srv}/v1/traces", "--header", "Authorization=Basic flag"}, nil},
			want{0, "exported 1 runs and 1 verdicts and 1 outcomes\n", "", []string{"/v1/traces"}, []string{"Basic flag"}},
		},
		{
			"the OTLP environment fills what the flags leave out",
			args{[]string{"otel"}, map[string]string{envOTLPEndpoint: "{srv}/otel/", envOTLPHeaders: "Authorization=Basic%20env"}},
			want{0, "exported 1 runs and 1 verdicts and 1 outcomes\n", "", []string{"/otel/v1/traces"}, []string{"Basic env"}},
		},
		{
			"a header of the flags wins over the same name in the environment",
			args{[]string{"otel", "--endpoint", "{srv}/v1/traces", "--header", "Authorization=Basic flag"}, map[string]string{envOTLPHeaders: "Authorization=Basic%20env"}},
			want{0, "exported 1 runs and 1 verdicts and 1 outcomes\n", "", []string{"/v1/traces"}, []string{"Basic flag"}},
		},
		{"a pair without = in the environment fails", args{[]string{"otel", "--endpoint", "{srv}"}, map[string]string{envOTLPHeaders: "Authorization"}},
			want{1, "", "is not name=value", nil, nil}},
		{"a value the environment does not URL encode fails", args{[]string{"otel", "--endpoint", "{srv}"}, map[string]string{envOTLPHeaders: "Authorization=%zz"}},
			want{1, "", "bad --header: OTEL_EXPORTER_OTLP_HEADERS: invalid URL escape", nil, nil}},
		{"since after every run sends no request", args{[]string{"otel", "--endpoint", "{srv}", "--since", "2026-10-08T00:00:00Z"}, nil},
			want{0, "exported 0 runs and 0 verdicts and 0 outcomes\n", "", nil, nil}},
		{"a since that is no RFC3339 time fails", args{[]string{"otel", "--endpoint", "{srv}", "--since", "yesterday"}, nil},
			want{1, "", `parsing time "yesterday"`, nil, nil}},
		{"no endpoint fails", args{[]string{"otel"}, nil}, want{1, "", "--endpoint is required", nil, nil}},
		{"another target is unknown", args{[]string{"zipkin"}, nil}, want{1, "", "unknown action", nil, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var paths, auths, bodies []string
			srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				defer mu.Unlock()
				paths, auths, bodies = append(paths, r.URL.Path), append(auths, r.Header.Get("Authorization")), append(bodies, string(body))
			}))
			t.Cleanup(srv.Close)
			args := make([]string, 0, len(tc.args.args))
			for _, a := range tc.args.args {
				args = append(args, strings.ReplaceAll(a, "{srv}", srv.URL))
			}
			getenv := func(k string) string {
				return strings.ReplaceAll(cmp.Or(tc.args.env[k], base(k)), "{srv}", srv.URL)
			}
			var stdout, stderr bytes.Buffer

			code := runExport(args, getenv, func() time.Time { return at.Add(time.Hour) }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.want.paths, paths)
			assert.Equal(t, tc.want.auths, auths)
			for _, body := range bodies {
				assert.True(t, json.Valid([]byte(body)))
			}
		})
	}
}

func TestExported(t *testing.T) {
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	run := trace.Trace{ID: "r1", Name: trace.NameRun, Producer: "bot", Time: at, Input: json.RawMessage(`{"applied":[]}`)}
	with := func(input string) trace.Trace {
		r := run
		r.Input = json.RawMessage(input)
		return r
	}
	reject := feedback.Feedback{TraceID: "r1", Time: at, Verdict: feedback.VerdictReject, ReasonCode: feedback.ReasonScope, Reason: "why", Reviewer: "ann"}
	type args struct {
		run      trace.Trace
		verdicts feedback.Records
		outcomes feedback.Outcomes
	}
	tcs := []struct {
		name string
		args args
		want otel.Run
	}{
		{
			"the verdict of the run goes along and one on another run stays off",
			args{run: run, verdicts: feedback.Records{reject, {TraceID: "other", Time: at, Verdict: feedback.VerdictApprove, Reviewer: "ann"}}},
			otel.Run{
				ID: "r1", Producer: "bot", Time: at, Applied: []otel.Ref{}, Withheld: []otel.Ref{},
				Verdict: &otel.Verdict{Time: at, Verdict: "reject", ReasonCode: "scope", Reason: "why", Reviewer: "ann"},
			},
		},
		{
			"only the newest verdict of a run goes along",
			args{run: run, verdicts: feedback.Records{{TraceID: "r1", Time: at.Add(time.Minute), Verdict: feedback.VerdictApprove, Reviewer: "ann"}, reject}},
			otel.Run{
				ID: "r1", Producer: "bot", Time: at, Applied: []otel.Ref{}, Withheld: []otel.Ref{},
				Verdict: &otel.Verdict{Time: at.Add(time.Minute), Verdict: "approve", Reviewer: "ann"},
			},
		},
		{
			"a withdrawn verdict goes along as none",
			args{run: run, verdicts: feedback.Records{{TraceID: "r1", Time: at.Add(time.Minute), Verdict: feedback.VerdictWithdraw, Reviewer: "ann"}, reject}},
			otel.Run{ID: "r1", Producer: "bot", Time: at, Applied: []otel.Ref{}, Withheld: []otel.Ref{}},
		},
		{
			"the newest outcome of the run goes along",
			args{run: run, outcomes: feedback.Outcomes{
				{TraceID: "r1", Time: at, Result: feedback.ResultRefuted, Reviewer: "ann"},
				{TraceID: "r1", Time: at.Add(time.Hour), Result: feedback.ResultConfirmed, ConfirmedCause: "a slow consumer", Reviewer: "ann"},
				{TraceID: "other", Time: at, Result: feedback.ResultRefuted, Reviewer: "ann"},
			}},
			otel.Run{
				ID: "r1", Producer: "bot", Time: at, Applied: []otel.Ref{}, Withheld: []otel.Ref{},
				Outcome: &otel.Outcome{Time: at.Add(time.Hour), Result: "confirmed", ConfirmedCause: "a slow consumer", Reviewer: "ann"},
			},
		},
		{
			"the input names the items and the draw",
			args{run: with(`{"applied":[],"withheld":[{"id":"git-c","version":2}],"drawn":true}`)},
			otel.Run{ID: "r1", Producer: "bot", Time: at, Applied: []otel.Ref{}, Withheld: []otel.Ref{{ID: "git-c", Version: 2}}, Drawn: true},
		},
		{
			"an input that does not decode names no item",
			args{run: with(`{"applied":[{"id":"git-c","version":2}],"drawn":"yes"}`)},
			otel.Run{ID: "r1", Producer: "bot", Time: at, Applied: []otel.Ref{}, Withheld: []otel.Ref{}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := tc.want
			want.Labels, want.Output = tc.args.run.Labels, tc.args.run.Output
			assert.Equal(t, []otel.Run{want}, exported(trace.Traces{tc.args.run}, tc.args.verdicts, tc.args.outcomes))
		})
	}
}
