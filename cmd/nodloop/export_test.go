package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/otel"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

var exportAt = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

// One span a collector received
type gotSpan struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	Parent  string `json:"parentSpanId"`
	Name    string `json:"name"`
	Start   string `json:"startTimeUnixNano"`
	End     string `json:"endTimeUnixNano"`
	Links   []struct {
		TraceID string `json:"traceId"`
		SpanID  string `json:"spanId"`
	} `json:"links"`
}

// One log record a collector received
type gotLog struct {
	EventName string `json:"eventName"`
	TraceID   string `json:"traceId"`
	SpanID    string `json:"spanId"`
}

// The requests of a collector that reads OTLP JSON
type otlpCollector struct {
	mu     sync.Mutex
	status int
	paths  []string
	types  []string
	auths  []string
	spans  []gotSpan
	logs   []gotLog
}

func (c *otlpCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []gotSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []gotLog `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, r.URL.Path)
	c.types = append(c.types, r.Header.Get("Content-Type"))
	c.auths = append(c.auths, r.Header.Get("Authorization"))
	for _, rs := range body.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			c.spans = append(c.spans, ss.Spans...)
		}
	}
	for _, rl := range body.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			c.logs = append(c.logs, sl.LogRecords...)
		}
	}
	w.WriteHeader(cmp.Or(c.status, http.StatusOK))
}

func (c *otlpCollector) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, s := range c.spans {
		out = append(out, s.Name)
	}
	return out
}

// Writes config.json of the home with the otel key and nothing when it is empty
func exportConfig(t *testing.T, home, saved string) {
	t.Helper()
	if saved != "" {
		hookSaveConfig(t, home, `{"otel":`+saved+`}`)
	}
}

// Records with one run of bot at 09:00, its reject at 09:01 and an outcome at 09:02
func exportFixture(t *testing.T) string {
	t.Helper()
	records := t.TempDir()
	a := app{cfg: config{recordDir: records}, now: func() time.Time { return exportAt }}
	traces, err := a.traces()
	require.NoError(t, err)
	run, err := trace.NewRun("bot", "", nil, nil, []byte("steps"), exportAt)
	require.NoError(t, err)
	require.NoError(t, traces.Append(t.Context(), run.Record()))
	verdicts, err := a.feedback()
	require.NoError(t, err)
	fb, err := feedback.New(run.ID, feedback.VerdictReject, feedback.ReasonScope, "no window", nil, "ann", exportAt.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, verdicts.Append(t.Context(), fb.Record()))
	outcomes, err := a.outcomes()
	require.NoError(t, err)
	o, err := feedback.NewOutcome(run.ID, feedback.ResultConfirmed, "a slow consumer", "", "ann", exportAt.Add(2*time.Minute))
	require.NoError(t, err)
	require.NoError(t, outcomes.Append(t.Context(), o.Record()))
	return records
}

// export otel resolves its endpoint, headers and protocol from the flags, then config.json, then the OTLP environment
func TestRunExportOtelTarget(t *testing.T) {
	type args struct {
		// {srv} stands for the URL of the collector
		args []string
		// The otel key of config.json
		saved string
		env   map[string]string
	}
	type want struct {
		code   int
		stdout string
		stderr string
		paths  []string
		types  []string
		auths  []string
	}
	sent := "exported 1 runs, 1 verdicts, 1 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n"
	proto, jsonType := "application/x-protobuf", "application/json"
	both := []string{"/c/v1/traces", "/c/v1/logs"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the flag names a traces URL and sends traces only in protobuf", args{[]string{"otel", "--endpoint", "{srv}/v1/traces", "--header", "Authorization=Basic flag"}, "", nil},
			want{0, sent, "", []string{"/v1/traces"}, []string{proto}, []string{"Basic flag"}}},
		{"the config sends traces and logs with the headers of its variable", args{[]string{"otel"}, `{"endpoint":"{srv}/c/","headers_env":"ACME_OTLP"}`,
			map[string]string{"ACME_OTLP": "Authorization=Basic%20cfg"}}, want{0, sent, "", both, []string{proto, proto}, []string{"Basic cfg", "Basic cfg"}}},
		{"the config without logs sends traces only", args{[]string{"otel"}, `{"endpoint":"{srv}/c","no_logs":true,"protocol":"http/json"}`, nil},
			want{0, sent, "", []string{"/c/v1/traces"}, []string{jsonType}, []string{""}}},
		{"the config wins over the environment and its headers stay home", args{[]string{"otel"}, `{"endpoint":"{srv}/c"}`,
			map[string]string{envOTLPEndpoint: "{srv}/e", envOTLPHeaders: "Authorization=Basic%20env"}}, want{0, sent, "", both, []string{proto, proto}, []string{"", ""}}},
		{"the environment alone names the endpoint, headers and protocol", args{[]string{"otel"}, "",
			map[string]string{envOTLPEndpoint: "{srv}/e/", envOTLPHeaders: "Authorization=Basic%20env", envOTLPProtocol: "http/json"}},
			want{0, sent, "", []string{"/e/v1/traces", "/e/v1/logs"}, []string{jsonType, jsonType}, []string{"Basic env", "Basic env"}}},
		{"the logs variable of the environment names the logs endpoint", args{[]string{"otel"}, "",
			map[string]string{envOTLPTracesEndpoint: "{srv}/t", envOTLPLogsEndpoint: "{srv}/l"}},
			want{0, sent, "", []string{"/t", "/l"}, []string{proto, proto}, []string{"", ""}}},
		{"the protocol flag wins over the config", args{[]string{"otel", "--protocol", "http/json"}, `{"endpoint":"{srv}/c","no_logs":true}`, nil},
			want{0, sent, "", []string{"/c/v1/traces"}, []string{jsonType}, []string{""}}},
		{"a header flag wins over the same name of the variable", args{[]string{"otel", "--header", "Authorization=Basic flag"}, `{"endpoint":"{srv}/c","no_logs":true,"headers_env":"ACME_OTLP"}`,
			map[string]string{"ACME_OTLP": "Authorization=Basic%20cfg"}}, want{0, sent, "", []string{"/c/v1/traces"}, []string{proto}, []string{"Basic flag"}}},
		{"grpc of the environment fails", args{[]string{"otel"}, "", map[string]string{envOTLPEndpoint: "{srv}", envOTLPProtocol: "grpc"}},
			want{1, "", `unknown otel protocol "grpc"`, nil, nil, nil}},
		{"a pair without = in the variable fails without its value", args{[]string{"otel"}, `{"endpoint":"{srv}","headers_env":"ACME_OTLP"}`, map[string]string{"ACME_OTLP": "Authorization"}},
			want{1, "", "bad --header: ACME_OTLP holds a pair that is not name=value", nil, nil, nil}},
		{"a pair without a name in the variable fails without its value", args{[]string{"otel"}, `{"endpoint":"{srv}","headers_env":"ACME_OTLP"}`,
			map[string]string{"ACME_OTLP": "=Basic%20cfg"}}, want{1, "", "bad --header: ACME_OTLP holds a pair that is not name=value", nil, nil, nil}},
		{"a value the environment does not URL encode fails without its value", args{[]string{"otel"}, "",
			map[string]string{envOTLPEndpoint: "{srv}", envOTLPHeaders: "Authorization=Basic%zz"}},
			want{1, "", "bad --header: OTEL_EXPORTER_OTLP_HEADERS holds a value that is not URL encoded\n", nil, nil, nil}},
		{"a config that does not decode fails", args{[]string{"otel"}, "{broken", nil}, want{1, "", "config.json is not valid JSON", nil, nil, nil}},
		{"since after every record sends no request", args{[]string{"otel", "--endpoint", "{srv}", "--since", "2026-10-08T00:00:00Z"}, "", nil},
			want{0, "exported 0 runs, 0 verdicts, 0 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n", "", nil, nil, nil}},
		{"since and pending together fail", args{[]string{"otel", "--endpoint", "{srv}", "--since", "2026-10-08T00:00:00Z", "--pending"}, "", nil},
			want{1, "", errExportFlags.Error(), nil, nil, nil}},
		{"a since that is no RFC3339 time fails", args{[]string{"otel", "--endpoint", "{srv}", "--since", "yesterday"}, "", nil},
			want{1, "", `parsing time "yesterday"`, nil, nil, nil}},
		{"no endpoint anywhere fails naming otel set", args{[]string{"otel"}, "", nil}, want{1, "", "--endpoint is required, or save one with nodloop otel set", nil, nil, nil}},
		{"a header flag without = fails", args{[]string{"otel", "--endpoint", "{srv}", "--header", "Authorization"}, "", nil},
			want{1, "", `bad --header: "Authorization" is not Name=value`, nil, nil, nil}},
		{"an unknown flag fails", args{[]string{"otel", "--bogus"}, "", nil}, want{1, "", "flag provided but not defined: -bogus", nil, nil, nil}},
		{"another target is unknown", args{[]string{"zipkin"}, "", nil}, want{1, "", "unknown action", nil, nil, nil}},
		{"no target fails", args{nil, "", nil}, want{1, "", "an action is required", nil, nil, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &otlpCollector{}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)
			records, home := exportFixture(t), t.TempDir()
			exportConfig(t, home, strings.ReplaceAll(tc.args.saved, "{srv}", srv.URL))
			args := make([]string, 0, len(tc.args.args))
			for _, a := range tc.args.args {
				args = append(args, strings.ReplaceAll(a, "{srv}", srv.URL))
			}
			getenv := func(k string) string {
				base := map[string]string{envRecordDir: records, "HOME": home}[k]
				return strings.ReplaceAll(cmp.Or(base, tc.args.env[k]), "{srv}", srv.URL)
			}
			var stdout, stderr bytes.Buffer

			code := runExport(args, getenv, func() time.Time { return exportAt.Add(time.Hour) }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.NotContains(t, stderr.String(), "Basic")
			c.mu.Lock()
			defer c.mu.Unlock()
			assert.Equal(t, tc.want.paths, c.paths)
			assert.Equal(t, tc.want.types, c.types)
			assert.Equal(t, tc.want.auths, c.auths)
		})
	}
}

// Ids of the records of the fixture of every kind
type exportedIDs struct {
	run, extract, check string
	verdictAt           time.Time
}

// One record of every kind an export sends
// 1. a session run of 90 s from its prompt, its reject and an outcome
// 2. an extraction of 30 s that drafted the candidate lesson, then approved
// 3. a coverage check of compaction c1 through a model
func exportKinds(t *testing.T) (string, exportedIDs) {
	t.Helper()
	ctx := t.Context()
	records := t.TempDir()
	at := exportAt
	a := app{cfg: config{recordDir: records}, now: func() time.Time { return at }}
	traces, err := a.traces()
	require.NoError(t, err)
	run, err := trace.NewRun(sessionProducer, "", trace.Labels{"repo": {"nodloop"}}, json.RawMessage(`{"applied":[{"id":"git-c","version":1}]}`),
		[]byte("done"), at.Add(90*time.Second))
	require.NoError(t, err)
	run.SessionID = "s1"
	run, err = run.Took(90 * time.Second)
	require.NoError(t, err)
	require.NoError(t, traces.Append(ctx, run.Record()))
	ids := exportedIDs{run: run.ID, verdictAt: at.Add(2 * time.Minute)}
	verdicts, err := a.feedback()
	require.NoError(t, err)
	fb, err := feedback.New(run.ID, feedback.VerdictReject, feedback.ReasonFact, "wrong flag", nil, feedback.ReviewerSession, ids.verdictAt)
	require.NoError(t, err)
	require.NoError(t, verdicts.Append(ctx, fb.Record()))
	outcomes, err := a.outcomes()
	require.NoError(t, err)
	o, err := feedback.NewOutcome(run.ID, feedback.ResultConfirmed, "", "checked", "ann", at.Add(3*time.Minute))
	require.NoError(t, err)
	require.NoError(t, outcomes.Append(ctx, o.Record()))
	at = at.Add(5 * time.Minute)
	ledger, err := a.ledger()
	require.NoError(t, err)
	_, _, err = ledger.Propose(ctx, knowledge.Knowledge{
		ID: "lesson", Kind: knowledge.KindJudgment, Content: "Use git -C", Author: "claude",
		Run: &knowledge.RunScope{Producer: sessionProducer}, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{run.ID}},
	})
	require.NoError(t, err)
	extract := trace.Trace{
		ID: trace.NewID(at), Name: trace.NameExtract, Ref: run.ID, Time: at, DurationMS: 30_000, Model: "claude-haiku-4-5",
		Input: json.RawMessage("{}"), Output: json.RawMessage(`{"conclusion":"proposed","candidate":{"id":"lesson","version":1}}`),
		Usage: trace.Usage{InputTokens: 10, OutputTokens: 4, CacheReadTokens: 100, CostUSD: 0.01},
	}
	require.NoError(t, traces.Append(ctx, extract.Record()))
	ids.extract = extract.ID
	at = at.Add(2 * time.Minute)
	require.NoError(t, testkit.Err(ledger.Approve(ctx, "lesson", 1, "ann")))
	at = at.Add(time.Minute)
	check := trace.Trace{
		ID: trace.NewID(at), Name: trace.NameCheck, SessionID: "c1", Time: at, DurationMS: 2000, Model: "claude-haiku-4-5",
		Input: json.RawMessage("{}"), Usage: trace.Usage{InputTokens: 3, OutputTokens: 1},
	}
	require.NoError(t, traces.Append(ctx, check.Record()))
	ids.check = check.ID
	return records, ids
}

// Every record goes as its own span once, nested and linked to what it judged, and every evaluation as a log record of its target
func TestRunExportOtelKinds(t *testing.T) {
	records, ids := exportKinds(t)
	c := &otlpCollector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
	var stdout, stderr bytes.Buffer

	code := runExport([]string{"otel", "--protocol", "http/json", "--endpoint", srv.URL + "/v1/traces"}, getenv, time.Now, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "exported 1 runs, 1 verdicts, 1 outcomes, 2 model calls, 0 questions, 1 decisions and 0 judgments\n", stdout.String())
	runSpan := otel.RunSpan(ids.run)
	drafted := otel.TraceSpan(ids.extract)
	verdict := otel.VerdictSpan(ids.run, ids.verdictAt)
	byName := map[string]gotSpan{}
	for _, s := range c.spans {
		byName[s.Name] = s
	}
	assert.ElementsMatch(t, []string{
		"invoke_agent session", "evaluate nodloop.verdict", "evaluate nodloop.outcome", "evaluate nodloop.decision",
		"invoke_agent nodloop.extract", "chat claude-haiku-4-5", "invoke_agent nodloop.check", "chat claude-haiku-4-5",
	}, c.names())
	turn := byName["invoke_agent session"]
	assert.Equal(t, []string{runSpan.TraceID, runSpan.SpanID}, []string{turn.TraceID, turn.SpanID})
	assert.Equal(t, fmt.Sprint(exportAt.UnixNano()), turn.Start, "the turn starts at its prompt")
	assert.Equal(t, fmt.Sprint(exportAt.Add(90*time.Second).UnixNano()), turn.End)
	linked := func(name string) []string {
		var out []string
		for _, l := range byName[name].Links {
			out = append(out, l.SpanID)
		}
		return out
	}
	assert.Equal(t, []string{runSpan.SpanID}, linked("evaluate nodloop.verdict"))
	assert.Equal(t, []string{runSpan.SpanID}, linked("evaluate nodloop.outcome"))
	assert.Equal(t, []string{drafted.SpanID}, linked("evaluate nodloop.decision"), "a decision links the extraction that drafted it")
	assert.Equal(t, []string{runSpan.SpanID, verdict.SpanID}, linked("invoke_agent nodloop.extract"), "an extraction links its run and the correction")
	assert.Empty(t, linked("invoke_agent nodloop.check"))
	extract := byName["invoke_agent nodloop.extract"]
	assert.Equal(t, fmt.Sprint(exportAt.Add(5*time.Minute-30*time.Second).UnixNano()), extract.Start)
	assert.Equal(t, drafted.SpanID, extract.SpanID)
	assert.Empty(t, c.logs, "the flag sends traces only")
}

// The log records of the evaluations carry the trace and span id of what they evaluate
func TestRunExportOtelLogs(t *testing.T) {
	records, ids := exportKinds(t)
	c := &otlpCollector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	getenv := func(k string) string {
		return map[string]string{envRecordDir: records, envOTLPEndpoint: srv.URL, envOTLPProtocol: "http/json"}[k]
	}
	var stderr bytes.Buffer

	code := runExport([]string{"otel"}, getenv, time.Now, &bytes.Buffer{}, &stderr)

	require.Equal(t, 0, code, stderr.String())
	runSpan := otel.RunSpan(ids.run)
	drafted := otel.TraceSpan(ids.extract)
	assert.ElementsMatch(t, []gotLog{
		{"gen_ai.evaluation.result", runSpan.TraceID, runSpan.SpanID},
		{"gen_ai.evaluation.result", runSpan.TraceID, runSpan.SpanID},
		{"gen_ai.evaluation.result", drafted.TraceID, drafted.SpanID},
	}, c.logs)
}

// --pending sends each record once across exports, moves the mark to the newest record sent and leaves it on a failure
func TestRunExportOtelPending(t *testing.T) {
	type args struct {
		// export.json before the export
		mark   string
		status int
		// Verdicts recorded at 11:00 on the run
		verdicts []feedback.Verdict
	}
	type want struct {
		code   int
		stdout string
		names  []string
		// The time export.json keeps after the export
		at string
		// Whether export.json names the position after the last trace the export read
		cursor bool
	}
	none := "exported 0 runs, 0 verdicts, 0 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n"
	verdict := "exported 0 runs, 1 verdicts, 0 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n"
	hourOn := `{"at":"2026-10-07T10:00:00Z"}`
	approve := []feedback.Verdict{feedback.VerdictApprove}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no mark sends every record and marks the newest", args{"", http.StatusOK, nil}, want{0,
			"exported 1 runs, 1 verdicts, 1 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n",
			[]string{"invoke_agent bot", "evaluate nodloop.verdict", "evaluate nodloop.outcome"}, "2026-10-07T09:02:00Z", true}},
		{"a mark that does not decode reads as none", args{"{", http.StatusOK, nil}, want{0,
			"exported 1 runs, 1 verdicts, 1 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n",
			[]string{"invoke_agent bot", "evaluate nodloop.verdict", "evaluate nodloop.outcome"}, "2026-10-07T09:02:00Z", true}},
		{"a mark of the time alone after every record sends nothing and keeps its time", args{hourOn, http.StatusOK, nil},
			want{0, none, nil, "2026-10-07T10:00:00Z", true}},
		{"a verdict after the mark goes alone and never its run again", args{hourOn, http.StatusOK, approve},
			want{0, verdict, []string{"evaluate nodloop.verdict"}, "2026-10-07T11:00:00Z", true}},
		{"a withdraw goes as an evaluation without a score", args{hourOn, http.StatusOK, []feedback.Verdict{feedback.VerdictWithdraw}},
			want{0, verdict, []string{"evaluate nodloop.verdict"}, "2026-10-07T11:00:00Z", true}},
		{"a cursor of a record no store holds falls back to the time of the mark",
			args{`{"at":"2026-10-07T10:00:00Z","after":{"traces":"gone","verdicts":"gone","outcomes":"gone"}}`, http.StatusOK, approve},
			want{0, verdict, []string{"evaluate nodloop.verdict"}, "2026-10-07T11:00:00Z", true}},
		{"a refused export leaves the mark", args{hourOn, http.StatusInternalServerError, approve},
			want{1, "", []string{"evaluate nodloop.verdict"}, "2026-10-07T10:00:00Z", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &otlpCollector{status: tc.args.status}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)
			records := exportFixture(t)
			if tc.args.mark != "" {
				require.NoError(t, os.WriteFile(filepath.Join(records, otel.MarkFile), []byte(tc.args.mark), 0o600))
			}
			later := exportAt.Add(2 * time.Hour)
			ids, err := tracefileRuns(records)
			require.NoError(t, err)
			end, err := tracefileEnd(records)
			require.NoError(t, err)
			a := app{cfg: config{recordDir: records}, now: func() time.Time { return later }}
			store, err := a.feedback()
			require.NoError(t, err)
			for _, v := range tc.args.verdicts {
				fb, err := feedback.New(ids[0], v, "", "", nil, "ann", later)
				require.NoError(t, err)
				require.NoError(t, store.Append(t.Context(), fb.Record()))
			}
			getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
			var stdout, stderr bytes.Buffer

			code := runExport([]string{"otel", "--pending", "--protocol", "http/json", "--endpoint", srv.URL}, getenv,
				func() time.Time { return later }, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Equal(t, tc.want.names, c.names())
			b, err := os.ReadFile(filepath.Join(records, otel.MarkFile))
			require.NoError(t, err)
			var mark otel.Mark
			require.NoError(t, json.Unmarshal(b, &mark))
			assert.Equal(t, tc.want.at, mark.At.Format(time.RFC3339))
			assert.Equal(t, tc.want.cursor, mark.After[storeTraces] == end)
		})
	}
}

// A record a slower process appends after an export with a time before the mark goes with the next export exactly once
// The fixture holds a run, its reject and an outcome, and the late verdict is stamped before the outcome the first export sent
func TestRunExportOtelPendingLateAppend(t *testing.T) {
	c := &otlpCollector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	records := exportFixture(t)
	ids, err := tracefileRuns(records)
	require.NoError(t, err)
	getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
	export := func() string {
		var stdout, stderr bytes.Buffer
		require.Equal(t, 0, runExport([]string{"otel", "--pending", "--protocol", "http/json", "--endpoint", srv.URL}, getenv,
			func() time.Time { return exportAt.Add(time.Hour) }, &stdout, &stderr), stderr.String())
		return stdout.String()
	}
	first := export()
	verdicts, err := (app{cfg: config{recordDir: records}}).feedback()
	require.NoError(t, err)
	late, err := feedback.New(ids[0], feedback.VerdictApprove, "", "", nil, "ann", exportAt.Add(90*time.Second))
	require.NoError(t, err)
	require.NoError(t, verdicts.Append(t.Context(), late.Record()))

	second, third := export(), export()

	assert.Equal(t, []string{
		"exported 1 runs, 1 verdicts, 1 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n",
		"exported 0 runs, 1 verdicts, 0 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n",
		"exported 0 runs, 0 verdicts, 0 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n",
	}, []string{first, second, third})
	c.mu.Lock()
	defer c.mu.Unlock()
	sent := otel.VerdictSpan(ids[0], late.Time)
	spans := slices.DeleteFunc(slices.Clone(c.spans), func(s gotSpan) bool { return s.SpanID != sent.SpanID })
	assert.Len(t, spans, 1, "the late verdict arrives once")
}

// A prune that removed the trace the mark ends at sends the traces stamped after the mark once and never the ones sent before
func TestRunExportOtelPendingPruned(t *testing.T) {
	c := &otlpCollector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	records := exportFixture(t)
	traces, err := tracefile.New(records)
	require.NoError(t, err)
	appendRun := func(at time.Time) trace.Trace {
		run, err := trace.NewRun("bot", "", nil, nil, []byte("steps"), at)
		require.NoError(t, err)
		require.NoError(t, traces.Append(t.Context(), run.Record()))
		return run
	}
	unjudged := appendRun(exportAt.Add(3 * time.Minute))
	getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
	export := func() string {
		var stdout, stderr bytes.Buffer
		require.Equal(t, 0, runExport([]string{"otel", "--pending", "--protocol", "http/json", "--endpoint", srv.URL}, getenv,
			func() time.Time { return exportAt.Add(time.Hour) }, &stdout, &stderr), stderr.String())
		return stdout.String()
	}
	first := export()
	removed, err := traces.Prune(t.Context(), func(context.Context) ([]string, error) { return []string{unjudged.ID}, nil })
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	later := appendRun(exportAt.Add(2 * time.Hour))

	second := export()

	assert.Equal(t, []string{
		"exported 2 runs, 1 verdicts, 1 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n",
		"exported 1 runs, 0 verdicts, 0 outcomes, 0 model calls, 0 questions, 0 decisions and 0 judgments\n",
	}, []string{first, second})
	c.mu.Lock()
	defer c.mu.Unlock()
	sent := otel.RunSpan(later.ID)
	assert.Len(t, slices.DeleteFunc(slices.Clone(c.spans), func(s gotSpan) bool { return s.SpanID != sent.SpanID }), 1, "the later run arrives once")
}

// A corrupt line before the mark that a lookup reads for a new record fails the export and leaves the mark
// The new record is an extraction of the run so the lookup reads the run and the verdicts of the run
func TestRunExportOtelPendingLookupFails(t *testing.T) {
	tcs := []struct {
		name string
		// The record file the corrupt line goes into before the mark
		args string
	}{
		{"the trace file read for the session of the run", "traces.jsonl"},
		{"the verdict file read for the verdict the extraction followed", "feedback.jsonl"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &otlpCollector{}
			srv := httptest.NewServer(c)
			t.Cleanup(srv.Close)
			records := exportFixture(t)
			ids, err := tracefileRuns(records)
			require.NoError(t, err)
			hookAppend(t, filepath.Join(records, tc.args), "not json\n")
			stores, err := (app{cfg: config{recordDir: records}}).stores()
			require.NoError(t, err)
			ctx := t.Context()
			_, traces, _ := stores.traces.After(ctx, record.TraceFilter{}, "")
			_, verdicts, _ := stores.verdicts.After(ctx, "", 0)
			_, outcomes, _ := stores.outcomes.After(ctx, "", 0)
			mark, err := json.Marshal(otel.Mark{At: exportAt.Add(2 * time.Minute), After: map[string]string{
				storeTraces: traces, storeVerdicts: verdicts, storeOutcomes: outcomes, storeLedger: "",
			}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(records, otel.MarkFile), mark, 0o600))
			extract := trace.Trace{ID: trace.NewID(exportAt.Add(time.Hour)), Name: trace.NameExtract, Ref: ids[0], Time: exportAt.Add(time.Hour), Input: json.RawMessage("{}")}
			require.NoError(t, stores.traces.Append(ctx, extract.Record()))
			getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
			var stdout, stderr bytes.Buffer

			code := runExport([]string{"otel", "--pending", "--protocol", "http/json", "--endpoint", srv.URL}, getenv,
				func() time.Time { return exportAt.Add(2 * time.Hour) }, &stdout, &stderr)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), "has corrupt lines")
			assert.Empty(t, c.names(), "nothing is sent")
			kept, err := os.ReadFile(filepath.Join(records, otel.MarkFile))
			require.NoError(t, err)
			assert.JSONEq(t, string(mark), string(kept))
		})
	}
}

// A decision links the extraction that drafted its version however many extractions came after and none when no extraction did
func TestRunExportOtelDecisionDrafts(t *testing.T) {
	records := t.TempDir()
	stores, err := (app{cfg: config{recordDir: records}}).stores()
	require.NoError(t, err)
	ctx := t.Context()
	var extracts []trace.Trace
	for i := range 9 {
		at := exportAt.Add(time.Duration(i) * time.Minute)
		extracts = append(extracts, trace.Trace{
			ID: trace.NewID(at), Name: trace.NameExtract, Time: at, Input: json.RawMessage("{}"),
			Output: json.RawMessage(fmt.Sprintf(`{"conclusion":"proposed","candidate":{"id":"lesson-%d","version":1}}`, i)),
		})
		require.NoError(t, stores.traces.Append(ctx, extracts[i].Record()))
	}
	decided := exportAt.Add(time.Hour)
	for _, id := range []string{"lesson-0", "manual"} {
		require.NoError(t, stores.items.Append(ctx, record.Knowledge{
			ID: id, Version: 1, Kind: string(knowledge.KindJudgment), Content: "Use git -C", Status: string(knowledge.StatusApproved),
			Approver: "ann", ApprovedAt: decided, Time: decided, Author: "claude",
		}))
	}
	c := &otlpCollector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	getenv := func(k string) string { return map[string]string{envRecordDir: records}[k] }
	var stderr bytes.Buffer
	code := runExport([]string{"otel", "--protocol", "http/json", "--endpoint", srv.URL + "/v1/traces"}, getenv, time.Now, &bytes.Buffer{}, &stderr)
	require.Equal(t, 0, code, stderr.String())
	links := map[string][]string{}
	for _, s := range c.spans {
		for _, l := range s.Links {
			links[s.SpanID] = append(links[s.SpanID], l.SpanID)
		}
	}
	tcs := []struct {
		name string
		args string
		want []string
	}{
		{"a version drafted before eight newer extractions links that extraction", "lesson-0", []string{otel.TraceSpan(extracts[0].ID).SpanID}},
		{"a version no extraction drafted links nothing", "manual", nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			span := otel.DecisionSpan(otel.Ref{ID: tc.args, Version: 1}, string(knowledge.StatusApproved), decided)

			assert.Equal(t, tc.want, links[span.SpanID])
		})
	}
}

// An export that fails leaves the mark and its error names neither the token of the endpoint query nor a header value
// 1. a collector that refuses the logs fails it after the traces went out
// 2. a collector that redirects fails it so neither the records nor the headers reach the place it names
// 3. records that do not open or do not read and a mark that does not open fail it before anything is sent
func TestRunExportOtelRefused(t *testing.T) {
	type args struct {
		// {srv} stands for the URL of the collector and {records} for the record dir
		flags []string
		// The status the collector answers per path and 200 for any other
		// The Location of every answer points at another server
		status map[string]int
		// Files of the record dir before the export by name and a directory for a name ending in /
		files map[string]string
	}
	type want struct {
		code   int
		stderr string
		// The paths the collector saw
		paths []string
		// Whether export.json is missing or unreadable after the export
		// A failure after the claim of the mark leaves it empty and one before leaves no file
		markFailed bool
	}
	pending := []string{"otel", "--pending", "--header", "X-Key=k1"}
	since := []string{"otel", "--since", "2026-10-07T00:00:00Z", "--header", "X-Key=k1"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a collector that refuses the logs fails after the traces went out", args{pending, map[string]int{"/v1/logs": http.StatusBadRequest}, nil},
			want{1, "/v1/logs answered 400", []string{"/v1/traces", "/v1/logs"}, false}},
		{
			"a collector that redirects fails without following it",
			args{[]string{"otel", "--pending", "--endpoint", "{srv}/v1/traces?token=t1", "--header", "X-Key=k1"},
				map[string]int{"/v1/traces": http.StatusTemporaryRedirect}, nil},
			want{1, "answered 307", []string{"/v1/traces"}, false},
		},
		{"a broken run line fails before anything is sent", args{pending, nil, map[string]string{"traces.jsonl": "not json\n"}},
			want{1, "nodloop export: ", nil, false}},
		{"a broken run line fails an export since a time", args{since, nil, map[string]string{"traces.jsonl": "not json\n"}},
			want{1, "traces.jsonl has corrupt lines", nil, true}},
		{"a broken verdict line fails before anything is sent", args{pending, nil, map[string]string{"feedback.jsonl": "not json\n"}},
			want{1, "feedback.jsonl has corrupt lines", nil, false}},
		{"a broken outcome line fails before anything is sent", args{pending, nil, map[string]string{"outcomes.jsonl": "not json\n"}},
			want{1, "outcomes.jsonl has corrupt lines", nil, false}},
		{"a broken knowledge line fails before anything is sent", args{pending, nil, map[string]string{"knowledge.jsonl": "not json\n"}},
			want{1, "knowledge.jsonl has corrupt lines", nil, false}},
		{"a mark that does not open fails before anything is sent", args{pending, nil, map[string]string{otel.MarkFile + "/": ""}},
			want{1, "nodloop export: ", nil, true}},
		{"a record dir that is a file fails before the mark", args{[]string{"otel", "--pending", "--record-dir", "{records}/regular"}, nil,
			map[string]string{"regular": ""}}, want{1, "record dir: ", nil, true}},
		{"a run file that cannot open fails before the mark", args{pending, nil, map[string]string{"traces.jsonl.bak/": ""}},
			want{1, "trace file: open failed", nil, true}},
		{"a verdict file that cannot open fails before the mark", args{pending, nil, map[string]string{"feedback.jsonl.bak/": ""}},
			want{1, "feedback file: ", nil, true}},
		{"an outcome file that cannot open fails before the mark", args{pending, nil, map[string]string{"outcomes.jsonl.bak/": ""}},
			want{1, "outcomes.jsonl.bak", nil, true}},
		{"a knowledge file that cannot open fails before the mark", args{pending, nil, map[string]string{"knowledge.jsonl.bak/": ""}},
			want{1, "knowledge file: ", nil, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mu sync.Mutex
			var reached bool
			elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				reached = true
			}))
			t.Cleanup(elsewhere.Close)
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				paths = append(paths, r.URL.Path)
				w.Header().Set("Location", elsewhere.URL+r.URL.Path)
				w.WriteHeader(cmp.Or(tc.args.status[r.URL.Path], http.StatusOK))
			}))
			t.Cleanup(srv.Close)
			records := exportFixture(t)
			getenv := func(k string) string { return map[string]string{envRecordDir: records, envOTLPEndpoint: srv.URL}[k] }
			for name, content := range tc.args.files {
				exportRecordFile(t, records, name, content)
			}
			placed := strings.NewReplacer("{srv}", srv.URL, "{records}", records)
			flags := make([]string, 0, len(tc.args.flags))
			for _, f := range tc.args.flags {
				flags = append(flags, placed.Replace(f))
			}
			var stderr bytes.Buffer

			code := runExport(flags, getenv, func() time.Time { return exportAt.Add(time.Hour) }, &bytes.Buffer{}, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.NotRegexp(t, "t1|k1", stderr.String())
			_, err := os.ReadFile(filepath.Join(records, otel.MarkFile))
			assert.Equal(t, tc.want.markFailed, err != nil)
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.want.paths, paths)
			assert.False(t, reached)
		})
	}
}

// Appends the content to the file of the record dir or makes a directory when the name ends in /
func exportRecordFile(t *testing.T, records, name, content string) {
	t.Helper()
	if strings.HasSuffix(name, "/") {
		require.NoError(t, os.Mkdir(filepath.Join(records, name), 0o755))
		return
	}
	f, err := os.OpenFile(filepath.Join(records, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// The ids of the runs of the records newest first
// The position after the last trace of the record dir
func tracefileEnd(records string) (string, error) {
	store, err := tracefile.New(records)
	if err != nil {
		return "", err
	}
	_, end, err := store.After(context.Background(), record.TraceFilter{}, "")
	return end, err
}

func tracefileRuns(records string) ([]string, error) {
	store, err := tracefile.New(records)
	if err != nil {
		return nil, err
	}
	runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun)})
	ids := make([]string, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	return ids, err
}

// The stop hook starts the pending export after a recorded run only once config.json holds an otel target
func TestRunHookStopStartsTheExport(t *testing.T) {
	type args struct {
		saved string
		env   map[string]string
		flags []string
		stdin string
		// The error of the starter
		err error
	}
	type want struct {
		// The commands started with {records} for the record dir
		starts []string
		stderr string
	}
	answer := `{"session_id":"s1","cwd":"{repo}","last_assistant_message":"done"}`
	export := []string{"export otel --pending --record-dir {records}"}
	saved := `{"endpoint":"http://192.0.2.1:4318"}`
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the saved target starts the export", args{saved, nil, nil, answer, nil}, want{export, ""}},
		{"an OTLP variable alone starts nothing", args{"", map[string]string{envOTLPEndpoint: "http://192.0.2.1:4318"}, nil, answer, nil}, want{nil, ""}},
		{"no target starts nothing", args{"", nil, nil, answer, nil}, want{nil, ""}},
		{"an empty answer records no run and starts nothing", args{saved, nil, nil,
			`{"session_id":"s1","cwd":"{repo}","last_assistant_message":" "}`, nil}, want{nil, ""}},
		{"a host whose stop carries no answer starts nothing", args{saved, nil, []string{"--host", "cursor"},
			`{"conversation_id":"s1","workspace_roots":["{repo}"],"status":"completed"}`, nil}, want{nil, ""}},
		{"a start that fails is printed and the hook passes", args{saved, nil, nil, answer, errExecutableTemporary},
			want{export, "nodloop hook: " + errExecutableTemporary.Error()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			exportConfig(t, home, tc.args.saved)
			getenv := func(k string) string {
				return cmp.Or(map[string]string{"HOME": home, envRecordDir: records}[k], tc.args.env[k])
			}
			var starts []string
			start := besideCurate(func(args []string) error {
				starts = append(starts, strings.ReplaceAll(strings.Join(args, " "), records, "{records}"))
				return tc.args.err
			})
			var stderr bytes.Buffer

			code := runHook(append([]string{"stop"}, tc.args.flags...), getenv, start, func() time.Time { return exportAt },
				strings.NewReader(strings.ReplaceAll(tc.args.stdin, "{repo}", repo)), &bytes.Buffer{}, &stderr)

			assert.Equal(t, 0, code)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, tc.want.starts, starts)
		})
	}
}

// After one otel set a session turn and its verdict reach a collector with no command, each once
// The starter runs the export in process where the hook and the MCP server start a process of their own
func TestExportWithNoCommand(t *testing.T) {
	c := &otlpCollector{}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	home, records, repo := hookHome(t, "use git -C")
	env := map[string]string{"HOME": home, envRecordDir: records}
	getenv := func(k string) string { return env[k] }
	var out bytes.Buffer
	require.Equal(t, 0, runOTel([]string{"set", "--endpoint", srv.URL, "--protocol", "http/json"}, getenv, atTerminal, &out, &out), out.String())
	at := exportAt
	now := func() time.Time { return at }
	out.Reset()
	// The MCP server may call it off the test goroutine so it fails by its error and never through require
	start := besideCurate(func(args []string) error {
		if args[0] != "export" {
			return fmt.Errorf("started %v", args)
		}
		if code := runExport(args[1:], getenv, now, &out, &out); code != 0 {
			return fmt.Errorf("export exited %d: %s", code, out.String())
		}
		return nil
	})
	stdin := `{"session_id":"s1","cwd":"` + repo + `","prompt":"go","last_assistant_message":"done"}`
	var stderr bytes.Buffer
	require.Equal(t, 0, runHook([]string{"prompt"}, getenv, start, now, strings.NewReader(stdin), &bytes.Buffer{}, &stderr))
	at = at.Add(time.Minute)
	require.Equal(t, 0, runHook([]string{"stop"}, getenv, start, now, strings.NewReader(stdin), &bytes.Buffer{}, &stderr))
	require.Empty(t, stderr.String())
	ids, err := tracefileRuns(records)
	require.NoError(t, err)
	at = at.Add(time.Minute)
	open := mcpOpen{getenv: getenv, now: now, session: "mcp-s1", log: &stderr, export: start}.open
	mcpClient := testkit.Connect(t, mcp.NewHost(open, "test", "").ServeTransport)

	feedbackErr := mcpClient.Run(t, "feedback", map[string]any{"trace_id": ids[0], "verdict": "reject", "reason": "too long", "reviewer": "session"})
	at = at.Add(time.Minute)
	startErr := start([]string{"export", "otel", "--pending"})

	assert.NoError(t, feedbackErr)
	assert.NoError(t, startErr)
	assert.Empty(t, stderr.String())
	names := c.names()
	assert.Equal(t, 1, strings.Count(strings.Join(names, "\n"), "evaluate nodloop.verdict"), names)
	assert.Equal(t, 2, strings.Count(strings.Join(names, "\n"), "invoke_agent session"), "the run of the setup and the turn each once")
	c.mu.Lock()
	defer c.mu.Unlock()
	turn := c.spans[slices.IndexFunc(c.spans, func(s gotSpan) bool { return s.SpanID == otel.RunSpan(ids[0]).SpanID })]
	assert.Equal(t, fmt.Sprint(exportAt.UnixNano()), turn.Start, "the turn of the session starts at its prompt")
	verdictLogs := slices.DeleteFunc(slices.Clone(c.logs), func(l gotLog) bool { return l.SpanID != otel.RunSpan(ids[0]).SpanID })
	assert.Len(t, verdictLogs, 1)
}

// The headers of a variable fill what the flags left out, into an empty flag too
func TestHeaderFlagFill(t *testing.T) {
	tcs := []struct {
		name string
		args headerFlag
		want headerFlag
	}{
		{"no flag takes every pair", nil, headerFlag{"Authorization": "Basic cfg", "X-Team": "acme"}},
		{"a flag keeps its value over the variable", headerFlag{"Authorization": "Basic flag"}, headerFlag{"Authorization": "Basic flag", "X-Team": "acme"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.args

			err := got.fill("ACME_OTLP", " Authorization=Basic%20cfg , ,X-Team=acme")

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A run sends the items and the draw of its input and an input that does not decode names no item so the run has no arm
func TestExportRun(t *testing.T) {
	type want struct {
		applied, withheld []otel.Ref
		drawn             bool
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"the items and the draw of the input", `{"applied":[],"withheld":[{"id":"git-c","version":1}],"drawn":true}`,
			want{[]otel.Ref{}, []otel.Ref{{ID: "git-c", Version: 1}}, true}},
		{"an input that does not decode names no item", `["git-c"]`, want{[]otel.Ref{}, []otel.Ref{}, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := exportRun(trace.Trace{ID: "r1", Name: trace.NameRun, Producer: "bot", Time: exportAt, Input: json.RawMessage(tc.args)})

			assert.Equal(t, tc.want, want{got.Applied, got.Withheld, got.Drawn})
		})
	}
}

// A verdict of a name is a person's and one the session inferred a model's, with an edit sent as the text it holds
func TestVerdictEvaluation(t *testing.T) {
	type args struct {
		reviewer string
		edited   string
	}
	type want struct {
		annotator otel.Annotator
		edited    string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a named verdict without an edit", args{"ann", ""}, want{otel.AnnotatorHuman, ""}},
		{"an edit held as a JSON string goes as its text", args{"ann", `"git -C repo status"`}, want{otel.AnnotatorHuman, "git -C repo status"}},
		{"an edit of another JSON value goes as it is", args{"ann", `{"answer":1}`}, want{otel.AnnotatorHuman, `{"answer":1}`}},
		{"a verdict the session inferred is a model's", args{feedback.ReviewerSession, ""}, want{otel.AnnotatorLLM, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := feedback.Feedback{TraceID: "r1", Verdict: feedback.VerdictEdit, Reviewer: tc.args.reviewer, Time: exportAt, Edited: json.RawMessage(tc.args.edited)}

			got := verdictEvaluation(v, "s1")

			assert.Equal(t, tc.want, want{got.Annotator, got.Edited})
		})
	}
}
