package classify_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
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

// The state of a judge request holds the anchor and the change
const judgeState = `{"change":"add: run go vet first","anchor":["plan: lint before tests"]}`

var judgeQuestions = classify.Questions{"decided": "The anchor settles it.", "against": "It goes against the anchor."}

// Stands for the HMAC under the key of the record directory
func seal(text string) string {
	return fmt.Sprintf("hmac of %d runes", len([]rune(text)))
}

// One member of a panel
// An endpoint when set and else a replier with its reply and error
type member struct {
	name     string
	reply    classify.Reply
	err      error
	endpoint *endpoint
}

func (m member) of(t *testing.T) classify.Member {
	t.Helper()
	if m.endpoint != nil {
		srv, _, _ := m.endpoint.serve(t)
		return classify.Member{Name: m.name, Classifier: classify.NewHTTP(classify.Endpoint{URL: srv.URL, Model: "laya"}, "", time.Second)}
	}
	r := classifymock.NewMockReplier(gomock.NewController(t))
	r.EXPECT().Reply(gomock.Any(), gomock.Any()).Return(m.reply, m.err)
	return classify.Member{Name: m.name, Classifier: r}
}

func TestPanelClassify(t *testing.T) {
	conform := classify.Answers{"decided": {Yes: 0.9}, "against": {Yes: 0.05}}
	opposed := classify.Answers{"decided": {Yes: 0.8}, "against": {Yes: 0.7}}
	other := classify.Answers{"decided": {Yes: 0.92}, "against": {Yes: 0.1}}
	opus := func(answers classify.Answers) classify.Reply {
		return classify.Reply{Answers: answers, Model: "claude-opus-5-5", Effort: "high"}
	}
	// A member as the trace records it
	type asked struct {
		Name    string           `json:"name"`
		Model   string           `json:"model"`
		Effort  string           `json:"effort"`
		Answers classify.Answers `json:"answers"`
		Error   string           `json:"error"`
	}
	type want struct {
		// The reply of each vote
		votes []classify.Reply
		// Whether each vote failed
		failed  []bool
		members []asked
	}
	tcs := []struct {
		name string
		args []member
		want want
	}{
		{"three answering members, a failing one and an endpoint each keep their model and effort",
			[]member{
				{name: "claude:1", reply: opus(conform)},
				{name: "claude:2", reply: opus(opposed)},
				{name: "claude:3", reply: opus(nil), err: assert.AnError},
				{name: "laya", endpoint: &endpoint{yes: map[string]float64{"decided": 0.92, "against": 0.1}, model: "laya-2"}},
			},
			want{
				votes:  []classify.Reply{opus(conform), opus(opposed), opus(nil), {Answers: other, Model: "laya-2"}},
				failed: []bool{false, false, true, false},
				members: []asked{
					{Name: "claude:1", Model: "claude-opus-5-5", Effort: "high", Answers: conform},
					{Name: "claude:2", Model: "claude-opus-5-5", Effort: "high", Answers: opposed},
					{Name: "claude:3", Model: "claude-opus-5-5", Effort: "high", Error: "assert.AnError general error for testing"},
					{Name: "laya", Model: "laya-2", Answers: other},
				},
			}},
		{"a member that misses a question gives no answer and keeps its model and effort",
			[]member{{name: "claude:1", reply: opus(classify.Answers{"decided": {Yes: 0.9}})}},
			want{
				votes:   []classify.Reply{opus(nil)},
				failed:  []bool{true},
				members: []asked{{Name: "claude:1", Model: "claude-opus-5-5", Effort: "high", Error: "classify: question not answered: against"}},
			}},
		{"a member that answers NaN gives no answer",
			[]member{{name: "claude:1", reply: opus(classify.Answers{"decided": {Yes: math.NaN()}, "against": {Yes: 0}})}},
			want{
				votes:   []classify.Reply{opus(nil)},
				failed:  []bool{true},
				members: []asked{{Name: "claude:1", Model: "claude-opus-5-5", Effort: "high", Error: "classify: endpoint answer is not valid: decided is NaN"}},
			}},
		{"a failing endpoint keeps the model it was sent",
			[]member{{name: "laya", endpoint: &endpoint{status: http.StatusServiceUnavailable}}},
			want{
				votes:   []classify.Reply{{Model: "laya"}},
				failed:  []bool{true},
				members: []asked{{Name: "laya", Model: "laya", Error: "classify: endpoint answered an error status: 503 model not loaded\n"}},
			}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			traces, err := tracefile.New(t.TempDir())
			require.NoError(t, err)
			members := make([]classify.Member, 0, len(tc.args))
			for _, m := range tc.args {
				members = append(members, m.of(t))
			}
			panel := classify.NewPanel(classify.PointJudge, members, traces, seal, func() time.Time { return fixed })

			got, err := panel.Classify(ctx, classify.Request{Ref: "run-1", State: judgeState, Questions: judgeQuestions})

			require.NoError(t, err)
			votes, failed := ballotOf(got)
			assert.Equal(t, tc.want.votes, votes)
			assert.Equal(t, tc.want.failed, failed)
			recorded, err := traces.List(ctx, record.TraceFilter{Name: string(trace.NameClassify)})
			require.NoError(t, err)
			require.Len(t, recorded, 1)
			back, ok := classify.RestoreBallot(trace.Restore(recorded[0]))
			require.True(t, ok)
			backVotes, backFailed := ballotOf(back)
			assert.Equal(t, []any{got.TraceID, votes, failed}, []any{back.TraceID, backVotes, backFailed}, "the ballot reads back from its trace")
			assert.Equal(t, got.TraceID, recorded[0].ID)
			assert.Equal(t, "judge", recorded[0].Subject)
			assert.Equal(t, "run-1", recorded[0].Ref)
			assert.Empty(t, recorded[0].Error)
			assert.JSONEq(t, `{"state":"`+seal(judgeState)+`","questions":["against","decided"]}`, string(recorded[0].Input))
			var out struct {
				Members []asked           `json:"members"`
				Answers *classify.Answers `json:"answers"`
			}
			require.NoError(t, json.Unmarshal(recorded[0].Output, &out))
			assert.Equal(t, tc.want.members, out.Members)
			assert.Nil(t, out.Answers, "a panel combines no answer")
			raw, err := json.Marshal(recorded[0])
			require.NoError(t, err)
			assert.NotContains(t, string(raw), "go vet", "no text of the change")
			assert.NotContains(t, string(raw), "lint before tests", "no text of the anchor")
			assert.NoError(t, schema.Validate(string(trace.NameClassify), recorded[0]))
		})
	}
}

// The reply of each vote and whether it failed as ErrMemberFailed or any error
func ballotOf(b classify.Ballot) ([]classify.Reply, []bool) {
	votes := make([]classify.Reply, 0, len(b.Votes))
	failed := make([]bool, 0, len(b.Votes))
	for _, v := range b.Votes {
		votes = append(votes, v.Reply)
		failed = append(failed, v.Err != nil)
	}
	return votes, failed
}

// A trace that is no classify or whose members do not decode holds no ballot and a failed member reads as ErrMemberFailed
func TestRestoreBallot(t *testing.T) {
	type want struct {
		ok bool
		// Whether each vote failed as ErrMemberFailed
		failed []bool
	}
	tcs := []struct {
		name string
		args trace.Trace
		want want
	}{
		{"a failed member reads as ErrMemberFailed", trace.Trace{
			Name: trace.NameClassify, Output: json.RawMessage(`{"members":[{"name":"a","answers":{"decided":{"yes":1}}},{"name":"b","error":"timeout"}]}`),
		}, want{true, []bool{false, true}}},
		{"a trace of another name holds none", trace.Trace{Name: trace.NameRun, Output: json.RawMessage(`{"members":[]}`)}, want{false, nil}},
		{"an output that does not decode holds none", trace.Trace{Name: trace.NameClassify, Output: json.RawMessage(`"members"`)}, want{false, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := classify.RestoreBallot(tc.args)

			var failed []bool
			for _, v := range got.Votes {
				failed = append(failed, errors.Is(v.Err, classify.ErrMemberFailed))
			}
			assert.Equal(t, tc.want, want{ok, failed})
		})
	}
}

// A ballot no record holds comes back with the error so its caller decides nothing on it
func TestPanelClassifyAppendFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "traces.jsonl"), 0o700))
	traces, err := tracefile.New(dir)
	require.NoError(t, err)
	reply := classify.Reply{Answers: classify.Answers{"decided": {Yes: 1}, "against": {Yes: 0}}, Model: "m", Effort: "high"}
	panel := classify.NewPanel(classify.PointJudge, []classify.Member{member{name: "claude:1", reply: reply}.of(t)}, traces, seal,
		func() time.Time { return fixed })

	got, err := panel.Classify(context.Background(), classify.Request{State: judgeState, Questions: judgeQuestions})

	assert.ErrorIs(t, err, tracefile.ErrAppend)
	assert.Empty(t, got.TraceID)
	require.Len(t, got.Votes, 1)
	assert.Equal(t, reply, got.Votes[0].Reply)
}

func TestHTTPReply(t *testing.T) {
	tcs := []struct {
		name string
		args endpoint
		want classify.Reply
	}{
		{"the model the endpoint answered with is named",
			endpoint{yes: map[string]float64{"holds": 1, "states": 1}, model: "laya-2"},
			classify.Reply{Answers: classify.Answers{"holds": {Yes: 1}, "states": {Yes: 1}}, Model: "laya-2"}},
		{"an endpoint that names none keeps the model it was sent",
			endpoint{yes: map[string]float64{"holds": 1, "states": 1}},
			classify.Reply{Answers: classify.Answers{"holds": {Yes: 1}, "states": {Yes: 1}}, Model: "laya"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, _, _ := tc.args.serve(t)
			h := classify.NewHTTP(classify.Endpoint{URL: srv.URL, Model: "laya"}, "", time.Second)

			got, err := h.Reply(context.Background(), classify.Request{State: "the draft", Questions: questions})

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
