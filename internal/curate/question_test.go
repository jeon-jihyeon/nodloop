package curate_test

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

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
		Choices: []curate.Choice{curate.ChoiceSame, curate.ChoiceUndo}, Text: hmac, Lines: []string{hmac, hmac}, Round: 1, Run: "run-1",
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
			want{`{"kind":"audit","key":"a7k2","change":{"id":"k-1","version":1},"surface":"additional_context","rules":"3",` +
				`"choices":["same","undo"],"text":"` + hmac + `","lines":["` + hmac + `","` + hmac + `"],"round":1,"run":"run-1"}`, nil},
		},
		{
			"a notice asks nothing",
			args{pushed(), notice},
			want{`{"kind":"notice","surface":"system_message","rules":"3","text":"` + hmac + `","round":1}`, nil},
		},
		{
			"a proposal the person asked for carries no probability",
			args{manual, with(audit, func(x *curate.Exposure) {
				x.Kind, x.Choices = curate.QuestionDirection, []curate.Choice{"keep", "drop"}
			})},
			want{`{"kind":"direction","key":"a7k2","change":{"id":"k-1","version":1},"surface":"additional_context","rules":"3",` +
				`"choices":["keep","drop"],"text":"` + hmac + `","lines":["` + hmac + `","` + hmac + `"],"round":1,"run":"run-1"}`, nil},
		},
		{
			"a question records the runes of its text at the most it may show",
			args{pushed(), with(audit, func(x *curate.Exposure) { x.Runes = curate.MaxRunes })},
			want{`{"kind":"audit","key":"a7k2","change":{"id":"k-1","version":1},"surface":"additional_context","rules":"3",` +
				`"choices":["same","undo"],"text":"` + hmac + `","lines":["` + hmac + `","` + hmac + `"],"round":1,"run":"run-1","runes":600}`, nil},
		},
		{"a text past the runes a question may show is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Runes = curate.MaxRunes + 1 })}, want{err: curate.ErrExposeInvalid}},
		{"a negative count of runes is refused", args{pushed(), with(notice, func(x *curate.Exposure) { x.Runes = -1 })}, want{err: curate.ErrExposeInvalid}},
		{"an unknown kind is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Kind = "draft" })}, want{err: curate.ErrExposeInvalid}},
		{"an unknown surface is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Surface = "block" })}, want{err: curate.ErrExposeInvalid}},
		{"a third round is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Round = 3 })}, want{err: curate.ErrExposeInvalid}},
		{"no rules version is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Rules = "" })}, want{err: curate.ErrExposeInvalid}},
		{"a text kept as itself is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Text = "keep it?" })}, want{err: curate.ErrExposeInvalid}},
		{"a notice with choices is refused", args{pushed(), with(notice, func(x *curate.Exposure) { x.Choices = []curate.Choice{"same"} })}, want{err: curate.ErrExposeInvalid}},
		{"a notice with lines a click holds is refused", args{pushed(), with(notice, func(x *curate.Exposure) { x.Lines = []string{hmac} })}, want{err: curate.ErrExposeInvalid}},
		{"a question with no line a click holds is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Lines = nil })}, want{err: curate.ErrExposeInvalid}},
		{"a line kept as itself is refused", args{pushed(), with(audit, func(x *curate.Exposure) { x.Lines = []string{hmac, "Why you"} })}, want{err: curate.ErrExposeInvalid}},
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

// A reply word names a choice shown whatever its case, the space around it and the punctuation it ends with
func TestExposureNames(t *testing.T) {
	type want struct {
		choice curate.Choice
		named  bool
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"a choice as shown", "keep", want{curate.ChoiceKeep, true}},
		{"a capital, a space and a trailing comma", " Drop, ", want{curate.ChoiceDrop, true}},
		{"a choice the question does not show", "merge", want{curate.ChoiceMerge, false}},
		{"no word", "", want{"", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			x := curate.Exposure{Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}}

			choice, named := x.Names(tc.args)

			assert.Equal(t, tc.want, want{choice, named})
		})
	}
}

func TestEventAnswer(t *testing.T) {
	change := &knowledge.Ref{ID: "k-1", Version: 1}
	answered := pushed()
	answered.Ref = "x1"
	undo := curate.Answer{Choice: curate.ChoiceUndo, Decide: "d1", Change: change, Hidden: true, DelayMS: 4000, Overturn: true, Proof: trace.ProofReply, Person: "ann"}
	wrote := curate.AnswerOutput{Records: []trace.Written{{ID: "k-1", Version: 1, Status: "retired"}}}
	picked := pushed()
	picked.Source, picked.Draw = trace.SourceManual, trace.Draw{CycleID: picked.Draw.CycleID, PolicyVersion: picked.Draw.PolicyVersion}
	mine := curate.Answer{Choice: curate.ChoiceMine, Decide: "d1", Proof: trace.ProofKey, Person: "ann", Refs: []knowledge.Ref{{ID: "k-2", Version: 1}}}
	type args struct {
		event  curate.Event
		answer curate.Answer
		out    curate.AnswerOutput
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
			args{answered, curate.Answer{Choice: curate.ChoiceNoAnswer, Decide: "d1", Change: change, Hidden: true, Proof: trace.ProofNone}, curate.AnswerOutput{}},
			want{
				`{"choice":"no_answer","decide":"d1","change":{"id":"k-1","version":1},"hidden":true,"delay_ms":0,"overturn":false,` +
					`"authority":"agent","proof":"none"}`,
				`{"records":[]}`, nil,
			},
		},
		{
			"an answer to a question the person picked names its decide, the versions a mine took and the intervention a yes carried out",
			args{picked, mine, curate.AnswerOutput{Carried: "i1"}},
			want{
				`{"choice":"mine","decide":"d1","hidden":false,"delay_ms":0,"overturn":false,"authority":"person","proof":"key","person":"ann",` +
					`"refs":[{"id":"k-2","version":1}]}`,
				`{"records":[],"carried":"i1"}`, nil,
			},
		},
		{"an unknown choice is refused", args{answered, curate.Answer{Choice: "approve", Proof: trace.ProofReply, Person: "ann"}, curate.AnswerOutput{}}, want{err: curate.ErrReplyInvalid}},
		{"an unknown proof is refused", args{answered, curate.Answer{Choice: curate.ChoiceSame, Proof: "model", Person: "ann"}, curate.AnswerOutput{}}, want{err: curate.ErrReplyInvalid}},
		{
			"an answer of no expose is refused",
			args{curate.Event{Session: "s1", Source: trace.SourcePush, Draw: pushed().Draw}, curate.Answer{Choice: curate.ChoiceSame, Proof: trace.ProofReply, Person: "ann"}, curate.AnswerOutput{}},
			want{err: curate.ErrReplyInvalid},
		},
		{
			"an answer without the proof of a person is refused",
			args{answered, curate.Answer{Choice: curate.ChoiceSame, Proof: trace.ProofNone}, curate.AnswerOutput{}}, want{err: curate.ErrReplyInvalid},
		},
		{"an answer without the person is refused", args{answered, curate.Answer{Choice: curate.ChoiceSame, Proof: trace.ProofReply}, curate.AnswerOutput{}}, want{err: curate.ErrReplyInvalid}},
		{
			"an answer under the reserved name nodloop is refused",
			args{answered, curate.Answer{Choice: curate.ChoiceSame, Proof: trace.ProofReply, Person: knowledge.ApproverNodloop}, curate.AnswerOutput{}},
			want{err: knowledge.ErrApproverReserved},
		},
		{
			"no answer with a proof is refused",
			args{answered, curate.Answer{Choice: curate.ChoiceNoAnswer, Proof: trace.ProofReply}, curate.AnswerOutput{}}, want{err: curate.ErrReplyInvalid},
		},
		{
			"no answer that wrote a record is refused",
			args{answered, curate.Answer{Choice: curate.ChoiceNoAnswer, Proof: trace.ProofNone}, wrote}, want{err: curate.ErrReplyInvalid},
		},
		{
			"no answer that carried out an intervention is refused",
			args{answered, curate.Answer{Choice: curate.ChoiceNoAnswer, Proof: trace.ProofNone}, curate.AnswerOutput{Carried: "i1"}}, want{err: curate.ErrReplyInvalid},
		},
		{
			"an answer under a draw of no cycle is refused",
			args{curate.Event{Session: "s1", Ref: "x1", Source: trace.SourcePush}, undo, curate.AnswerOutput{}}, want{err: trace.ErrDrawInvalid},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.event.Answer(tc.args.answer, tc.args.out, at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want, want{string(got.Input), string(got.Output), tc.want.err})
		})
	}
}

func TestEventAnswerFields(t *testing.T) {
	event := pushed()
	event.Ref = "x1"

	got, err := event.Answer(curate.Answer{Choice: curate.ChoiceSame, Decide: "d1", Proof: trace.ProofReply, Person: "ann"}, curate.AnswerOutput{}, at)
	require.NoError(t, err)

	assert.Equal(t, []any{trace.NameAnswer, "s1", "x1", "same", trace.SourcePush}, []any{got.Name, got.SessionID, got.Ref, got.Subject, got.Source})
	assert.NoError(t, schema.Validate(string(trace.NameAnswer), got.Record()))
}

// A question reads as the lines of SPEC-curate within 600 runes with no id
// 1. quoted lessons and words go on one line and shrink together until the text fits
// 2. lines that still do not fit are cut before the reply line so it always shows
// 3. a question with nothing else prepared still has its Prepared line
func TestQuestionText(t *testing.T) {
	long := strings.Repeat("run the tests with the race detector ", 20)
	type args struct {
		q curate.Question
	}
	type want struct {
		start, end string
		runes      int
	}
	add := curate.Question{
		Choices: [2]curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Change: "add the lesson %s in repo acme", Why: "it reaches a place you have not decided on",
	}
	with := func(change func(*curate.Question)) curate.Question {
		q := add
		change(&q)
		return q
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the lines with the person's words quoted",
			args{with(func(q *curate.Question) {
				q.Lessons, q.Words, q.Prepared = []string{"Name the\nfailing test first"}, []string{"you skipped it again"}, []string{"no related lesson yet"}
			})},
			want{
				"One decision for you: add the lesson \"Name the failing test first\" in repo acme\nWhy you: it reaches a place you have not decided on\n" +
					"Prepared: your words \"you skipped it again\" · no related lesson yet\nReply keep or drop", "Reply keep or drop", 218,
			},
		},
		{
			"the part corrected quoted after the person's words",
			args{with(func(q *curate.Question) {
				q.Lessons, q.Words = []string{"Name the failing test first"}, []string{"you skipped it again"}
				q.Excerpt, q.Prepared = "go test\n./...", []string{"no related lesson yet"}
			})},
			want{
				"One decision for you: add the lesson \"Name the failing test first\" in repo acme\n" +
					"Why you: it reaches a place you have not decided on\n" +
					"Prepared: your words \"you skipped it again\" · the part corrected \"go test ./...\" · " +
					"no related lesson yet\nReply keep or drop",
				"Reply keep or drop", 255,
			},
		},
		{
			"long quotes shrink together and the reply line shows",
			args{with(func(q *curate.Question) { q.Lessons, q.Words = []string{long, long}, []string{long} })},
			want{"One decision for you: add the lesson \"run the tests", "Reply keep or drop", 586},
		},
		{
			"a long excerpt shrinks with the other quotes",
			args{with(func(q *curate.Question) { q.Lessons, q.Words, q.Excerpt = []string{long, long}, []string{long}, long })},
			want{"One decision for you: add the lesson \"run the tests", "Reply keep or drop", 590},
		},
		{
			"words that do not fit at the shortest quotes are cut before the reply line",
			args{with(func(q *curate.Question) { q.Lessons, q.Words = []string{long}, slices.Repeat([]string{long}, 30) })},
			want{"One decision for you: add the lesson \"run the tests", "…\nReply keep or drop", curate.MaxRunes},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.args.q.Text()

			assert.Equal(t, []any{true, true, tc.want.runes}, []any{strings.HasPrefix(got, tc.want.start), strings.HasSuffix(got, tc.want.end), utf8.RuneCountInString(got)})
			assert.LessOrEqual(t, utf8.RuneCountInString(got), curate.MaxRunes)
		})
	}
}

// The block puts the heading before the question and the instruction to copy its lines into one AskUserQuestion call after it
// with the description nodloop names for each option
func TestQuestionBlock(t *testing.T) {
	q := curate.Question{
		Key: "k7q2", Choices: [2]curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Change: "add the lesson %s in repo acme", Lessons: []string{"Name the failing test first"},
		Why: "it reaches a place you have not decided on", Words: []string{"you skipped it again"},
	}

	got := q.Block()

	assert.Equal(t, "nodloop question for you\n"+q.Text()+"\n"+
		`Ask this in one AskUserQuestion call with header "nodloop k7q2", `+
		`the two options keep with the description "Apply the change" and drop with the description "Leave the change out", `+
		"and as its question the lines from One decision to Prepared copied word for word, keeping every quote. "+
		"If that tool is unavailable, finish this turn without asking a substitute question. The proposal stays pending. "+
		"Record nothing and decide nothing yourself.", got)
}

func TestQuestionLiteralChange(t *testing.T) {
	tcs := []struct{ name, change string }{
		{"a percent in a requested proposal stays literal", `keep the lesson "Show 100% of the result"`},
		{"a formatting token in a requested proposal stays literal", `keep the lesson "Preserve %s in the example"`},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			q := curate.Question{Change: tc.change, Choices: [2]curate.Choice{curate.ChoiceYes, curate.ChoiceNotYet}}
			assert.Contains(t, q.Text(), "One decision for you: "+tc.change)
			assert.NotContains(t, q.Text(), "%!")
		})
	}
}
