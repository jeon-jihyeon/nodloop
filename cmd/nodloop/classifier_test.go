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
	set := []string{"set", "critic", "--url", layaURL}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a fresh home lists every point as claude alone", args{nil, []string{"list"}},
			want{0, "point\tcritic\tclaude (default)\npoint\treaction\tclaude (default)\n", ""}},
		{"a set point is listed with its model and key env",
			args{[][]string{{"set", "critic", "--url", layaURL, "--model", "laya", "--key-env", "LAYA_KEY"}}, []string{"list"}},
			want{0, "point\tcritic\t" + layaURL + "\tmodel laya\tkey $LAYA_KEY\npoint\treaction\tclaude (default)\n", ""}},
		{"set prints the point and its URL", args{nil, set}, want{0, "point\tcritic\t" + layaURL + "\n", ""}},
		{"set again replaces the endpoint", args{[][]string{set}, []string{"set", "critic", "--url", "https://openrouter.ai/api/alpha/decisions"}},
			want{0, "point\tcritic\thttps://openrouter.ai/api/alpha/decisions\n", ""}},
		{"unset leaves claude alone", args{[][]string{set}, []string{"unset", "critic"}},
			want{0, "point\tcritic\tclaude (default)\n", ""}},
		{"set without a URL is refused", args{nil, []string{"set", "critic"}},
			want{1, "", "nodloop classifier: classify: invalid endpoint: \"\" is not an absolute http or https URL"}},
		{"an unknown point is refused", args{nil, []string{"set", "review", "--url", layaURL}},
			want{1, "", "nodloop classifier: classify: unknown decision point: \"review\". Use one of [critic reaction]"}},
		{"unset of an unknown point is refused", args{nil, []string{"unset", "review"}},
			want{1, "", "nodloop classifier: classify: unknown decision point: \"review\". Use one of [critic reaction]"}},
		{"a point never set is not probed", args{nil, []string{"probe", "critic"}},
			want{1, "", "nodloop classifier: classify: no endpoint set for the point: critic"}},
		{"an unknown action is refused", args{nil, []string{"use", "critic"}},
			want{1, "", "nodloop classifier: unknown action \"use\""}},
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

// A config written before 0.7.0 reads as the endpoint each point asked first
// set rewrites it in the new shape and drops its decisions
func TestRunClassifierLegacyConfig(t *testing.T) {
	type want struct {
		code   int
		stdout string
		stderr string
	}
	const named = `"classifiers":{"laya":{"url":"` + layaURL + `"},"spare":{"url":"http://spare"}},`
	tcs := []struct {
		name   string
		config string
		args   []string
		want   want
	}{
		{"a cascade of an endpoint and claude asks that endpoint",
			`{` + named + `"decisions":{"critic":{"mode":"cascade","members":["laya","claude"],"threshold":0.9}}}`, []string{"list"},
			want{0, "point\tcritic\t" + layaURL + "\npoint\treaction\tclaude (default)\n", ""}},
		{"a single endpoint asks that endpoint",
			`{` + named + `"decisions":{"reaction":{"mode":"single","members":["laya"]}}}`, []string{"list"},
			want{0, "point\tcritic\tclaude (default)\npoint\treaction\t" + layaURL + "\n", ""}},
		{"claude alone has no endpoint",
			`{` + named + `"decisions":{"critic":{"mode":"single","members":["claude"]}}}`, []string{"list"},
			want{0, "point\tcritic\tclaude (default)\npoint\treaction\tclaude (default)\n", ""}},
		{"a parallel setup is refused with how to repair it",
			`{` + named + `"decisions":{"critic":{"mode":"parallel","members":["laya","spare"],"combine":"all"}}}`, []string{"list"},
			want{1, "", "nodloop classifier: classifier setup no longer run: decisions.critic in config.json asks [laya spare]. " +
				"Run nodloop classifier set critic --url <url> to ask one endpoint before claude"}},
		{"unset clears a setup nodloop no longer runs",
			`{` + named + `"decisions":{"critic":{"mode":"parallel","members":["laya","spare"],"combine":"all"}}}`, []string{"unset", "critic"},
			want{0, "point\tcritic\tclaude (default)\n", ""}},
		{"set repairs a setup nodloop no longer runs",
			`{` + named + `"decisions":{"critic":{"mode":"cascade","members":["claude","laya"],"threshold":0.8}}}`, []string{"set", "critic", "--url", layaURL},
			want{0, "point\tcritic\t" + layaURL + "\n", ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := homeDir(t.TempDir())
			require.NoError(t, os.MkdirAll(home.dir(), 0o700))
			require.NoError(t, os.WriteFile(home.configPath(), []byte(tc.config), 0o600))
			getenv := func(k string) string { return map[string]string{"HOME": string(home)}[k] }
			var stdout, stderr bytes.Buffer

			code := runClassifier(tc.args, getenv, time.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			assert.Equal(t, tc.want.stdout, stdout.String())
			line, _, _ := strings.Cut(stderr.String(), "\n")
			assert.Equal(t, tc.want.stderr, line)
		})
	}
}

// set keeps the other keys of config.json and drops the decisions of a config before 0.7.0
func TestRunClassifierKeepsConfig(t *testing.T) {
	home := homeDir(t.TempDir())
	require.NoError(t, os.MkdirAll(home.dir(), 0o700))
	legacy := `{"approver":"ann","record_dir":"/records","classifiers":{"laya":{"url":"` + layaURL + `"}},` +
		`"decisions":{"reaction":{"mode":"single","members":["laya"]}}}`
	require.NoError(t, os.WriteFile(home.configPath(), []byte(legacy), 0o600))
	getenv := func(k string) string { return map[string]string{"HOME": string(home)}[k] }

	require.Equal(t, 0, runClassifier([]string{"set", "critic", "--url", layaURL}, getenv, time.Now, &bytes.Buffer{}, &bytes.Buffer{}))

	uc, err := home.readConfig()
	require.NoError(t, err)
	assert.Equal(t, "ann", uc.Approver)
	assert.Equal(t, "/records", uc.RecordDir)
	assert.Nil(t, uc.Decisions)
	endpoints, err := uc.endpoints()
	require.NoError(t, err)
	assert.Equal(t, classify.Endpoints{classify.PointCritic: {URL: layaURL}, classify.PointReaction: {URL: layaURL}}, endpoints)
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
	require.Equal(t, 0, runClassifier([]string{"set", "critic", "--url", srv.URL, "--key-env", "LAYA_KEY"}, getenv, time.Now, &bytes.Buffer{}, &bytes.Buffer{}))
	var stdout, stderr bytes.Buffer

	code := runClassifier([]string{"probe", "critic"}, getenv, time.Now, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.True(t, strings.HasPrefix(stdout.String(), "critic\t"+strings.TrimPrefix(srv.URL, "http://")+"\tblue 0.97\t"), stdout.String())
	assert.Equal(t, "Bearer secret", auth)
}

// extract asks the endpoint of the critic point and records its answers
// It asks claude alone with no record when nothing is set up
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
		{"a sure endpoint answers alone",
			[][]string{{"set", "critic", "--url", srv.URL}}, want{1, 1}},
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
			var report bytes.Buffer
			require.Equal(t, 0, runReport([]string{"extract"}, getenv, now, &report, &stderr), stderr.String())
			assert.Equal(t, "extract\tunknown\tmodel\textractions 1\tproposed 1\trefused drafts -\tquestions false -\n", report.String())
		})
	}
}

// A critic endpoint edited by hand into config.json is checked before any model call
func TestRunKnowledgeExtractInvalidEndpoint(t *testing.T) {
	home, records := homeDir(t.TempDir()), t.TempDir()
	require.NoError(t, os.MkdirAll(home.dir(), 0o700))
	require.NoError(t, os.WriteFile(home.configPath(), []byte(`{"classifiers":{"critic":{"url":"localhost:8000"}}}`), 0o600))
	getenv := func(k string) string { return map[string]string{"HOME": string(home), envRecordDir: records}[k] }
	out := filepath.Join(string(home), "out.txt")
	require.NoError(t, os.WriteFile(out, []byte("cd repo && git status"), 0o600))
	var id bytes.Buffer
	require.Equal(t, 0, runRun([]string{"record", "--producer", "session", "--label", "repo=nodloop", "--output", out}, getenv, time.Now, &id, &bytes.Buffer{}))
	client := llmmock.NewMockClient(gomock.NewController(t))
	var stderr bytes.Buffer

	code := runKnowledge([]string{"extract", "--from", strings.TrimSpace(id.String())}, getenv, client, time.Now, &bytes.Buffer{}, &stderr)

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), `critic: classify: invalid endpoint: "localhost:8000" is not an absolute http or https URL`)
}
