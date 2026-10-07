package classify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
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
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "laya", "answers": answers})
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
type builtin struct {
	answers classify.Answers
	err     error
}

func (b builtin) Classify(context.Context, classify.Request) (classify.Answers, error) {
	return b.answers, b.err
}

func TestPlanClassify(t *testing.T) {
	sure := endpoint{yes: map[string]float64{"holds": 0.95, "states": 0.9}}
	unsure := endpoint{yes: map[string]float64{"holds": 0.6, "states": 0.1}}
	down := endpoint{status: http.StatusInternalServerError}
	claude := builtin{answers: classify.Answers{"holds": {Yes: 1}, "states": {Yes: 0}}}
	type args struct {
		endpoint endpoint
		builtin  builtin
	}
	type want struct {
		answers classify.Answers
		asked   []string
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a sure endpoint answers alone",
			args{sure, claude},
			want{answers: classify.Answers{"holds": {Yes: 0.95}, "states": {Yes: 0.9}}, asked: []string{"laya"}}},
		{"an unsure endpoint falls back to claude",
			args{unsure, claude},
			want{answers: classify.Answers{"holds": {Yes: 1}, "states": {Yes: 0}}, asked: []string{"laya", "claude"}}},
		{"a failing endpoint falls back to claude",
			args{down, claude},
			want{answers: classify.Answers{"holds": {Yes: 1}, "states": {Yes: 0}}, asked: []string{"laya", "claude"}}},
		{"a failing claude fails the plan",
			args{down, builtin{err: classify.ErrStatus}},
			want{asked: []string{"laya", "claude"}, err: classify.ErrStatus}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			traces, err := tracefile.New(t.TempDir())
			require.NoError(t, err)
			plan := classify.NewPlan(classify.PointCritic, tc.args.endpoint.member(t, "laya"),
				classify.Member{Name: "claude", Classifier: tc.args.builtin}, traces, func() time.Time { return fixed })

			got, err := plan.Classify(ctx, classify.Request{Ref: "run-1", State: "the draft", Questions: questions})

			require.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.answers, got)
			recorded, err := traces.List(ctx, trace.Filter{Name: trace.NameClassify})
			require.NoError(t, err)
			require.Len(t, recorded, 1)
			assert.Equal(t, "critic", recorded[0].Subject)
			assert.Equal(t, "run-1", recorded[0].Ref)
			assert.Equal(t, tc.want.err != nil, recorded[0].Error != "")
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
	claude := classify.Member{Name: "claude", Classifier: builtin{answers: classify.Answers{"holds": {Yes: 1}, "states": {Yes: 1}}}}

	_, err = classify.NewPlan(classify.PointCritic, unsure.member(t, "laya"), claude, traces, tick).
		Classify(ctx, classify.Request{State: "the draft", Questions: questions})

	require.NoError(t, err)
	recorded, err := traces.List(ctx, trace.Filter{Name: trace.NameClassify})
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
	endpoints := classify.Endpoints{classify.PointCritic: {URL: "http://localhost:8000"}}

	got, err := endpoints.Without(classify.PointCritic)

	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Len(t, endpoints, 1, "the endpoints it was called on stay as they were")
	_, err = endpoints.Without("review")
	assert.ErrorIs(t, err, classify.ErrPointUnknown)
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
	assert.Equal(t, "localhost:8000", classify.Endpoint{URL: "http://localhost:8000/v1/systemone"}.Name())
}
