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

func TestPlanClassify(t *testing.T) {
	sure := endpoint{yes: map[string]float64{"holds": 0.95, "states": 0.9}}
	unsure := endpoint{yes: map[string]float64{"holds": 0.6, "states": 0.1}}
	down := endpoint{status: http.StatusInternalServerError}
	type args struct {
		setup     classify.Setup
		endpoints []endpoint
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
		{"single asks its member",
			args{classify.Setup{Mode: classify.ModeSingle, Members: []string{"a"}}, []endpoint{unsure}},
			want{answers: classify.Answers{"holds": {Yes: 0.6}, "states": {Yes: 0.1}}, asked: []string{"a"}}},
		{"single returns the failure of its member",
			args{classify.Setup{Mode: classify.ModeSingle, Members: []string{"a"}}, []endpoint{down}},
			want{asked: []string{"a"}, err: classify.ErrStatus}},
		{"cascade stops at the first member over the threshold",
			args{classify.Setup{Mode: classify.ModeCascade, Members: []string{"a", "b"}, Threshold: 0.8}, []endpoint{sure, unsure}},
			want{answers: classify.Answers{"holds": {Yes: 0.95}, "states": {Yes: 0.9}}, asked: []string{"a"}}},
		{"cascade passes a member below the threshold on",
			args{classify.Setup{Mode: classify.ModeCascade, Members: []string{"a", "b"}, Threshold: 0.8}, []endpoint{unsure, sure}},
			want{answers: classify.Answers{"holds": {Yes: 0.95}, "states": {Yes: 0.9}}, asked: []string{"a", "b"}}},
		{"cascade passes a failing member on",
			args{classify.Setup{Mode: classify.ModeCascade, Members: []string{"a", "b"}, Threshold: 0.8}, []endpoint{down, unsure}},
			want{answers: classify.Answers{"holds": {Yes: 0.6}, "states": {Yes: 0.1}}, asked: []string{"a", "b"}}},
		{"cascade keeps the last answer below the threshold",
			args{classify.Setup{Mode: classify.ModeCascade, Members: []string{"a", "b"}, Threshold: 0.99}, []endpoint{unsure, sure}},
			want{answers: classify.Answers{"holds": {Yes: 0.95}, "states": {Yes: 0.9}}, asked: []string{"a", "b"}}},
		{"parallel all keeps the lowest yes",
			args{classify.Setup{Mode: classify.ModeParallel, Members: []string{"a", "b"}, Combine: classify.CombineAll}, []endpoint{sure, unsure}},
			want{answers: classify.Answers{"holds": {Yes: 0.6}, "states": {Yes: 0.1}}, asked: []string{"a", "b"}}},
		{"parallel any keeps the highest yes",
			args{classify.Setup{Mode: classify.ModeParallel, Members: []string{"a", "b"}, Combine: classify.CombineAny}, []endpoint{unsure, sure}},
			want{answers: classify.Answers{"holds": {Yes: 0.95}, "states": {Yes: 0.9}}, asked: []string{"a", "b"}}},
		{"parallel fails when a member fails",
			args{classify.Setup{Mode: classify.ModeParallel, Members: []string{"a", "b"}, Combine: classify.CombineAll}, []endpoint{sure, down}},
			want{asked: []string{"a", "b"}, err: classify.ErrStatus}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			traces, err := tracefile.New(t.TempDir())
			require.NoError(t, err)
			members := make([]classify.Member, 0, len(tc.args.endpoints))
			for i, e := range tc.args.endpoints {
				members = append(members, e.member(t, tc.args.setup.Members[i]))
			}
			plan := classify.NewPlan(classify.PointCritic, tc.args.setup, members, traces, func() time.Time { return fixed })

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
	setup := classify.Setup{Mode: classify.ModeCascade, Members: []string{"a", "b"}, Threshold: 0.8}
	at := fixed
	tick := func() time.Time {
		at = at.Add(time.Second)
		return at
	}

	_, err = classify.NewPlan(classify.PointCritic, setup, []classify.Member{unsure.member(t, "a"), unsure.member(t, "b")}, traces, tick).
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

// Parallel members are asked at once
// Each endpoint answers only after both requests arrived so a plan that asks one after the other times out
func TestPlanClassifyParallelAtOnce(t *testing.T) {
	arrived := make(chan struct{}, 2)
	both := make(chan struct{})
	go func() {
		<-arrived
		<-arrived
		close(both)
	}()
	members := make([]classify.Member, 0, 2)
	for _, name := range []string{"a", "b"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			arrived <- struct{}{}
			<-both
			_, _ = w.Write([]byte(`{"answers":{"holds":{"noul":0.9},"states":{"noul":0.9}}}`))
		}))
		t.Cleanup(srv.Close)
		members = append(members, classify.Member{Name: name, Classifier: classify.NewHTTP(classify.Endpoint{URL: srv.URL}, "", time.Second)})
	}
	traces, err := tracefile.New(t.TempDir())
	require.NoError(t, err)
	setup := classify.Setup{Mode: classify.ModeParallel, Members: []string{"a", "b"}, Combine: classify.CombineAll}

	got, err := classify.NewPlan(classify.PointCritic, setup, members, traces, func() time.Time { return fixed }).
		Classify(context.Background(), classify.Request{State: "the draft", Questions: questions})

	require.NoError(t, err)
	assert.Equal(t, classify.Answers{"holds": {Yes: 0.9}, "states": {Yes: 0.9}}, got)
}

func TestNewSetup(t *testing.T) {
	type args struct {
		members   []string
		mode      classify.Mode
		threshold float64
		combine   classify.Combine
	}
	tcs := []struct {
		name string
		args args
		want classify.Setup
		err  error
	}{
		{"one member is single", args{members: []string{"laya"}},
			classify.Setup{Mode: classify.ModeSingle, Members: []string{"laya"}}, nil},
		{"two members are a cascade at 0.8", args{members: []string{"laya", "claude"}},
			classify.Setup{Mode: classify.ModeCascade, Members: []string{"laya", "claude"}, Threshold: 0.8}, nil},
		{"a cascade keeps its threshold", args{members: []string{"laya", "claude"}, threshold: 0.6},
			classify.Setup{Mode: classify.ModeCascade, Members: []string{"laya", "claude"}, Threshold: 0.6}, nil},
		{"a parallel combines all by default", args{members: []string{"laya", "claude"}, mode: classify.ModeParallel},
			classify.Setup{Mode: classify.ModeParallel, Members: []string{"laya", "claude"}, Combine: classify.CombineAll}, nil},
		{"single with two members fails", args{members: []string{"laya", "claude"}, mode: classify.ModeSingle}, classify.Setup{}, classify.ErrSetupInvalid},
		{"cascade with one member fails", args{members: []string{"laya"}, mode: classify.ModeCascade}, classify.Setup{}, classify.ErrSetupInvalid},
		{"a member named twice fails", args{members: []string{"laya", "laya"}}, classify.Setup{}, classify.ErrSetupInvalid},
		{"a threshold over 1 fails", args{members: []string{"laya", "claude"}, threshold: 1.5}, classify.Setup{}, classify.ErrSetupInvalid},
		{"a negative threshold fails", args{members: []string{"laya", "claude"}, threshold: -0.5}, classify.Setup{}, classify.ErrSetupInvalid},
		{"a threshold outside cascade fails", args{members: []string{"laya"}, threshold: 0.5}, classify.Setup{}, classify.ErrSetupInvalid},
		{"a combine outside parallel fails", args{members: []string{"laya", "claude"}, combine: classify.CombineAny}, classify.Setup{}, classify.ErrSetupInvalid},
		{"an unknown combine fails", args{members: []string{"laya", "claude"}, mode: classify.ModeParallel, combine: "most"}, classify.Setup{}, classify.ErrSetupInvalid},
		{"an unknown mode fails", args{members: []string{"laya", "claude"}, mode: "vote"}, classify.Setup{}, classify.ErrSetupInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := classify.NewSetup(tc.args.members, tc.args.mode, tc.args.threshold, tc.args.combine)

			require.ErrorIs(t, err, tc.err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A setup read from a config edited by hand holds the parameter its mode needs
func TestSetupCheck(t *testing.T) {
	two := []string{"laya", "claude"}
	tcs := []struct {
		name  string
		setup classify.Setup
		want  error
	}{
		{"a cascade with a threshold passes", classify.Setup{Mode: classify.ModeCascade, Members: two, Threshold: 0.8}, nil},
		{"a cascade without a threshold fails", classify.Setup{Mode: classify.ModeCascade, Members: two}, classify.ErrSetupInvalid},
		{"a parallel with a combine passes", classify.Setup{Mode: classify.ModeParallel, Members: two, Combine: classify.CombineAny}, nil},
		{"a parallel without a combine fails", classify.Setup{Mode: classify.ModeParallel, Members: two}, classify.ErrSetupInvalid},
		{"no mode fails", classify.Setup{Members: two}, classify.ErrSetupInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.setup.Check()

			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestEndpointsWith(t *testing.T) {
	type args struct {
		name string
		url  string
	}
	type want struct {
		// The URL saved under the name
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
		{"a local server is added", args{"laya", local}, want{local, nil}},
		{"an https API is added", args{"jev", "https://openrouter.ai/api/alpha/decisions"}, want{"https://openrouter.ai/api/alpha/decisions", nil}},
		{"claude is reserved", args{"claude", local}, want{"", classify.ErrEndpointInvalid}},
		{"an empty name fails", args{"", local}, want{"", classify.ErrEndpointInvalid}},
		{"a relative URL fails", args{"laya", "localhost:8000"}, want{"", classify.ErrEndpointInvalid}},
		{"another scheme fails", args{"laya", "ftp://localhost/v1"}, want{"", classify.ErrEndpointInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := classify.Endpoints(nil).With(tc.args.name, classify.Endpoint{URL: tc.args.url})

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.url, got[tc.args.name].URL)
		})
	}
}

func TestEndpointsCheck(t *testing.T) {
	endpoints := classify.Endpoints{"laya": {URL: "http://localhost:8000/v1/systemone"}}
	tcs := []struct {
		name    string
		members []string
		err     error
	}{
		{"claude and an added endpoint pass", []string{"laya", "claude"}, nil},
		{"an endpoint never added fails", []string{"jev", "claude"}, classify.ErrClassifierUnknown},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := endpoints.Check(classify.Setup{Mode: classify.ModeCascade, Members: tc.members})

			assert.ErrorIs(t, err, tc.err)
		})
	}
}

func TestDecisionsUsing(t *testing.T) {
	d := classify.Decisions{classify.PointCritic: {Mode: classify.ModeCascade, Members: []string{"laya", "claude"}}}

	assert.Equal(t, []classify.Point{classify.PointCritic}, d.Using("laya"))
	assert.Empty(t, d.Using("jev"))
}
