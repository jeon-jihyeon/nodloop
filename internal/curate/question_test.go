package curate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The event of a question the stop pushed in session s1 about the decide d1
func pushed() curate.Event {
	return curate.Event{
		Session: "s1", Ref: "d1", Source: trace.SourcePush,
		Draw: trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "seed", "d1", "audit", 0.15),
	}
}

func TestEventExpose(t *testing.T) {
	change := &knowledge.Ref{ID: "k-1", Version: 1}
	audit := curate.Exposure{
		Kind: curate.QuestionAudit, Key: "a7k2", Change: change, Surface: curate.SurfaceContext, Rules: curate.QuestionRules,
		Choices: []curate.Choice{curate.ChoiceSame, curate.ChoiceUndo}, Text: hmac, Round: 1, Run: "run-1",
	}
	notice := curate.Exposure{Kind: curate.QuestionNotice, Surface: curate.SurfaceMessage, Rules: curate.QuestionRules, Text: hmac, Round: 1}
	with := func(x curate.Exposure, change func(*curate.Exposure)) curate.Exposure {
		x.Choices = append([]curate.Choice{}, x.Choices...)
		change(&x)
		return x
	}
	manual := curate.Event{Session: "s1", Ref: "i-1", Source: trace.SourceManual, Draw: trace.Draw{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d"}}
	type args struct {
		event curate.Event
		x     curate.Exposure
	}
	type want struct {
		input string
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"an audit question names its key, the change, its choices and the run it followed",
			args{pushed(), audit},
			want{`{"kind":"audit","key":"a7k2","change":{"id":"k-1","version":1},"surface":"additional_context","rules":"1",` +
				`"choices":["same","undo"],"text":"` + hmac + `","round":1,"run":"run-1"}`, nil},
		},
		{
			"a notice asks nothing",
			args{pushed(), notice},
			want{`{"kind":"notice","surface":"system_message","rules":"1","text":"` + hmac + `","round":1}`, nil},
		},
		{
			"a proposal the person asked for carries no probability",
			args{manual, with(audit, func(x *curate.Exposure) {
				x.Kind, x.Choices = curate.QuestionDirection, []curate.Choice{"keep", "drop"}
			})},
			want{`{"kind":"direction","key":"a7k2","change":{"id":"k-1","version":1},"surface":"additional_context","rules":"1",` +
				`"choices":["keep","drop"],"text":"` + hmac + `","round":1,"run":"run-1"}`, nil},
		},
		{
			"a question records the runes of its text at the most it may show",
			args{pushed(), with(audit, func(x *curate.Exposure) { x.Runes = curate.MaxRunes })},
			want{`{"kind":"audit","key":"a7k2","change":{"id":"k-1","version":1},"surface":"additional_context","rules":"1",` +
				`"choices":["same","undo"],"text":"` + hmac + `","round":1,"run":"run-1","runes":600}`, nil},
		},
		{"a text past the runes a question may show is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Runes = curate.MaxRunes + 1 })}, want{err: curate.ErrExposeInvalid}},
		{"a negative count of runes is refused", args{pushed(), with(notice, func(x *curate.Exposure) { x.Runes = -1 })}, want{err: curate.ErrExposeInvalid}},
		{"an unknown kind is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Kind = "draft" })}, want{err: curate.ErrExposeInvalid}},
		{"an unknown surface is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Surface = "block" })}, want{err: curate.ErrExposeInvalid}},
		{"a third round is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Round = 3 })}, want{err: curate.ErrExposeInvalid}},
		{"no rules version is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Rules = "" })}, want{err: curate.ErrExposeInvalid}},
		{"a text kept as itself is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Text = "keep it?" })}, want{err: curate.ErrExposeInvalid}},
		{"a notice with choices is refused", args{pushed(), with(notice, func(x *curate.Exposure) { x.Choices = []curate.Choice{"same"} })}, want{err: curate.ErrExposeInvalid}},
		{"a notice in the answer is refused", args{pushed(), with(notice, func(x *curate.Exposure) { x.Surface = curate.SurfaceContext })}, want{err: curate.ErrExposeInvalid}},
		{"a key of 5 characters is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Key = "a7k2x" })}, want{err: curate.ErrExposeInvalid}},
		{"a key of capitals is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Key = "A7K2" })}, want{err: curate.ErrExposeInvalid}},
		{"a question as a system message is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Surface = curate.SurfaceMessage })}, want{err: curate.ErrExposeInvalid}},
		{"one choice is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Choices = x.Choices[:1] })}, want{err: curate.ErrExposeInvalid}},
		{"the same choice twice is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Choices[1] = "same" })}, want{err: curate.ErrExposeInvalid}},
		{"no answer as a choice is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Choices[1] = curate.ChoiceNoAnswer })}, want{err: curate.ErrExposeInvalid}},
		{"a question about nothing is refused", args{curate.Event{Session: "s1", Source: trace.SourcePush, Draw: pushed().Draw}, audit}, want{err: curate.ErrExposeInvalid}},
		{"an expose without its session is refused", args{curate.Event{Ref: "d1", Source: trace.SourcePush, Draw: pushed().Draw}, audit}, want{err: curate.ErrExposeInvalid}},
		{"a pushed question without its probability is refused", args{curate.Event{Session: "s1", Ref: "d1", Source: trace.SourcePush, Draw: manual.Draw}, audit}, want{err: trace.ErrDrawInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.event.Expose(tc.args.x, at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.input, string(got.Input))
		})
	}
}

func TestEventExposeFields(t *testing.T) {
	x := curate.Exposure{Kind: curate.QuestionNotice, Surface: curate.SurfaceMessage, Rules: curate.QuestionRules, Text: hmac, Round: 1}

	got, err := pushed().Expose(x, at)
	require.NoError(t, err)

	assert.Equal(t, []any{trace.NameExpose, "s1", "d1", "notice", trace.SourcePush}, []any{got.Name, got.SessionID, got.Ref, got.Subject, got.Source})
	assert.Empty(t, got.Output)
	assert.NoError(t, schema.Validate(string(trace.NameExpose), got.Record()))
}

func TestEventAnswer(t *testing.T) {
	change := &knowledge.Ref{ID: "k-1", Version: 1}
	answered := pushed()
	answered.Ref = "x1"
	undo := curate.Answer{Choice: curate.ChoiceUndo, Decide: "d1", Change: change, Hidden: true, DelayMS: 4000, Overturn: true, Proof: trace.ProofReply, Person: "ann"}
	wrote := []trace.Written{{ID: "k-1", Version: 1, Status: "retired"}}
	type args struct {
		event  curate.Event
		answer curate.Answer
		wrote  []trace.Written
	}
	type want struct {
		input, output string
		err           error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"an answer of the person names the person and the record it wrote",
			args{answered, undo, wrote},
			want{
				`{"choice":"undo","decide":"d1","change":{"id":"k-1","version":1},"hidden":true,"delay_ms":4000,"overturn":true,` +
					`"authority":"person","proof":"reply","person":"ann"}`,
				`{"records":[{"id":"k-1","version":1,"status":"retired"}]}`, nil,
			},
		},
		{
			"no answer has no proof and wrote nothing",
			args{answered, curate.Answer{Choice: curate.ChoiceNoAnswer, Decide: "d1", Change: change, Hidden: true, Proof: trace.ProofNone}, nil},
			want{
				`{"choice":"no_answer","decide":"d1","change":{"id":"k-1","version":1},"hidden":true,"delay_ms":0,"overturn":false,` +
					`"authority":"agent","proof":"none"}`,
				`{"records":[]}`, nil,
			},
		},
		{"an unknown choice is refused", args{answered, curate.Answer{Choice: "approve", Proof: trace.ProofReply, Person: "ann"}, nil}, want{err: curate.ErrReplyInvalid}},
		{"an unknown proof is refused", args{answered, curate.Answer{Choice: curate.ChoiceSame, Proof: "model", Person: "ann"}, nil}, want{err: curate.ErrReplyInvalid}},
		{
			"an answer of no expose is refused",
			args{curate.Event{Session: "s1", Source: trace.SourcePush, Draw: pushed().Draw}, curate.Answer{Choice: curate.ChoiceSame, Proof: trace.ProofReply, Person: "ann"}, nil},
			want{err: curate.ErrReplyInvalid},
		},
		{
			"an answer without the proof of a person is refused",
			args{answered, curate.Answer{Choice: curate.ChoiceSame, Proof: trace.ProofNone}, nil}, want{err: curate.ErrReplyInvalid},
		},
		{"an answer without the person is refused", args{answered, curate.Answer{Choice: curate.ChoiceSame, Proof: trace.ProofReply}, nil}, want{err: curate.ErrReplyInvalid}},
		{
			"no answer with a proof is refused",
			args{answered, curate.Answer{Choice: curate.ChoiceNoAnswer, Proof: trace.ProofReply}, nil}, want{err: curate.ErrReplyInvalid},
		},
		{
			"no answer that wrote a record is refused",
			args{answered, curate.Answer{Choice: curate.ChoiceNoAnswer, Proof: trace.ProofNone}, wrote}, want{err: curate.ErrReplyInvalid},
		},
		{
			"an answer under a draw of no cycle is refused",
			args{curate.Event{Session: "s1", Ref: "x1", Source: trace.SourcePush}, undo, nil}, want{err: trace.ErrDrawInvalid},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.event.Answer(tc.args.answer, tc.args.wrote, at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want, want{string(got.Input), string(got.Output), tc.want.err})
		})
	}
}

func TestEventAnswerFields(t *testing.T) {
	event := pushed()
	event.Ref = "x1"

	got, err := event.Answer(curate.Answer{Choice: curate.ChoiceSame, Decide: "d1", Proof: trace.ProofReply, Person: "ann"}, nil, at)
	require.NoError(t, err)

	assert.Equal(t, []any{trace.NameAnswer, "s1", "x1", "same", trace.SourcePush}, []any{got.Name, got.SessionID, got.Ref, got.Subject, got.Source})
	assert.NoError(t, schema.Validate(string(trace.NameAnswer), got.Record()))
}
