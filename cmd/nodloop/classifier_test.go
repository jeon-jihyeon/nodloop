package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

const layaURL = "http://localhost:8000/v1/systemone"

// Each row runs its commands in order on one home and checks the last
func TestRunClassifier(t *testing.T) {
	type args struct {
		// Commands run first on the same home
		setup [][]string
		args  []string
	}
	type want struct {
		code   int
		stdout string
		// The first line of stderr
		// The usage text may follow it
		stderr string
	}
	add := []string{"add", "laya", "--url", layaURL}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a fresh home lists the critic as claude alone", args{nil, []string{"list"}},
			want{0, "point\tcritic\tclaude (default)\n", ""}},
		{"an added endpoint is listed with its model and key env",
			args{[][]string{{"add", "laya", "--url", layaURL, "--model", "laya", "--key-env", "LAYA_KEY"}}, []string{"list"}},
			want{0, "classifier\tlaya\t" + layaURL + "\tmodel laya\tkey $LAYA_KEY\npoint\tcritic\tclaude (default)\n", ""}},
		{"two members default to a cascade at 0.8", args{[][]string{add}, []string{"use", "critic", "--members", "laya,claude"}},
			want{0, "point\tcritic\tcascade\tlaya,claude\tthreshold 0.8\n", ""}},
		{"members are trimmed around the commas", args{[][]string{add}, []string{"use", "critic", "--members", "laya, claude"}},
			want{0, "point\tcritic\tcascade\tlaya,claude\tthreshold 0.8\n", ""}},
		{"a parallel setup is listed with its combine",
			args{[][]string{add, {"use", "critic", "--members", "laya,claude", "--mode", "parallel", "--combine", "any"}}, []string{"list"}},
			want{0, "classifier\tlaya\t" + layaURL + "\npoint\tcritic\tparallel\tlaya,claude\tcombine any\n", ""}},
		{"reset leaves claude alone", args{[][]string{add, {"use", "critic", "--members", "laya"}}, []string{"reset", "critic"}},
			want{0, "point\tcritic\tclaude (default)\n", ""}},
		{"a removed endpoint leaves the list", args{[][]string{add}, []string{"remove", "laya"}}, want{0, "", ""}},
		{"claude is reserved", args{nil, []string{"add", "claude", "--url", layaURL}},
			want{1, "", "nodloop classifier: classify: invalid endpoint: the name \"claude\" is reserved"}},
		{"use without members is refused", args{nil, []string{"use", "critic"}},
			want{1, "", "nodloop classifier: use: --members is required"}},
		{"a member never added is refused", args{nil, []string{"use", "critic", "--members", "laya,claude"}},
			want{1, "", "nodloop classifier: classify: unknown classifier: laya. Add it with nodloop classifier add"}},
		{"an unknown point is refused", args{nil, []string{"use", "reaction", "--members", "claude"}},
			want{1, "", "nodloop classifier: classify: unknown decision point: \"reaction\". Use one of [critic]"}},
		{"reset of an unknown point is refused", args{nil, []string{"reset", "reaction"}},
			want{1, "", "nodloop classifier: classify: unknown decision point: \"reaction\". Use one of [critic]"}},
		{"an endpoint in use is not removed", args{[][]string{add, {"use", "critic", "--members", "laya"}}, []string{"remove", "laya"}},
			want{1, "", "nodloop classifier: classify: classifier in use: laya is a member of [critic]. Run nodloop classifier use or reset first"}},
		{"an endpoint never added is not removed", args{nil, []string{"remove", "laya"}},
			want{1, "", "nodloop classifier: classify: unknown classifier: laya"}},
		{"an endpoint never added is not probed", args{nil, []string{"probe", "laya"}},
			want{1, "", "nodloop classifier: classify: unknown classifier: laya"}},
		{"an unknown action is refused", args{nil, []string{"rename", "laya"}},
			want{1, "", "nodloop classifier: unknown action \"rename\""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
			for _, args := range tc.args.setup {
				var stderr bytes.Buffer
				require.Equal(t, 0, runClassifier(args, getenv, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
			}
			var stdout, stderr bytes.Buffer

			code := runClassifier(tc.args.args, getenv, time.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Equal(t, tc.want.stdout, stdout.String())
			line, _, _ := strings.Cut(stderr.String(), "\n")
			assert.Equal(t, tc.want.stderr, line)
		})
	}
}

// The other keys of config.json stay as written
func TestRunClassifierKeepsConfig(t *testing.T) {
	home := homeDir(t.TempDir())
	require.NoError(t, os.MkdirAll(home.dir(), 0o700))
	require.NoError(t, os.WriteFile(home.configPath(), []byte(`{"approver":"ann","record_dir":"/records"}`), 0o600))
	getenv := func(k string) string { return map[string]string{"HOME": string(home)}[k] }

	require.Equal(t, 0, runClassifier([]string{"add", "laya", "--url", layaURL}, getenv, time.Now, &bytes.Buffer{}, &bytes.Buffer{}))

	uc, err := home.readConfig()
	require.NoError(t, err)
	assert.Equal(t, "ann", uc.Approver)
	assert.Equal(t, "/records", uc.RecordDir)
	assert.Equal(t, classify.Endpoints{"laya": {URL: layaURL}}, uc.Classifiers)
}

// A probe prints the answer of the endpoint and sends the key its env holds
func TestRunClassifierProbe(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"answers":{"blue":{"type":"noul","noul":0.97}}}`))
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	getenv := func(k string) string { return map[string]string{"HOME": home, "LAYA_KEY": "secret"}[k] }
	require.Equal(t, 0, runClassifier([]string{"add", "laya", "--url", srv.URL, "--key-env", "LAYA_KEY"}, getenv, time.Now, &bytes.Buffer{}, &bytes.Buffer{}))
	var stdout, stderr bytes.Buffer

	code := runClassifier([]string{"probe", "laya"}, getenv, time.Now, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.True(t, strings.HasPrefix(stdout.String(), "laya\tblue 0.97\t"), stdout.String())
	assert.Equal(t, "Bearer secret", auth)
}

// extract asks the critic setup and records its answers, and asks claude alone with no record when nothing is set up
func TestRunKnowledgeExtractClassifier(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	draft := `{"relation":"add","kind":"judgment","content":"Run git with -C <dir> instead of changing into the directory","keys":["repo"]}`
	passed := `{"states":true,"holds":true,"fits":true,"why":"ok"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"states":{"noul":0.95},"holds":{"noul":0.9},"fits":{"noul":0.92}}}`))
	}))
	t.Cleanup(srv.Close)
	type want struct {
		calls      int
		classified int
	}
	tcs := []struct {
		name  string
		setup [][]string
		want  want
	}{
		{"without a setup claude criticizes and nothing is recorded", nil, want{2, 0}},
		{"a confident first member of a cascade answers alone",
			[][]string{{"add", "laya", "--url", srv.URL}, {"use", "critic", "--members", "laya,claude"}}, want{1, 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			now := func() time.Time { return at }
			for _, args := range tc.setup {
				require.Equal(t, 0, runClassifier(args, getenv, now, &bytes.Buffer{}, &bytes.Buffer{}))
			}
			out := filepath.Join(home, "out.txt")
			require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
			var id bytes.Buffer
			require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, getenv, now, &id, &bytes.Buffer{}))
			run := strings.TrimSpace(id.String())
			require.Equal(t, 0, runFeedback([]string{"add", "--trace", run, "--verdict", "reject", "--reason", "ran cd before git"}, getenv, now, &bytes.Buffer{}, &bytes.Buffer{}))
			client := llmmock.NewMockClient(gomock.NewController(t))
			answers := []string{draft, passed}
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Times(tc.want.calls).DoAndReturn(func(context.Context, llm.Request) (llm.Response, error) {
				next := answers[0]
				answers = answers[1:]
				return llm.Response{Output: json.RawMessage(next)}, nil
			})
			var stdout, stderr bytes.Buffer

			code := runKnowledge([]string{"extract", "--from", run}, getenv, client, now, &stdout, &stderr)

			require.Equal(t, 0, code, stderr.String())
			assert.Contains(t, stdout.String(), "relation\tadd\n")
			traces, err := tracefile.New(records)
			require.NoError(t, err)
			recorded, err := traces.List(context.Background(), trace.Filter{Name: trace.NameClassify})
			require.NoError(t, err)
			assert.Len(t, recorded, tc.want.classified)
		})
	}
}

// A critic setup edited by hand into config.json is checked before any model call
func TestRunKnowledgeExtractInvalidSetup(t *testing.T) {
	home, records := homeDir(t.TempDir()), t.TempDir()
	require.NoError(t, os.MkdirAll(home.dir(), 0o700))
	require.NoError(t, os.WriteFile(home.configPath(), []byte(`{"decisions":{"critic":{"mode":"cascade","members":["claude","laya"]}}}`), 0o600))
	getenv := func(k string) string { return map[string]string{"HOME": string(home), envRecordDir: records}[k] }
	out := filepath.Join(string(home), "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, getenv, time.Now, &id, &bytes.Buffer{}))
	client := llmmock.NewMockClient(gomock.NewController(t))
	var stderr bytes.Buffer

	code := runKnowledge([]string{"extract", "--from", strings.TrimSpace(id.String())}, getenv, client, time.Now, &bytes.Buffer{}, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "classify: invalid setup: a cascade threshold lies above 0 and up to 1, got 0: decisions.critic in config.json")
}
