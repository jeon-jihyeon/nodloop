package curate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

var start = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

func ref(id string) knowledge.Ref {
	return knowledge.Ref{ID: id, Version: 1}
}

// Eight approvals as drafted and three retires before start and one approval at start
func decisions() curate.Shots {
	var out curate.Shots
	for i := range 8 {
		id := fmt.Sprintf("k-a%d", i)
		out = append(out, curate.Shot{Ref: ref(id), At: start.Add(-time.Duration(i+1) * time.Hour), Content: "lesson " + id, Kept: true})
	}
	for i := range 3 {
		id := fmt.Sprintf("k-r%d", i)
		out = append(out, curate.Shot{Ref: ref(id), At: start.Add(-time.Duration(i+1) * time.Hour), Content: "lesson " + id})
	}
	return append(out, curate.Shot{Ref: ref("k-late"), At: start, Content: "lesson k-late", Kept: true})
}

// The sets the decisions draw at start
var drawnAtStart = [][]knowledge.Ref{
	{ref("k-a2"), ref("k-a4"), ref("k-a6"), ref("k-r0"), ref("k-a1"), ref("k-r1")},
	{ref("k-r0"), ref("k-a4"), ref("k-a0"), ref("k-a1"), ref("k-a2"), ref("k-r2")},
	{ref("k-a6"), ref("k-a7"), ref("k-a4"), ref("k-r1"), ref("k-r0"), ref("k-a5")},
}

// What a variant answers with the probabilities of yes
func answer(decided, against float64) json.RawMessage {
	return fmt.Appendf(nil, `{"decided":{"case_yes":"y","case_no":"n","probability":%v},"against":{"case_yes":"y","case_no":"n","probability":%v}}`,
		decided, against)
}

// Stands for the HMAC under the key of the record directory
func seal(text string) string {
	return fmt.Sprintf("hmac of %d runes", len([]rune(text)))
}

var change = curate.Case{
	Change:  "add: run the linter before the tests",
	Anchor:  []string{"plan: lint before tests", "lessons: name the failing test first", "rules: never push to main"},
	Carried: []string{"added: keep commits small"},
}

func TestJudgeJudge(t *testing.T) {
	type reply struct {
		output json.RawMessage
		err    error
	}
	// A member as the trace records it
	type member struct {
		Name   string `json:"name"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	type want struct {
		outcome curate.Outcome
		share   float64
		bin     curate.Bin
		// The error of the members that gave no answer
		err     error
		members []member
	}
	answered := []member{{"claude:1", "claude-sonnet-5-5", "high"}, {"claude:2", "claude-sonnet-5-5", "high"}, {"claude:3", "claude-sonnet-5-5", "high"}}
	tcs := []struct {
		name string
		args []reply
		want want
	}{
		{"three conforming variants are conform in the bin of their confidence",
			[]reply{{answer(0.9, 0.05), nil}, {answer(0.85, 0.05), nil}, {answer(0.95, 0.05), nil}},
			want{curate.OutcomeConform, 1, curate.Bin1, nil, answered}},
		{"three variants against an anchor line are against",
			[]reply{{answer(0.9, 0.8), nil}, {answer(0.4, 0.6), nil}, {answer(0.9, 0.9), nil}},
			want{curate.OutcomeAgainst, 0, "", nil, answered}},
		{"three variants that find the anchor silent are undecided",
			[]reply{{answer(0.3, 0.1), nil}, {answer(0.2, 0.2), nil}, {answer(0.45, 0.05), nil}},
			want{curate.OutcomeUndecided, 0, "", nil, answered}},
		{"members that differ split with the share and bin of the conforming ones",
			[]reply{{answer(0.9, 0.05), nil}, {answer(0.9, 0.15), nil}, {answer(0.9, 0.7), nil}},
			want{curate.OutcomeSplit, 2.0 / 3, curate.Bin2, nil, answered}},
		{"a variant whose call fails splits and keeps the model it asked",
			[]reply{{answer(0.9, 0.05), nil}, {nil, assert.AnError}, {answer(0.9, 0.05), nil}},
			want{curate.OutcomeSplit, 2.0 / 3, curate.Bin1, assert.AnError,
				[]member{{"claude:1", "claude-sonnet-5-5", "high"}, {"claude:2", "sonnet", "high"}, {"claude:3", "claude-sonnet-5-5", "high"}}}},
		{"a variant whose answer does not decode splits",
			[]reply{{answer(0.9, 0.05), nil}, {answer(0.9, 0.05), nil}, {json.RawMessage(`"conform"`), nil}},
			want{curate.OutcomeSplit, 2.0 / 3, curate.Bin1, curate.ErrAnswerInvalid,
				[]member{{"claude:1", "claude-sonnet-5-5", "high"}, {"claude:2", "claude-sonnet-5-5", "high"}, {"claude:3", "sonnet", "high"}}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			stores := testkit.Open(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			for _, r := range tc.args {
				client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: r.output, Model: "claude-sonnet-5-5"}, r.err)
			}
			members, err := curate.JudgeConfig{}.Next("sonnet", curate.JudgeEffort, decisions(), start).Members(client, decisions())
			require.NoError(t, err)
			judge := curate.NewJudge(classify.NewPanel(classify.PointJudge, members, stores.Traces, seal, stores.Clock.Now))
			state, err := json.Marshal(change)
			require.NoError(t, err)

			got, err := judge.Judge(ctx, "run-1", change)

			require.NoError(t, err)
			assert.Equal(t, tc.want.outcome, got.Outcome)
			assert.InDelta(t, tc.want.share, got.Share, 1e-9)
			assert.Equal(t, tc.want.bin, got.Bin)
			assert.ErrorIs(t, got.Err, tc.want.err)
			recorded, err := stores.Traces.Get(ctx, got.Trace)
			require.NoError(t, err)
			assert.Equal(t, "run-1", recorded.Ref)
			assert.JSONEq(t, `{"state":"`+seal(string(state))+`","questions":["against","decided"]}`, string(recorded.Input))
			var out struct {
				Members []member `json:"members"`
			}
			require.NoError(t, json.Unmarshal(recorded.Output, &out))
			assert.Equal(t, tc.want.members, out.Members)
		})
	}
}

// A judgment no record holds is never read
func TestJudgeJudgeAppendFails(t *testing.T) {
	stores := testkit.Open(t)
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: answer(0.9, 0.05)}, nil).Times(3)
	members, err := curate.JudgeConfig{}.Next("sonnet", curate.JudgeEffort, nil, start).Members(client, nil)
	require.NoError(t, err)
	traces := &testkit.FlakyTraces{Store: stores.Traces, Appends: testkit.Reads{Err: assert.AnError}}
	judge := curate.NewJudge(classify.NewPanel(classify.PointJudge, members, traces, seal, stores.Clock.Now))

	got, err := judge.Judge(context.Background(), "run-1", change)

	assert.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, curate.Judgment{}, got)
}

// Each variant asks with its own few-shot set and order of the anchor lines and the model and effort of the configuration
func TestJudgeConfigMembers(t *testing.T) {
	cfg := curate.JudgeConfig{
		Model: "claude-sonnet-5-5", Effort: "high", Prompt: curate.PromptVersion, Start: start,
		Sets: [][]knowledge.Ref{{ref("k-a0"), ref("k-r0")}, {ref("k-a1")}, nil},
	}
	tail := "## Changes carried out since the anchor version\n- added: keep commits small\n\n## Change\nadd: run the linter before the tests\n"
	type want struct {
		names   []string
		prompts []string
		models  []string
		efforts []string
	}
	stores := testkit.Open(t)
	client := llmmock.NewMockClient(gomock.NewController(t))
	var sent []llm.Request
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req llm.Request) (llm.Response, error) {
		sent = append(sent, req)
		return llm.Response{Output: answer(0.9, 0.05), Model: "claude-sonnet-5-5"}, nil
	}).Times(3)
	members, err := cfg.Members(client, decisions())
	require.NoError(t, err)
	judge := curate.NewJudge(classify.NewPanel(classify.PointJudge, members, stores.Traces, seal, stores.Clock.Now))

	_, err = judge.Judge(context.Background(), "run-1", change)

	require.NoError(t, err)
	got := want{}
	for i, m := range members {
		got.names = append(got.names, m.Name)
		got.prompts = append(got.prompts, sent[i].Prompt)
		got.models = append(got.models, sent[i].Model)
		got.efforts = append(got.efforts, sent[i].Effort)
	}
	assert.Equal(t, want{
		names: []string{"claude:1", "claude:2", "claude:3"},
		prompts: []string{
			"## Earlier decisions of the person\n- approved as drafted: lesson k-a0\n- retired: lesson k-r0\n\n" +
				"## Anchor lines\n- plan: lint before tests\n- lessons: name the failing test first\n- rules: never push to main\n\n" + tail,
			"## Earlier decisions of the person\n- approved as drafted: lesson k-a1\n\n" +
				"## Anchor lines\n- rules: never push to main\n- lessons: name the failing test first\n- plan: lint before tests\n\n" + tail,
			"## Earlier decisions of the person\nnone\n\n" +
				"## Anchor lines\n- lessons: name the failing test first\n- rules: never push to main\n- plan: lint before tests\n\n" + tail,
		},
		models:  []string{"claude-sonnet-5-5", "claude-sonnet-5-5", "claude-sonnet-5-5"},
		efforts: []string{"high", "high", "high"},
	}, got)
	assert.Equal(t, sent[0].System, sent[2].System, "every variant reads one set of rules")
	assert.Contains(t, sent[0].System, "1. decided: "+curate.JudgeQuestions["decided"])
	assert.Contains(t, sent[0].System, "2. against: "+curate.JudgeQuestions["against"])
	assert.Contains(t, string(sent[0].Schema), `"probability":{"type":"number","minimum":0,"maximum":1}`)
}

func TestJudgeConfigMembersShotUnknown(t *testing.T) {
	cfg := curate.JudgeConfig{Model: "sonnet", Effort: "high", Prompt: curate.PromptVersion, Start: start, Sets: [][]knowledge.Ref{{ref("k-gone")}}}

	got, err := cfg.Members(llmmock.NewMockClient(gomock.NewController(t)), decisions())

	assert.ErrorIs(t, err, curate.ErrShotUnknown)
	assert.ErrorContains(t, err, "k-gone v1")
	assert.Nil(t, got)
}

func TestJudgeConfigNext(t *testing.T) {
	earlier := start.Add(-14 * 24 * time.Hour)
	kept := curate.JudgeConfig{
		Model: "sonnet", Effort: "high", Prompt: curate.PromptVersion, Start: earlier, Sets: [][]knowledge.Ref{{ref("k-a0")}, {ref("k-a1")}, nil},
	}
	older := kept
	older.Prompt = "0"
	type args struct {
		config curate.JudgeConfig
		model  string
		effort string
	}
	tcs := []struct {
		name string
		args args
		want curate.JudgeConfig
	}{
		{"a first judge starts now with sets drawn from the decisions before now",
			args{curate.JudgeConfig{}, "sonnet", "high"},
			curate.JudgeConfig{Model: "sonnet", Effort: "high", Prompt: curate.PromptVersion, Start: start, Sets: drawnAtStart}},
		{"the same model and effort keep the configuration and its sets however many decisions came since",
			args{kept, "sonnet", "high"}, kept},
		{"another model is a new judge",
			args{kept, "opus", "high"},
			curate.JudgeConfig{Model: "opus", Effort: "high", Prompt: curate.PromptVersion, Start: start, Sets: drawnAtStart}},
		{"another effort is a new judge",
			args{kept, "sonnet", "max"},
			curate.JudgeConfig{Model: "sonnet", Effort: "max", Prompt: curate.PromptVersion, Start: start, Sets: drawnAtStart}},
		{"a configuration of another prompt version is a new judge",
			args{older, "sonnet", "high"},
			curate.JudgeConfig{Model: "sonnet", Effort: "high", Prompt: curate.PromptVersion, Start: start, Sets: drawnAtStart}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.config.Next(tc.args.model, tc.args.effort, decisions(), start))
		})
	}
}

func TestShotsDraw(t *testing.T) {
	type args struct {
		decisions curate.Shots
		start     time.Time
	}
	one := curate.Shots{{Ref: ref("k-a0"), At: start.Add(-time.Hour), Content: "lesson k-a0", Kept: true}}
	tcs := []struct {
		name string
		args args
		want [][]knowledge.Ref
	}{
		{"three sets of four approvals and two retires drawn from the decisions before the start", args{decisions(), start}, drawnAtStart},
		{"a later start draws other sets", args{decisions(), start.Add(24 * time.Hour)}, [][]knowledge.Ref{
			{ref("k-a2"), ref("k-a5"), ref("k-a3"), ref("k-a6"), ref("k-r1"), ref("k-r0")},
			{ref("k-a7"), ref("k-a5"), ref("k-a6"), ref("k-a4"), ref("k-r2"), ref("k-r1")},
			{ref("k-a3"), ref("k-r1"), ref("k-r0"), ref("k-a2"), ref("k-a7"), ref("k-a6")},
		}},
		{"few decisions give every variant the same short set", args{one, start}, [][]knowledge.Ref{{ref("k-a0")}, {ref("k-a0")}, {ref("k-a0")}}},
		{"no decision before the start leaves the sets empty", args{decisions(), start.Add(-12 * time.Hour)}, make([][]knowledge.Ref, 3)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.decisions.Draw(tc.args.start))
		})
	}
}

func TestVotes(t *testing.T) {
	v := func(decided, against float64) classify.Vote {
		return classify.Vote{Member: "m", Reply: classify.Reply{Answers: classify.Answers{"decided": {Yes: decided}, "against": {Yes: against}}}}
	}
	failed := classify.Vote{Member: "m", Err: assert.AnError}
	type want struct {
		outcome curate.Outcome
		share   float64
		bin     curate.Bin
		err     error
	}
	tcs := []struct {
		name string
		args curate.Votes
		want want
	}{
		{"no vote is split", nil, want{curate.OutcomeSplit, 0, "", nil}},
		{"decided at one half conforms at the edge of b1", curate.Votes{v(0.5, 0.05)}, want{curate.OutcomeConform, 1, curate.Bin1, nil}},
		{"just below b1 is b2", curate.Votes{v(0.9, 0.06)}, want{curate.OutcomeConform, 1, curate.Bin2, nil}},
		{"the edge of b2", curate.Votes{v(0.9, 0.15)}, want{curate.OutcomeConform, 1, curate.Bin2, nil}},
		{"just below b2 is b3", curate.Votes{v(0.9, 0.16)}, want{curate.OutcomeConform, 1, curate.Bin3, nil}},
		{"the edge of b3", curate.Votes{v(0.9, 0.30)}, want{curate.OutcomeConform, 1, curate.Bin3, nil}},
		{"just below b3 is b4", curate.Votes{v(0.9, 0.31)}, want{curate.OutcomeConform, 1, curate.Bin4, nil}},
		{"against at one half wins over decided", curate.Votes{v(0.9, 0.5)}, want{curate.OutcomeAgainst, 0, "", nil}},
		{"decided below one half finds the anchor silent", curate.Votes{v(0.49, 0.1)}, want{curate.OutcomeUndecided, 0, "", nil}},
		{"the bin is the mean of the conforming answers", curate.Votes{v(0.9, 0.05), v(0.9, 0.15), v(0.9, 0.9)},
			want{curate.OutcomeSplit, 2.0 / 3, curate.Bin2, nil}},
		{"a member without an answer splits a unanimous rest", curate.Votes{v(0.9, 0.05), v(0.9, 0.05), failed},
			want{curate.OutcomeSplit, 2.0 / 3, curate.Bin1, assert.AnError}},
		{"members that all failed are split", curate.Votes{failed, failed}, want{curate.OutcomeSplit, 0, "", assert.AnError}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want.outcome, tc.args.Outcome())
			assert.InDelta(t, tc.want.share, tc.args.Share(), 1e-9)
			assert.Equal(t, tc.want.bin, tc.args.Bin())
			assert.ErrorIs(t, tc.args.Err(), tc.want.err)
		})
	}
}

// The claude member asked alone as a panel asks it
func TestClaudeJudgeReply(t *testing.T) {
	type args struct {
		state  string
		output json.RawMessage
		err    error
		// Whether the backend is reached
		calls int
	}
	type want struct {
		reply classify.Reply
		err   error
	}
	valid := `{"change":"add: x","anchor":["a"]}`
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a case is answered with the model the backend named and the effort asked",
			args{valid, answer(0.8, 0.1), nil, 1},
			want{classify.Reply{Answers: classify.Answers{"decided": {Yes: 0.8}, "against": {Yes: 0.1}}, Model: "claude-sonnet-5-5", Effort: "high"}, nil}},
		{"a state that is no case never reaches the backend",
			args{"the change in words", nil, nil, 0},
			want{classify.Reply{Model: "sonnet", Effort: "high"}, curate.ErrCaseInvalid}},
		{"a failing backend keeps the model asked",
			args{valid, nil, assert.AnError, 1},
			want{classify.Reply{Model: "sonnet", Effort: "high"}, assert.AnError}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).
				Return(llm.Response{Output: tc.args.output, Model: "claude-sonnet-5-5"}, tc.args.err).Times(tc.args.calls)
			members, err := curate.JudgeConfig{}.Next("sonnet", curate.JudgeEffort, nil, start).Members(client, nil)
			require.NoError(t, err)
			judge, ok := members[0].Classifier.(classify.Replier)
			require.True(t, ok)

			got, err := judge.Reply(context.Background(), classify.Request{State: tc.args.state, Questions: curate.JudgeQuestions})

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.reply, got)
		})
	}
}

// A member that a plan asks as a plain classifier answers the same
func TestClaudeJudgeClassify(t *testing.T) {
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: answer(0.8, 0.1)}, nil)
	members, err := curate.JudgeConfig{}.Next("sonnet", curate.JudgeEffort, nil, start).Members(client, nil)
	require.NoError(t, err)

	got, err := members[0].Classifier.Classify(context.Background(), classify.Request{State: `{"change":"add: x"}`, Questions: curate.JudgeQuestions})

	require.NoError(t, err)
	assert.Equal(t, classify.Answers{"decided": {Yes: 0.8}, "against": {Yes: 0.1}}, got)
}

func TestOutcomeValid(t *testing.T) {
	tcs := []struct {
		name string
		args curate.Outcome
		want bool
	}{
		{"every outcome of the set is valid", curate.OutcomeUndecided, true},
		{"a bool is not an outcome", "true", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Valid())
		})
	}
}

func TestBinValid(t *testing.T) {
	tcs := []struct {
		name string
		args curate.Bin
		want bool
	}{
		{"a bin of the set is valid", curate.Bin4, true},
		{"a confidence is not a bin", "0.95", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Valid())
		})
	}
}
