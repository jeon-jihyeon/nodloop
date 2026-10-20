package classify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/classify/classifymock"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

var questions = classify.Questions{"holds": "It holds beyond this run.", "states": "It states the correction."}

var fixed = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

// An endpoint that answers every question with its probability or fails with the status
type endpoint struct {
	yes    map[string]float64
	status int
	// Bytes of blank before the answer so the body passes the read limit
	pad int
	// The model the answer names
	model string
}

// Serves the Jev wire format and keeps the last request body and authorization header
func (e endpoint) serve(t *testing.T) (*httptest.Server, *[]byte, *string) {
	t.Helper()
	var body []byte
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		auth = r.Header.Get("Authorization")
		if e.status != 0 {
			http.Error(w, "model not loaded", e.status)
			return
		}
		_, _ = w.Write(bytes.Repeat([]byte(" "), e.pad))
		answers := map[string]any{}
		for name, yes := range e.yes {
			answers[name] = map[string]any{"type": "noul", "noul": yes, "confidence": yes, "action": map[string]any{"act_probability": 1.0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": e.model, "answers": answers})
	}))
	t.Cleanup(srv.Close)
	return srv, &body, &auth
}

func (e endpoint) member(t *testing.T, name string) classify.Member {
	t.Helper()
	srv, _, _ := e.serve(t)
	return classify.Member{Name: name, Classifier: classify.NewHTTP(classify.Endpoint{URL: srv.URL}, "", time.Second)}
}

func TestHTTPClassify(t *testing.T) {
	type args struct {
		endpoint endpoint
		key      string
	}
	type want struct {
		answers classify.Answers
		auth    string
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"noul answers come back as yes",
			args{endpoint{yes: map[string]float64{"holds": 0.42, "states": 0.91}}, ""},
			want{answers: classify.Answers{"holds": {Yes: 0.42}, "states": {Yes: 0.91}}}},
		{"a key is sent as a bearer token",
			args{endpoint{yes: map[string]float64{"holds": 1, "states": 1}}, "secret"},
			want{answers: classify.Answers{"holds": {Yes: 1}, "states": {Yes: 1}}, auth: "Bearer secret"}},
		{"a question left out fails",
			args{endpoint{yes: map[string]float64{"states": 0.9}}, ""},
			want{err: classify.ErrAnswerMissing}},
		{"a probability over 1 fails",
			args{endpoint{yes: map[string]float64{"holds": 1.5, "states": 0.9}}, ""},
			want{err: classify.ErrResponseInvalid}},
		{"an error status fails",
			args{endpoint{status: http.StatusServiceUnavailable}, ""},
			want{err: classify.ErrStatus}},
		{"a body over the read limit fails",
			args{endpoint{yes: map[string]float64{"holds": 1, "states": 1}, pad: 1 << 20}, ""},
			want{err: classify.ErrResponseInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, body, auth := tc.args.endpoint.serve(t)
			h := classify.NewHTTP(classify.Endpoint{URL: srv.URL, Model: "laya"}, tc.args.key, time.Second)

			got, err := h.Classify(context.Background(), classify.Request{State: "the draft", Questions: questions})

			require.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.auth, *auth)
			assert.JSONEq(t, `{"model":"laya","state":"the draft","questions":{
				"holds":{"type":"noul","instructions":"It holds beyond this run."},
				"states":{"type":"noul","instructions":"It states the correction."}}}`, string(*body))
			assert.Equal(t, tc.want.answers, got)
		})
	}
}

// The built in member a point falls back to with fixed answers or a failure
func builtin(t *testing.T, answers classify.Answers, err error) classify.Member {
	t.Helper()
	m := classifymock.NewMockClassifier(gomock.NewController(t))
	m.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(answers, err).AnyTimes()
	return classify.Member{Name: "claude", Classifier: m}
}

func TestPlanClassify(t *testing.T) {
	sure := endpoint{yes: map[string]float64{"holds": 0.95, "states": 0.9}}
	unsure := endpoint{yes: map[string]float64{"holds": 0.6, "states": 0.1}}
	down := endpoint{status: http.StatusInternalServerError}
	claude := classify.Answers{"holds": {Yes: 1}, "states": {Yes: 0}}
	type args struct {
		endpoint endpoint
		// What the built in member answers
		answers classify.Answers
		err     error
	}
	type want struct {
		answers classify.Answers
		asked   []string
		// Every error the plan returns joined
		errs []error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a sure endpoint answers alone",
			args{sure, claude, nil},
			want{answers: classify.Answers{"holds": {Yes: 0.95}, "states": {Yes: 0.9}}, asked: []string{"laya"}}},
		{"an unsure endpoint falls back to claude",
			args{unsure, claude, nil},
			want{answers: claude, asked: []string{"laya", "claude"}}},
		{"a failing endpoint falls back to claude",
			args{down, claude, nil},
			want{answers: claude, asked: []string{"laya", "claude"}}},
		{"an unsure endpoint and a failing claude fail with the error of claude",
			args{unsure, nil, assert.AnError},
			want{asked: []string{"laya", "claude"}, errs: []error{assert.AnError}}},
		{"a failing endpoint and a failing claude fail with both errors",
			args{down, nil, assert.AnError},
			want{asked: []string{"laya", "claude"}, errs: []error{classify.ErrStatus, assert.AnError}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			traces, err := tracefile.New(t.TempDir())
			require.NoError(t, err)
			plan := classify.NewPlan(classify.PointCritic, tc.args.endpoint.member(t, "laya"),
				builtin(t, tc.args.answers, tc.args.err), traces, seal, func() time.Time { return fixed })

			got, err := plan.Rule(ctx, classify.Request{Ref: "run-1", State: "the draft", Questions: questions})

			assert.Equal(t, len(tc.want.errs) > 0, err != nil, err)
			for _, want := range tc.want.errs {
				assert.ErrorIs(t, err, want)
			}
			assert.Equal(t, tc.want.answers, got.Answers)
			recorded, err := traces.List(ctx, record.TraceFilter{Name: string(trace.NameClassify)})
			require.NoError(t, err)
			require.Len(t, recorded, 1)
			assert.Equal(t, recorded[0].ID, got.TraceID, "the ruling names the trace that records it")
			assert.Equal(t, "critic", recorded[0].Subject)
			assert.Equal(t, "run-1", recorded[0].Ref)
			assert.Equal(t, len(tc.want.errs) > 0, recorded[0].Error != "")
			var out struct {
				Members []struct {
					Name string `json:"name"`
				} `json:"members"`
			}
			require.NoError(t, json.Unmarshal(recorded[0].Output, &out))
			var asked []string
			for _, m := range out.Members {
				asked = append(asked, m.Name)
			}
			assert.Equal(t, tc.want.asked, asked)
		})
	}
}

// A plan without an endpoint asks the built in member alone and still records its trace
func TestPlanClassifyAlone(t *testing.T) {
	claude := classify.Answers{"holds": {Yes: 0.6}, "states": {Yes: 0}}
	type args struct {
		answers classify.Answers
		err     error
	}
	type want struct {
		answers classify.Answers
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an unsure answer stands since no member comes after it", args{claude, nil}, want{claude, nil}},
		{"a failing member fails the plan", args{nil, assert.AnError}, want{nil, assert.AnError}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			traces, err := tracefile.New(t.TempDir())
			require.NoError(t, err)
			plan := classify.NewPlan(classify.PointReaction, classify.Member{}, builtin(t, tc.args.answers, tc.args.err), traces, seal,
				func() time.Time { return fixed })

			got, err := plan.Rule(ctx, classify.Request{Ref: "run-1", State: "no, keep it short", Questions: questions})

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.answers, got.Answers)
			recorded, err := traces.List(ctx, record.TraceFilter{Name: string(trace.NameClassify)})
			require.NoError(t, err)
			require.Len(t, recorded, 1)
			assert.Equal(t, recorded[0].ID, got.TraceID)
			var out struct {
				Members []struct {
					Name string `json:"name"`
				} `json:"members"`
			}
			require.NoError(t, json.Unmarshal(recorded[0].Output, &out))
			require.Len(t, out.Members, 1)
			assert.Equal(t, "claude", out.Members[0].Name)
			assert.NoError(t, schema.Validate(string(trace.NameClassify), recorded[0]))
		})
	}
}

// A ruling no record holds keeps its answers and names no trace
func TestPlanRuleAppendFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "traces.jsonl"), 0o700))
	traces, err := tracefile.New(dir)
	require.NoError(t, err)
	sure := endpoint{yes: map[string]float64{"holds": 0.95, "states": 0.9}}
	plan := classify.NewPlan(classify.PointCritic, sure.member(t, "laya"), builtin(t, nil, nil), traces, seal, func() time.Time { return fixed })

	got, err := plan.Rule(context.Background(), classify.Request{Ref: "run-1", State: "the draft", Questions: questions})

	assert.ErrorIs(t, err, tracefile.ErrAppend)
	assert.Equal(t, classify.Ruling{Answers: classify.Answers{"holds": {Yes: 0.95}, "states": {Yes: 0.9}}}, got)
}

// The trace keeps how long each member took and the whole request
// The clock moves a second each time it is read
func TestPlanClassifyTimes(t *testing.T) {
	ctx := context.Background()
	traces, err := tracefile.New(t.TempDir())
	require.NoError(t, err)
	unsure := endpoint{yes: map[string]float64{"holds": 0.6, "states": 0.1}}
	at := fixed
	tick := func() time.Time {
		at = at.Add(time.Second)
		return at
	}
	claude := builtin(t, classify.Answers{"holds": {Yes: 1}, "states": {Yes: 1}}, nil)

	_, err = classify.NewPlan(classify.PointCritic, unsure.member(t, "laya"), claude, traces, seal, tick).
		Classify(ctx, classify.Request{State: "the draft", Questions: questions})

	require.NoError(t, err)
	recorded, err := traces.List(ctx, record.TraceFilter{Name: string(trace.NameClassify)})
	require.NoError(t, err)
	require.Len(t, recorded, 1)
	var out struct {
		Members []struct {
			MS int64 `json:"ms"`
		} `json:"members"`
	}
	require.NoError(t, json.Unmarshal(recorded[0].Output, &out))
	assert.Equal(t, int64(5000), recorded[0].DurationMS)
	require.Len(t, out.Members, 2)
	assert.Equal(t, []int64{1000, 1000}, []int64{out.Members[0].MS, out.Members[1].MS})
}

// The critic keeps its state as text and every other point only its HMAC with the questions by name
// The trace names the model of the built in member once it answered
func TestPlanClassifyKeeps(t *testing.T) {
	const words = "## User message\n\nno, quote the refund window"
	sure := endpoint{yes: map[string]float64{"holds": 0.95, "states": 0.9}}
	unsure := endpoint{yes: map[string]float64{"holds": 0.6, "states": 0.1}}
	sonnet := member{name: "claude", reply: classify.Reply{Answers: classify.Answers{"holds": {Yes: 1}, "states": {Yes: 0}}, Model: "claude-sonnet-5-5"}}
	type args struct {
		point    classify.Point
		endpoint endpoint
	}
	type want struct {
		input string
		model string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the critic keeps the draft and the instructions", args{classify.PointCritic, unsure},
			want{`{"state":` + quoted(t, words) + `,"questions":{"holds":"It holds beyond this run.","states":"It states the correction."}}`, "claude-sonnet-5-5"}},
		{"the reaction keeps the HMAC of the person's words and the names", args{classify.PointReaction, unsure},
			want{`{"state":` + quoted(t, seal(words)) + `,"questions":["holds","states"]}`, "claude-sonnet-5-5"}},
		{"a sure endpoint leaves the model of the built in member out", args{classify.PointReaction, sure},
			want{`{"state":` + quoted(t, seal(words)) + `,"questions":["holds","states"]}`, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			traces, err := tracefile.New(t.TempDir())
			require.NoError(t, err)
			claude := classify.Member{Name: sonnet.name, Classifier: replier(t, sonnet.reply)}
			plan := classify.NewPlan(tc.args.point, tc.args.endpoint.member(t, "laya"), claude, traces, seal, func() time.Time { return fixed })

			_, err = plan.Classify(ctx, classify.Request{Ref: "run-1", State: words, Questions: questions})

			require.NoError(t, err)
			recorded, err := traces.List(ctx, record.TraceFilter{Name: string(trace.NameClassify)})
			require.NoError(t, err)
			require.Len(t, recorded, 1)
			assert.JSONEq(t, tc.want.input, string(recorded[0].Input))
			assert.Equal(t, tc.want.model, recorded[0].Model)
			require.NoError(t, schema.Validate(string(trace.NameClassify), recorded[0]))
		})
	}
}

// A replier that may answer any number of times
func replier(t *testing.T, reply classify.Reply) classify.Classifier {
	t.Helper()
	r := classifymock.NewMockReplier(gomock.NewController(t))
	r.EXPECT().Reply(gomock.Any(), gomock.Any()).Return(reply, nil).AnyTimes()
	return r
}

func quoted(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}

func TestEndpointsWith(t *testing.T) {
	type args struct {
		point classify.Point
		url   string
	}
	type want struct {
		// The URL saved under the point
		// Empty when the endpoint is refused
		url string
		err error
	}
	const local = "http://localhost:8000/v1/systemone"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a local server is set", args{classify.PointCritic, local}, want{local, nil}},
		{"an https API is set", args{classify.PointReaction, "https://openrouter.ai/api/alpha/decisions"}, want{"https://openrouter.ai/api/alpha/decisions", nil}},
		{"an unknown point fails", args{"review", local}, want{"", classify.ErrPointUnknown}},
		{"a relative URL fails", args{classify.PointCritic, "localhost:8000"}, want{"", classify.ErrEndpointInvalid}},
		{"another scheme fails", args{classify.PointCritic, "ftp://localhost/v1"}, want{"", classify.ErrEndpointInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := classify.Endpoints(nil).With(tc.args.point, classify.Endpoint{URL: tc.args.url})

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.url, got[tc.args.point].URL)
		})
	}
}

func TestEndpointsWithout(t *testing.T) {
	type want struct {
		endpoints classify.Endpoints
		err       error
	}
	tcs := []struct {
		name string
		args classify.Point
		want want
	}{
		{"the endpoint of the point is removed", classify.PointCritic, want{endpoints: classify.Endpoints{}}},
		{"another point leaves the endpoints", classify.PointReaction, want{endpoints: classify.Endpoints{classify.PointCritic: {URL: "http://localhost:8000"}}}},
		{"an unknown point fails", "review", want{err: classify.ErrPointUnknown}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			endpoints := classify.Endpoints{classify.PointCritic: {URL: "http://localhost:8000"}}

			got, err := endpoints.Without(tc.args)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.endpoints, got)
			assert.Len(t, endpoints, 1, "the endpoints it was called on stay as they were")
		})
	}
}

func TestEndpointsCheck(t *testing.T) {
	tcs := []struct {
		name      string
		endpoints classify.Endpoints
		err       error
	}{
		{"points with URLs pass", classify.Endpoints{classify.PointCritic: {URL: "http://localhost:8000"}}, nil},
		{"an unknown point fails", classify.Endpoints{"review": {URL: "http://localhost:8000"}}, classify.ErrPointUnknown},
		{"a URL edited into a relative one fails", classify.Endpoints{classify.PointCritic: {URL: "localhost"}}, classify.ErrEndpointInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.endpoints.Check()

			assert.ErrorIs(t, err, tc.err)
		})
	}
}

func TestEndpointName(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"host and path name the endpoint", "http://localhost:8000/v1/systemone", "localhost:8000/v1/systemone"},
		{"another path on the same host differs", "http://localhost:8000/v1/systemtwo", "localhost:8000/v1/systemtwo"},
		{"a URL without a path is its host", "https://openrouter.ai/", "openrouter.ai"},
		{"a URL that does not parse is named as written", "http://local host:8000", "http://local host:8000"},
		{"a URL without a host is named as written", "localhost", "localhost"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, classify.Endpoint{URL: tc.args}.Name())
		})
	}
}

func TestEndpointClassifier(t *testing.T) {
	tcs := []struct {
		name string
		// The env variable the endpoint names for its key
		args string
		want string
	}{
		{"the key the env holds is sent as a bearer token", "LAYA_KEY", "Bearer secret"},
		{"no env variable sends no key", "", ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, _, auth := endpoint{yes: map[string]float64{"holds": 1, "states": 1}}.serve(t)
			getenv := func(k string) string { return map[string]string{"LAYA_KEY": "secret"}[k] }

			_, err := classify.Endpoint{URL: srv.URL, KeyEnv: tc.args}.Classifier(getenv, time.Second).
				Classify(context.Background(), classify.Request{State: "the draft", Questions: questions})

			require.NoError(t, err)
			assert.Equal(t, tc.want, *auth)
		})
	}
}
