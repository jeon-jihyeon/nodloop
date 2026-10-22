package curate_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The HMAC of a text as 64 hex under no key
func hexSeal(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// The person's words that name the runs asked about as a reader of the verdicts would give them
func runWords(_ context.Context, runs []string) ([]string, error) {
	return []string{"words on " + strings.Join(runs, " ")}, nil
}

// The stop of the session in the repo after its run with curate.json as the first pass left it
func (d desk) stopIn(t *testing.T, session, repo string, pushed bool) curate.Asking {
	t.Helper()
	m, err := curate.ReadMark(d.stores.Dir)
	require.NoError(t, err)
	return curate.Asking{Session: session, Run: "run-" + session, Place: *acme(repo), Pushed: pushed, Mark: m, All: d.all(t), Seal: hexSeal, Words: runWords}
}

// The ledger of the bench as a stop reads it
func (b bench) all(t *testing.T) knowledge.Set {
	t.Helper()
	all, err := b.stores.Ledger.All(context.Background())
	require.NoError(t, err)
	return all
}

// What the expose of a stop showed as "kind id round source p" and empty for nothing shown
func exposed(t *testing.T, s curate.Shown) string {
	t.Helper()
	if s.Expose.ID == "" {
		return ""
	}
	var x curate.Exposure
	require.NoError(t, json.Unmarshal(s.Expose.Input, &x))
	id := ""
	if x.Change != nil {
		id = x.Change.ID
	}
	p := "none"
	if s.Expose.Draw.P != nil {
		p = fmt.Sprint(*s.Expose.Draw.P)
	}
	return strings.Join([]string{string(x.Kind), id, fmt.Sprint(x.Round), string(s.Expose.Source), p}, " ")
}

// The choices of the answers of the records oldest first
func (b bench) answers(t *testing.T) []string {
	t.Helper()
	found, err := b.stores.Traces.List(context.Background(), record.TraceFilter{Name: string(trace.NameAnswer)})
	require.NoError(t, err)
	out := []string{}
	for _, tr := range trace.RestoreAll(found) {
		var a curate.Answer
		require.NoError(t, json.Unmarshal(tr.Input, &a))
		out = append([]string{strings.TrimSpace(fmt.Sprintf("%s %s %s %s", a.Choice, a.Proof, tr.Source, a.Person))}, out...)
	}
	return out
}

// The stop asks one question in the order of question kinds
// 1. a direction question that reaches the place before the oldest audit before one shadow decision drawn with its p_select
// 2. one pushed question per session and none in a mode that infers nothing
// 3. a proposal the session made is asked in any mode whatever was asked before
// It is asked in no other session
// 4. a question two other sessions showed with no answer is closed as no_answer and left as it was
func TestIntervenerAsk(t *testing.T) {
	ctx := context.Background()
	type args struct {
		session string
		repo    string
		pushed  bool
		// Records written before the stop
		setup func(t *testing.T, d desk)
	}
	type want struct {
		shown   string
		answers []string
	}
	asked := func(session, repo string) func(t *testing.T, d desk) {
		return func(t *testing.T, d desk) {
			_, err := d.iv.Ask(ctx, d.stopIn(t, session, repo, true))
			require.NoError(t, err)
		}
	}
	twice := func(t *testing.T, d desk) {
		asked("s-1", "svc")(t, d)
		asked("s-2", "svc")(t, d)
	}
	proposed := func(session string) func(t *testing.T, d desk) {
		return func(t *testing.T, d desk) {
			asked("s-new", "svc")(t, d)
			_, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep, Session: session}, agent)
			require.NoError(t, err)
		}
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a direction question that reaches the place comes first", args{"s-new", "svc", true, nil}, want{"direction k-ask 1 push 1", []string{}}},
		{"the oldest audit comes before a shadow decision", args{"s-new", "acme", true, nil}, want{"audit k-dup 1 push 1", []string{}}},
		{
			"a shadow decision is drawn when nothing else is due",
			args{"s-new", "acme", true, func(t *testing.T, d desk) {
				_, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionAnswer, Target: d.decideOf(t, "k-dup"), Choice: trace.ChoiceSame}, person)
				require.NoError(t, err)
			}},
			want{"shadow k-add 1 push 1", []string{"same key manual ann"}},
		},
		{"a place no question reaches asks nothing", args{"s-new", "work", true, nil}, want{"", []string{}}},
		{"one pushed question per session", args{"s-new", "acme", true, asked("s-new", "svc")}, want{"", []string{}}},
		{"a mode that infers nothing pushes nothing", args{"s-new", "svc", false, nil}, want{"", []string{}}},
		{"a later session asks it again in round 2", args{"s-2", "svc", true, asked("s-1", "svc")}, want{"direction k-ask 2 push 1", []string{}}},
		{
			"a question two other sessions showed is closed as no_answer and the next one is asked",
			args{"s-3", "svc", true, twice}, want{"", []string{"no_answer none push"}},
		},
		{
			"a proposal of the session is asked whatever was asked before and with no pushed question",
			args{"s-new", "work", false, proposed("s-new")}, want{"direction k-add 1 manual none", []string{}},
		},
		{"a proposal of another session waits for the stop of that session", args{"s-new", "work", false, proposed("s-other")}, want{"", []string{}}},
		{
			"a dismissed question is never asked",
			args{"s-new", "svc", true, func(t *testing.T, d desk) {
				_, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionDismiss, Target: d.decideOf(t, "k-ask")}, person)
				require.NoError(t, err)
			}},
			want{"", []string{}},
		},
		{"a stop with no session asks nothing", args{"", "svc", true, nil}, want{"", []string{}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			if tc.args.setup != nil {
				tc.args.setup(t, d)
			}

			got, err := d.iv.Ask(ctx, d.stopIn(t, tc.args.session, tc.args.repo, tc.args.pushed))

			require.NoError(t, err)
			assert.Equal(t, tc.want, want{exposed(t, got), d.answers(t)})
		})
	}
}

// The expose of a question names its key, choices, text and run before the stop prints the question
// and the question quotes the person's words on the runs its change cites
func TestIntervenerAskExpose(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)

	got, err := d.iv.Ask(ctx, d.stopIn(t, "s-new", "svc", true))
	require.NoError(t, err)
	again, err := d.iv.Ask(ctx, d.stopIn(t, "s-later", "svc", true))
	require.NoError(t, err)

	var x curate.Exposure
	require.NoError(t, json.Unmarshal(got.Expose.Input, &x))
	text := got.Question.Text()
	assert.Equal(t, "One decision for you: add the lesson \"Use the staging cluster\" in repo svc\n"+
		"Why you: it reaches a place you have not decided on\n"+
		"Prepared: your words \"words on r-k-ask\"\n"+
		"Reply keep or drop", text)
	assert.Regexp(t, regexp.MustCompile(`^[a-z0-9]{4}$`), got.Question.Key)
	lines := []string{
		hexSeal("One decision for you: add the lesson \"Use the staging cluster\" in repo svc"), hexSeal("Why you: it reaches a place you have not decided on"),
		hexSeal("Prepared: your words \"words on r-k-ask\""), hexSeal("nodloop " + got.Question.Key), hexSeal("keep: Apply the change"),
		hexSeal("drop: Leave the change out"),
	}
	assert.Equal(t, curate.Exposure{
		Kind: curate.QuestionDirection, Key: got.Question.Key, Change: &knowledge.Ref{ID: "k-ask", Version: 1}, Surface: curate.SurfaceContext,
		Rules: curate.QuestionRules, Choices: []curate.Choice{curate.ChoiceKeep, curate.ChoiceDrop}, Text: hexSeal(text), Lines: lines, Round: 1,
		Run: "run-s-new", Runes: utf8.RuneCountInString(text),
	}, x)
	assert.Equal(t, []any{"s-new", d.decideOf(t, "k-ask"), trace.SourcePush}, []any{got.Expose.SessionID, got.Expose.Ref, got.Expose.Source})
	assert.Equal(t, got.Question.Key, again.Question.Key, "a question asked again keeps its key")
}

// A direction question whose change cites a run of the session comes before an older one
func TestIntervenerAskOwnSession(t *testing.T) {
	ctx := context.Background()
	type args struct {
		session string
		// Reads of the traces allowed before they fail
		lists int
	}
	type want struct {
		shown string
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the question of a run of the session comes first", args{"s-r-k-dup", -1}, want{"direction k-dup 1 push 1", nil}},
		{"the oldest comes first in any other session", args{"s-new", -1}, want{"direction k-ask 1 push 1", nil}},
		{"runs that cannot be read fail the stop", args{"s-r-k-dup", 6}, want{"", assert.AnError}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			later := d.asking(t, "r-k-dup", curate.Change{Kind: curate.ChangeAdd, ID: "k-dup", Version: 1, Anchor: 1}, curate.RuleScopeWider)
			a := d.stopIn(t, tc.args.session, "acme", true)
			a.Place = knowledge.RunScope{Producer: "session"}
			a.Mark.Due = []curate.Due{
				{Kind: curate.QuestionDirection, Decide: d.decideOf(t, "k-ask"), Change: curate.ChangeAdd, Run: "r-k-ask", At: written},
				{Kind: curate.QuestionDirection, Decide: later, Change: curate.ChangeAdd, Run: "r-k-dup", At: written.Add(time.Hour)},
			}
			got, err := d.flakyIntervener(tc.args.lists, -1).Ask(ctx, a)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.shown, exposed(t, got))
		})
	}
}

// Records that cannot be read or written fail the stop and show nothing
func TestIntervenerAskFails(t *testing.T) {
	ctx := context.Background()
	type args struct {
		repo           string
		lists, appends int
		// Whether the answers and words fail
		words bool
	}
	tcs := []struct {
		name string
		args args
	}{
		{"traces that cannot be read", args{"svc", 0, -1, false}},
		{"an expose that cannot be appended", args{"svc", -1, 0, false}},
		{"words that cannot be read", args{"svc", -1, -1, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			iv := d.flakyIntervener(tc.args.lists, tc.args.appends)
			a := d.stopIn(t, "s-new", tc.args.repo, true)
			if tc.args.words {
				a.Words = func(context.Context, []string) ([]string, error) { return nil, assert.AnError }
			}

			got, err := iv.Ask(ctx, a)

			assert.ErrorIs(t, err, assert.AnError)
			assert.Empty(t, exposed(t, got))
		})
	}
}

// A question two other sessions showed whose no_answer cannot be appended fails the stop
func TestIntervenerAskUnanswered(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	for _, session := range []string{"s-1", "s-2"} {
		_, err := d.iv.Ask(ctx, d.stopIn(t, session, "svc", true))
		require.NoError(t, err)
	}
	got, err := d.flakyIntervener(-1, 0).Ask(ctx, d.stopIn(t, "s-3", "svc", true))

	assert.ErrorIs(t, err, assert.AnError)
	assert.Empty(t, exposed(t, got))
}

// The intervener of the bench whose trace lists fail after the allowed ones and whose appends after theirs
// -1 fails none
func (b bench) flakyIntervener(lists, appends int) *curate.Intervener {
	reads := func(n int) testkit.Reads {
		if n < 0 {
			return testkit.Reads{}
		}
		return testkit.Reads{Allowed: n, Err: assert.AnError}
	}
	traces := &testkit.FlakyTraces{Store: b.stores.Traces, Lists: reads(lists), Appends: reads(appends)}
	return curate.NewIntervener(b.stores.Ledger, traces, curate.InterveneSetting{Build: "0.10.0"}, b.stores.Clock.Now)
}

// The person's own reply answers the question shown after the answer they read
// 1. the first word typed with case and trailing punctuation ignored or the label clicked under the key of the header
// 2. the answer names the expose with its source and draw and proof reply
// The ledger record names the answer
// 3. an input that names no choice of a question shown after that answer matches nothing
// So does a click that misses a line the stop showed or an option description nodloop named
// A click whose lines hold more text or more space still answers
// 4. a question closed already matches and records nothing
func TestIntervenerReply(t *testing.T) {
	ctx := context.Background()
	type args struct {
		reply func(q curate.Question) curate.Reply
		// Answers before the reply
		setup func(t *testing.T, d desk)
	}
	type want struct {
		matched  bool
		choice   curate.Choice
		answered bool
		answers  []string
		statuses []string
		err      error
	}
	word := func(run, w string) func(curate.Question) curate.Reply {
		return func(curate.Question) curate.Reply {
			return curate.Reply{Session: "s-new", Run: run, Word: w, Person: "ann"}
		}
	}
	// The lines up to Prepared that the AskUserQuestion call copies word for word
	lines := func(q curate.Question) string {
		before, _, _ := strings.Cut(q.Text(), "\nReply ")
		return before
	}
	// The options as the block names them
	named := []curate.Option{{Label: "keep", Description: "Apply the change"}, {Label: "drop", Description: "Leave the change out"}}
	type click struct {
		question func(curate.Question) string
		options  []curate.Option
		seal     func(string) string
	}
	clickOn := func(c click, label string) func(curate.Question) curate.Reply {
		return func(q curate.Question) curate.Reply {
			return curate.Reply{Session: "s-new", Key: q.Key, Question: c.question(q), Options: c.options, Seal: c.seal, Word: label, Person: "ann"}
		}
	}
	copied := click{lines, named, hexSeal}
	worded := func(curate.Question) string { return "Drop the lesson?" }
	// The lines with their space doubled and a line of the model after them
	wrapped := func(q curate.Question) string {
		return "  " + strings.ReplaceAll(lines(q), " ", "  ") + "\n\nShould this lesson be kept or dropped?"
	}
	// The lines joined into one with periods as Haiku joined them
	joined := func(q curate.Question) string { return strings.ReplaceAll(lines(q), "\n", ". ") }
	capitals := []curate.Option{{Label: "Keep", Description: "Apply  the change"}, {Label: "Drop", Description: "Leave the change out"}}
	described := []curate.Option{{Label: "keep", Description: "Keep the lesson"}, {Label: "drop", Description: "Leave the change out"}}
	swapped := []curate.Option{{Label: "keep", Description: "Leave the change out"}, {Label: "drop", Description: "Apply the change"}}
	waiting := []string{"k-add v1 candidate", "k-ask v1 candidate", "k-carried v1 approved nodloop", "k-dup v1 retired nodloop duplicate"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a reply word after the answer the question followed keeps the change under the person",
			args{word("run-s-new", "Keep."), nil},
			want{true, curate.ChoiceKeep, true, []string{"keep reply push ann"},
				[]string{"k-add v1 candidate", "k-ask v1 approved ann", "k-carried v1 approved nodloop", "k-dup v1 retired nodloop duplicate"}, nil},
		},
		{
			"a click under the key of its header drops it under the person",
			args{clickOn(copied, "Drop"), nil},
			want{true, curate.ChoiceDrop, true, []string{"drop reply push ann"},
				[]string{"k-add v1 candidate", "k-ask v1 retired ann person", "k-carried v1 approved nodloop", "k-dup v1 retired nodloop duplicate"}, nil},
		},
		{"a word after another answer answers nothing", args{word("run-other", "keep"), nil}, want{false, "", false, []string{}, waiting, nil}},
		{"a word that is no choice answers nothing", args{word("run-s-new", "maybe"), nil}, want{false, "", false, []string{}, waiting, nil}},
		{
			"a click whose lines hold more space and a line of the model drops it under the person",
			args{clickOn(click{wrapped, capitals, hexSeal}, "drop"), nil},
			want{true, curate.ChoiceDrop, true, []string{"drop reply push ann"},
				[]string{"k-add v1 candidate", "k-ask v1 retired ann person", "k-carried v1 approved nodloop", "k-dup v1 retired nodloop duplicate"}, nil},
		},
		{"a label that is no choice answers nothing", args{clickOn(copied, "Other"), nil}, want{false, "", false, []string{}, waiting, nil}},
		{
			"a click on a question a model worded itself under the key of the header answers nothing",
			args{clickOn(click{worded, named, hexSeal}, "drop"), nil},
			want{false, "", false, []string{}, waiting, nil},
		},
		{"a click whose lines a model joined into one answers nothing", args{clickOn(click{joined, named, hexSeal}, "drop"), nil}, want{false, "", false, []string{}, waiting, nil}},
		{"a click whose option a model described itself answers nothing", args{clickOn(click{lines, described, hexSeal}, "drop"), nil}, want{false, "", false, []string{}, waiting, nil}},
		{"a click whose descriptions are swapped answers nothing", args{clickOn(click{lines, swapped, hexSeal}, "drop"), nil}, want{false, "", false, []string{}, waiting, nil}},
		{"a click with no option answers nothing", args{clickOn(click{lines, nil, hexSeal}, "drop"), nil}, want{false, "", false, []string{}, waiting, nil}},
		{"a click checked with no seal answers nothing", args{clickOn(click{lines, named, nil}, "drop"), nil}, want{false, "", false, []string{}, waiting, nil}},
		{
			"a question answered already matches and records nothing more",
			args{word("run-s-new", "drop"), func(t *testing.T, d desk) {
				_, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionDismiss, Target: d.decideOf(t, "k-ask")}, person)
				require.NoError(t, err)
			}},
			want{true, curate.ChoiceDrop, false, []string{}, waiting, nil},
		},
		{
			"the reserved name nodloop records nothing",
			args{func(curate.Question) curate.Reply {
				return curate.Reply{Session: "s-new", Run: "run-s-new", Word: "keep", Person: knowledge.ApproverNodloop}
			}, nil},
			want{false, "", false, []string{}, waiting, knowledge.ErrApproverReserved},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			shown, err := d.iv.Ask(ctx, d.stopIn(t, "s-new", "svc", true))
			require.NoError(t, err)
			if tc.args.setup != nil {
				tc.args.setup(t, d)
			}

			got, err := d.iv.Reply(ctx, tc.args.reply(shown.Question))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want, want{got.Matched, got.Choice, got.Answer != "", d.answers(t), statuses(t, d.bench), tc.want.err})
		})
	}
}

// The answer of a reply names the expose and takes its draw and the ledger record it wrote names the answer
func TestIntervenerReplyRecords(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	shown, err := d.iv.Ask(ctx, d.stopIn(t, "s-new", "svc", true))
	require.NoError(t, err)

	got, err := d.iv.Reply(ctx, curate.Reply{Session: "s-new", Run: "run-s-new", Word: "keep", Person: "ann"})
	require.NoError(t, err)

	answer, err := d.stores.Traces.Get(ctx, got.Answer)
	require.NoError(t, err)
	tr := trace.Restore(answer)
	assert.Equal(t, []any{shown.Expose.ID, "s-new", trace.SourcePush, shown.Expose.Draw}, []any{tr.Ref, tr.SessionID, tr.Source, tr.Draw})
	assert.Equal(t, got.Answer, d.decision(t, "k-ask", 1))
	assert.Empty(t, d.interventions(t), "a pushed answer adds no intervention")
}

// A reply reads the exposes of its session first and the rest of the records only for an input that names a choice of one
func TestIntervenerReplyUnreadable(t *testing.T) {
	ctx := context.Background()
	type args struct {
		// Lists of traces that pass before every later one fails
		lists int
		word  string
	}
	type want struct {
		matched bool
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"exposes that cannot be read fail the reply", args{0, "keep"}, want{false, assert.AnError}},
		{"records that cannot be read after the exposes fail a reply that names a choice", args{1, "keep"}, want{false, assert.AnError}},
		{"an input that names no choice reads no record past the exposes", args{1, "maybe"}, want{false, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			_, err := d.iv.Ask(ctx, d.stopIn(t, "s-new", "svc", true))
			require.NoError(t, err)

			got, err := d.flakyIntervener(tc.args.lists, -1).Reply(ctx, curate.Reply{Session: "s-new", Run: "run-s-new", Word: tc.args.word, Person: "ann"})

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.matched, got.Matched)
		})
	}
}

// The notice names what curate carried out in the place once per session and only what came after the last notice there
func TestIntervenerNotice(t *testing.T) {
	ctx := context.Background()
	type args struct {
		session, repo string
		// Records written before the stop
		setup func(t *testing.T, b bench, iv *curate.Intervener)
		// Whether the mark names no cycle
		noCycle bool
	}
	noticed := func(session string) func(t *testing.T, b bench, iv *curate.Intervener) {
		return func(t *testing.T, b bench, iv *curate.Intervener) {
			b.correct(t, "run-"+session, trace.Labels{"repo": {"work"}}, nil)
			_, err := iv.Notice(ctx, b.noticeStop(t, session, "work"))
			require.NoError(t, err)
		}
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{
			"a lesson nodloop approved in the place is named once",
			args{"s-new", "work", nil, false},
			`From your earlier decisions nodloop applied a lesson here: "Squash before a merge". /nodloop:nod shows them and takes any back`,
		},
		{
			"a withdrawal is named beside an approval",
			args{"s-new", "work", func(t *testing.T, b bench, _ *curate.Intervener) {
				withdrawn := lesson("k-gone", 1, "Rebase before a push", acme("work"), knowledge.StatusRetired, knowledge.ApproverNodloop)
				withdrawn.Reason, withdrawn.Decision, withdrawn.Time = knowledge.ReasonWords, "d-gone", written.Add(time.Minute)
				require.NoError(t, testkit.Err(b.stores.Ledger.Import(ctx, []knowledge.Knowledge{withdrawn})))
			}, false},
			`From your earlier decisions nodloop applied a lesson and withdrew a lesson here: "Rebase before a push" · "Squash before a merge". ` +
				`/nodloop:nod shows them and takes any back`,
		},
		{"a session that had a notice gets none", args{"s-new", "work", noticed("s-new"), false}, ""},
		{"a later session of the place gets none for what it was told", args{"s-later", "work", noticed("s-new"), false}, ""},
		{"a place nothing reaches gets none", args{"s-new", "acme", nil, false}, ""},
		{"no cycle gets none", args{"s-new", "work", nil, true}, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := noticeBench(t)
			iv := b.intervener(nil)
			if tc.args.setup != nil {
				tc.args.setup(t, b, iv)
			}
			a := b.noticeStop(t, tc.args.session, tc.args.repo)
			if tc.args.noCycle {
				a.Mark.CycleID = ""
			}

			got, err := iv.Notice(ctx, a)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Notice)
			assert.Equal(t, tc.want != "", got.Expose.ID != "")
		})
	}
}

// A bench whose ledger holds a lesson nodloop approved in repo work through a decide
func noticeBench(t *testing.T) bench {
	t.Helper()
	carried := lesson("k-carried", 1, "Squash before a merge", acme("work"), knowledge.StatusApproved, knowledge.ApproverNodloop)
	carried.Decision = "d-carried"
	return newBench(t, nil, []knowledge.Knowledge{carried}, nil)
}

// The stop of the session in the repo of a cycle with its run recorded in that repo
func (b bench) noticeStop(t *testing.T, session, repo string) curate.Asking {
	t.Helper()
	b.correct(t, "run-"+session, trace.Labels{"repo": {repo}}, nil)
	return curate.Asking{
		Session: session, Run: "run-" + session, Place: *acme(repo), Pushed: true, Seal: hexSeal, All: b.all(t),
		Mark: curate.Mark{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d", Seed: "5eed"},
	}
}

// The expose of a notice is one line on the system message that asks nothing and records its runes
func TestIntervenerNoticeExpose(t *testing.T) {
	b := noticeBench(t)

	got, err := b.intervener(nil).Notice(context.Background(), b.noticeStop(t, "s-new", "work"))
	require.NoError(t, err)

	var x curate.Exposure
	require.NoError(t, json.Unmarshal(got.Expose.Input, &x))
	assert.Equal(t, curate.Exposure{
		Kind: curate.QuestionNotice, Surface: curate.SurfaceMessage, Rules: curate.QuestionRules, Text: hexSeal(got.Notice), Round: 1, Run: "run-s-new",
		Runes: utf8.RuneCountInString(got.Notice),
	}, x)
	assert.Equal(t, []any{"s-new", trace.SourcePush, 1.0}, []any{got.Expose.SessionID, got.Expose.Source, *got.Expose.Draw.P})
}

// Records that cannot be read fail the notice and show nothing
func TestIntervenerNoticeFails(t *testing.T) {
	ctx := context.Background()
	type args struct {
		lists, appends int
	}
	tcs := []struct {
		name string
		args args
	}{
		{"exposes that cannot be read", args{0, -1}},
		{"knob events that cannot be read", args{1, -1}},
		{"a notice that cannot be appended", args{-1, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := noticeBench(t)
			a := b.noticeStop(t, "s-new", "work")

			got, err := b.flakyIntervener(tc.args.lists, tc.args.appends).Notice(ctx, a)

			assert.ErrorIs(t, err, assert.AnError)
			assert.Empty(t, got.Notice)
		})
	}
}

// A notice whose earlier notice names a run that is gone counts that notice as one of another place
func TestIntervenerNoticeRunGone(t *testing.T) {
	ctx := context.Background()
	b := noticeBench(t)
	iv := b.intervener(nil)
	gone := b.noticeStop(t, "s-old", "work")
	gone.Run = "run-pruned"
	_, err := iv.Notice(ctx, gone)
	require.NoError(t, err)

	got, err := iv.Notice(ctx, b.noticeStop(t, "s-new", "work"))

	require.NoError(t, err)
	assert.NotEmpty(t, got.Notice)
}

// Each kind of question names its change, why it needs the person, what was prepared and the two choices of its decide
func TestIntervenerAskText(t *testing.T) {
	ctx := context.Background()
	type args struct {
		kind curate.QuestionKind
		// The decide the question asks about appended to the desk
		decide func(t *testing.T, d desk) string
	}
	asking := func(ref string, c curate.Change, rule curate.RuleCode) func(*testing.T, desk) string {
		return func(t *testing.T, d desk) string { return d.asking(t, ref, c, rule) }
	}
	first := func(id string) func(*testing.T, desk) string {
		return func(t *testing.T, d desk) string { return d.decideOf(t, id) }
	}
	add := curate.Change{Kind: curate.ChangeAdd, ID: "k-add", Version: 1, Anchor: 1}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{
			"an audit says what nodloop carried out",
			args{curate.QuestionAudit, first("k-dup")},
			"One decision for you: nodloop dropped the lesson \"Run the tests before a commit\" since a lesson already says it\n" +
				"Why you: nodloop checks a share of what it carried out with you\nPrepared: your words \"words on r-k-dup\"\nReply same or undo",
		},
		{
			"a shadow decision asks as a direction question with nothing about the judge",
			args{curate.QuestionShadow, first("k-add")},
			"One decision for you: add the lesson \"Name the failing test first\" in repo acme\n" +
				"Why you: nodloop asks you before it carries out such changes on its own\nPrepared: your words \"words on r-k-add\"\nReply keep or drop",
		},
		{
			"an update replaces the lesson the person approved",
			args{curate.QuestionDirection, asking("r-k-add", curate.Change{Kind: curate.ChangeUpdate, ID: "k-person", Version: 1, Anchor: 1}, curate.RulePersonItem)},
			"One decision for you: replace the lesson \"Run the tests before a commit\" with \"Run the tests before a commit\" in repo acme\n" +
				"Why you: it changes a lesson you approved\nPrepared: your words \"words on r-k-person\"\nReply keep or drop",
		},
		{
			"the person's words asking to drop their lesson ask to retire it",
			args{curate.QuestionDirection, asking("r-k-ask", curate.Change{Kind: curate.ChangeWithdraw, ID: "k-person", Version: 1, Signal: "c-1", Anchor: 1},
				curate.RulePersonItem)},
			"One decision for you: retire the lesson \"Run the tests before a commit\" you approved\n" +
				"Why you: it changes a lesson you approved\nPrepared: your words \"words on r-k-ask\"\nReply keep or drop",
		},
		{
			"a change that needs room merges first",
			args{curate.QuestionDirection, asking("r-k-add", add, curate.RuleRoom)},
			"One decision for you: add the lesson \"Name the failing test first\" in repo acme after merging lessons to make room\n" +
				"Why you: it needs room, so lessons would be merged\nPrepared: your words \"words on r-k-add\" · the merge waits for its coverage check\n" +
				"Reply merge or separate",
		},
		{
			"a compaction asks to approve the merge",
			args{curate.QuestionDirection, asking("r-k-add", curate.Change{Kind: curate.ChangeCompaction, Compaction: "c-gone", Anchor: 1}, curate.RulePersonItem)},
			"One decision for you: approve the merge of lessons that was prepared\nWhy you: it changes a lesson you approved\n" +
				"Prepared: the merge waits for its coverage check\nReply merge or separate",
		},
		{
			"drift asks to take the lessons nodloop added as the person's own",
			args{curate.QuestionDirection, asking("r-k-add", add, curate.RuleDrift)},
			"One decision for you: take the lessons nodloop added in this place as your own\n" +
				"Why you: lessons nodloop added here outnumber the ones you confirmed\nPrepared: your words \"words on r-k-add\" · /nodloop:nod shows them first\n" +
				"Reply mine or review",
		},
		{
			"a reanchor asks to take the lessons nodloop added as the person's own",
			args{curate.QuestionDirection, asking("r-k-add", curate.Change{Kind: curate.ChangeReanchor, Anchor: 1}, "")},
			"One decision for you: take the lessons nodloop added in this place as your own\n" +
				"Why you: lessons nodloop added here outnumber the ones you confirmed\nPrepared: /nodloop:nod shows them first\nReply mine or review",
		},
		{
			"a pause sends the change to the person",
			args{curate.QuestionDirection, asking("r-k-add", add, curate.RulePaused)},
			"One decision for you: add the lesson \"Name the failing test first\" in repo acme\nWhy you: you paused what nodloop carries out here\n" +
				"Prepared: your words \"words on r-k-add\"\nReply keep or drop",
		},
		{
			"a closed kind is a change the recorded decisions do not settle",
			args{curate.QuestionDirection, asking("r-k-add", add, curate.RuleKindClosed)},
			"One decision for you: add the lesson \"Name the failing test first\" in repo acme\nWhy you: your recorded decisions do not settle it\n" +
				"Prepared: your words \"words on r-k-add\"\nReply keep or drop",
		},
		{
			"a widening asks to let nodloop carry out one more kind and bin",
			args{curate.QuestionDirection, asking("", curate.Change{Kind: curate.ChangeWiden, Opens: &curate.Opening{Kind: curate.ChangeAdd, Bin: curate.Bin2}, Anchor: 1}, "")},
			"One decision for you: let nodloop carry out new lessons it is quite sure of without asking you\n" +
				"Why you: only your yes lets nodloop carry out more on its own\nPrepared: your answers on such changes agreed with what nodloop would have done\n" +
				"Reply yes or notyet",
		},
		{
			"the activation names what the anchor holds",
			args{curate.QuestionDirection, func(t *testing.T, d desk) string {
				tr, err := curate.AnchorVersion{Version: 2, Refs: []knowledge.Ref{{ID: "k-person", Version: 1}}, Cause: curate.CauseFiles}.Trace(written)
				require.NoError(t, err)
				require.NoError(t, d.stores.Traces.Append(ctx, tr.Record()))
				return d.asking(t, "", curate.Change{Kind: curate.ChangeActivate, Anchor: 1}, "")
			}},
			"One decision for you: let lessons nodloop approves from your earlier decisions reach your sessions\n" +
				"Why you: until you say yes nothing reaches a run without your approval\n" +
				"Prepared: your record holds 1 lesson you approved and 0 files you set as direction · /nodloop:nod shows them first\nReply yes or notyet",
		},
		{
			"a proposal names the payload in plain words",
			args{curate.QuestionDirection, func(t *testing.T, d desk) string {
				p, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceUndo, Session: "s-new"}, agent)
				require.NoError(t, err)
				return p.Decide
			}},
			"One decision for you: undo the lesson \"Name the failing test first\"\nWhy you: an agent proposed it and only you decide it\n" +
				"Prepared: your words \"words on r-k-add\" · asked for in this conversation\nReply yes or notyet",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			decide := tc.args.decide(t, d)
			a := d.stopIn(t, "s-new", "acme", true)
			a.Mark.Due = []curate.Due{{Kind: tc.args.kind, Decide: decide, Session: "s-new", At: written}}

			got, err := d.iv.Ask(ctx, a)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Question.Text())
		})
	}
}

// A question about a prepared merge says whether its coverage check passed
func TestIntervenerAskCoverage(t *testing.T) {
	ctx := context.Background()
	approved := knowledge.StatusApproved
	type args struct {
		coverage func(b bench) func(context.Context, string) (knowledge.Coverage, error)
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"a check that passed", args{func(b bench) func(context.Context, string) (knowledge.Coverage, error) { return b.coverage }}, "coverage passed, nothing lost"},
		{"no check at hand", args{func(bench) func(context.Context, string) (knowledge.Coverage, error) { return nil }}, "the merge waits for its coverage check"},
		{
			"a check that cannot be read",
			args{func(bench) func(context.Context, string) (knowledge.Coverage, error) {
				return func(context.Context, string) (knowledge.Coverage, error) { return knowledge.Coverage{}, assert.AnError }
			}},
			"the merge waits for its coverage check",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newBench(t, nil, []knowledge.Knowledge{
				lesson("k-a", 1, "rule a", acme("acme"), approved, "ann"), lesson("k-b", 1, "rule b", acme("acme"), approved, "ann"),
			}, nil)
			c, err := b.stores.Ledger.ProposeCompaction(ctx, "k-a", []knowledge.Knowledge{{
				Kind: knowledge.KindMeaning, Content: "rule a and rule b", Run: acme("acme"), Author: "claude",
				Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-a", Version: 1}, {ID: "k-b", Version: 1}, {ID: "k-person", Version: 1}}},
			}})
			require.NoError(t, err)
			id := trace.NewID(written)
			decide, err := curate.Event{ID: id, Ref: "r-k-a", Source: trace.SourcePush, Draw: trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "5eed", id, "audit", 0)}.
				Decide(curate.Change{Kind: curate.ChangeCompaction, Compaction: c.ID, Anchor: 1}, curate.Decision{Tier: 2, RuleCode: curate.RulePersonItem, Action: curate.ActionAsk}, nil, written)
			require.NoError(t, err)
			require.NoError(t, b.stores.Traces.Append(ctx, decide.Record()))
			iv := curate.NewIntervener(b.stores.Ledger, b.stores.Traces, curate.InterveneSetting{Coverage: tc.args.coverage(b)}, b.stores.Clock.Now)
			a := b.noticeStop(t, "s-new", "acme")
			a.Mark.Due = []curate.Due{{Kind: curate.QuestionDirection, Decide: id, Change: curate.ChangeCompaction, At: written}}

			got, err := iv.Ask(ctx, a)

			require.NoError(t, err)
			assert.Equal(t, []string{tc.want}, got.Question.Prepared)
		})
	}
}

// Appends a decide of the cycle of the first pass that carried out the change with the decision and drew it for audit
func (d desk) carried(t *testing.T, ref string, c curate.Change, decision curate.Decision) string {
	t.Helper()
	now := d.stores.Clock.Now()
	id := trace.NewID(now)
	tr, err := curate.Event{ID: id, Ref: ref, Source: trace.SourcePush, Draw: d.first.Event.AuditDraw(id, 1)}.Decide(c, decision, nil, now)
	require.NoError(t, err)
	require.NoError(t, d.stores.Traces.Append(context.Background(), tr.Record()))
	return id
}

// Imports a waiting lesson of the scope into the desk and appends a decide that asks about it
func (d desk) scoped(t *testing.T, scope *knowledge.RunScope) string {
	t.Helper()
	k := lesson("k-scoped", 1, "Use the scoped rule", scope, knowledge.StatusCandidate, "")
	require.NoError(t, testkit.Err(d.stores.Ledger.Import(context.Background(), []knowledge.Knowledge{k})))
	return d.asking(t, "r-k-add", curate.Change{Kind: curate.ChangeAdd, ID: "k-scoped", Version: 1, Anchor: 1}, curate.RuleScopeWider)
}

// The line of a change names what curate carried out and where a lesson reaches in plain words
func TestIntervenerAskChange(t *testing.T) {
	ctx := context.Background()
	type args struct {
		kind   curate.QuestionKind
		decide func(t *testing.T, d desk) string
		// Whether the stop reads the person's words
		words bool
	}
	audited := func(c curate.Change, decision curate.Decision) func(*testing.T, desk) string {
		return func(t *testing.T, d desk) string { return d.carried(t, "r-k-add", c, decision) }
	}
	scoped := func(labels trace.Labels) func(*testing.T, desk) string {
		return func(t *testing.T, d desk) string {
			return d.scoped(t, &knowledge.RunScope{Producer: "session", Labels: labels})
		}
	}
	add := curate.Change{Kind: curate.ChangeAdd, ID: "k-add", Version: 1, Anchor: 1}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{
			"an audit of an approval", args{curate.QuestionAudit, audited(add, curate.Decision{Tier: 1, Action: curate.ActionApply}), true},
			"nodloop added the lesson \"Name the failing test first\" in repo acme from your earlier decisions",
		},
		{
			"an audit of a withdrawal on the person's words",
			args{curate.QuestionAudit, audited(curate.Change{Kind: curate.ChangeWithdraw, ID: "k-carried", Version: 1, Signal: "c-1", Anchor: 1}, curate.Decision{
				Tier: 1, Action: curate.ActionWithdraw, Reason: knowledge.ReasonWords, Failure: curate.FailureUnderexplored, Reopen: curate.ReopenOtherSession,
			}), true},
			"nodloop withdrew the lesson \"Squash before a merge\" since you asked to stop following it",
		},
		{
			"an audit of a join", args{curate.QuestionAudit, audited(curate.Change{Kind: curate.ChangeJoin, ID: "k-add", Version: 1, Old: []knowledge.Ref{{ID: "k-ask", Version: 1}}, Anchor: 1},
				curate.Decision{Action: curate.ActionJoin, Reason: knowledge.ReasonDuplicate, Failure: curate.FailureHard, Reopen: curate.ReopenPersonOnly}), true},
			"nodloop joined the lesson \"Name the failing test first\" to an older one that says the same",
		},
		{
			"an audit of a reason with no words of its own", args{curate.QuestionAudit, audited(add, curate.Decision{
				Action: curate.ActionDrop, Reason: knowledge.ReasonPerson, Failure: curate.FailureHard, Reopen: curate.ReopenPersonOnly,
			}), true},
			"nodloop dropped the lesson \"Name the failing test first\" since your earlier decisions settled it",
		},
		{
			"a widening of the withdrawals names no bin",
			args{curate.QuestionDirection, func(t *testing.T, d desk) string {
				return d.asking(t, "", curate.Change{Kind: curate.ChangeWiden, Opens: &curate.Opening{Kind: curate.ChangeWithdraw}, Anchor: 1}, "")
			}, true},
			"let nodloop carry out withdrawals of lessons it approved without asking you",
		},
		{
			"a proposal the records no longer hold",
			args{curate.QuestionDirection, func(t *testing.T, d desk) string {
				return d.asking(t, "", curate.Change{Kind: curate.ChangeIntervention, Proposal: "i-gone", Anchor: 1}, curate.RuleProposal)
			}, true},
			"carry out what an agent proposed",
		},
		{"a dir of its own", args{curate.QuestionDirection, scoped(trace.Labels{"dir": {"sub"}}), false}, "add the lesson \"Use the scoped rule\" in dir sub"},
		{"the root dir of a repo goes without saying", args{curate.QuestionDirection, scoped(trace.Labels{"repo": {"acme"}, "dir": {"."}}), false},
			"add the lesson \"Use the scoped rule\" in repo acme"},
		{"the root dir alone is named", args{curate.QuestionDirection, scoped(trace.Labels{"dir": {"."}}), false}, "add the lesson \"Use the scoped rule\" in dir ."},
		{"a scope with no label reaches everywhere", args{curate.QuestionDirection, scoped(nil), false}, "add the lesson \"Use the scoped rule\" everywhere"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			decide := tc.args.decide(t, d)
			a := d.stopIn(t, "s-new", "acme", true)
			a.Mark.Due = []curate.Due{{Kind: tc.args.kind, Decide: decide, At: written}}
			if !tc.args.words {
				a.Words = nil
			}

			got, err := d.iv.Ask(ctx, a)

			require.NoError(t, err)
			assert.Equal(t, tc.want, fmt.Sprintf(got.Question.Change, anyOf(got.Question.Lessons)...))
		})
	}
}

// The lessons quoted as a change line quotes them
func anyOf(lessons []string) []any {
	out := []any{}
	for _, l := range lessons {
		out = append(out, `"`+l+`"`)
	}
	return out
}

// A question about a version quotes the part of the answer the extraction that drafted it kept
// 1. an extraction the store lacks or that kept none or does not decode quotes nothing and the question still asks
// 2. so does a version no extraction drafted
// 3. a withdrawal quotes the words of its signal and no excerpt
// 4. a failed read of the extraction fails the stop
func TestIntervenerAskExcerpt(t *testing.T) {
	ctx := context.Background()
	type args struct {
		// The extraction the version names
		extract string
		kind    curate.ChangeKind
		gets    testkit.Reads
	}
	type want struct {
		excerpt string
		quoted  bool
		err     error
	}
	add, withdraw := curate.ChangeAdd, curate.ChangeWithdraw
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an extraction that kept an excerpt", args{"x-kept", add, testkit.Reads{Allowed: 9}},
			want{"cd repo && git status", true, nil}},
		{"an extraction that kept none", args{"x-none", add, testkit.Reads{Allowed: 9}}, want{"", false, nil}},
		{"an extraction the store lacks", args{"x-gone", add, testkit.Reads{Allowed: 9}}, want{"", false, nil}},
		{"an extraction whose output does not decode", args{"x-text", add, testkit.Reads{Allowed: 9}}, want{"", false, nil}},
		{"a trace of another kind", args{"x-other", add, testkit.Reads{Allowed: 9}}, want{"", false, nil}},
		{"a version no extraction drafted", args{"", add, testkit.Reads{Allowed: 9}}, want{"", false, nil}},
		{"a withdrawal", args{"x-kept", withdraw, testkit.Reads{Allowed: 9}}, want{"", false, nil}},
		{"a failed read of the extraction", args{"x-kept", add, testkit.Reads{Err: assert.AnError}}, want{"",
			false, assert.AnError}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			k := lesson("k-quoted", 1, "Run git with -C", acme("acme"), knowledge.StatusCandidate, "")
			k.Extract = tc.args.extract
			require.NoError(t, testkit.Err(d.stores.Ledger.Import(ctx, []knowledge.Knowledge{k})))
			for id, out := range map[string]string{
				"x-kept": `{"conclusion":"proposed","excerpt":"cd repo && git status"}`,
				"x-none": `{"conclusion":"proposed"}`, "x-text": `"text"`,
			} {
				x := trace.Trace{ID: id, Name: trace.NameExtract, Subject: "model", Ref: "r-k-quoted", Time: written,
					Output: json.RawMessage(out)}
				require.NoError(t, d.stores.Traces.Append(ctx, x.Record()))
			}
			other := trace.Trace{
				ID: "x-other", Name: trace.NameRun, Time: written,
				Output: json.RawMessage(`{"excerpt":"a different record"}`),
			}
			require.NoError(t, d.stores.Traces.Append(ctx, other.Record()))
			c := curate.Change{Kind: tc.args.kind, ID: "k-quoted", Version: 1, Anchor: 1}
			if tc.args.kind == withdraw {
				c.Signal = "c-1"
			}
			decide := d.asking(t, "r-k-quoted", c, curate.RulePersonItem)
			traces := &testkit.FlakyTraces{Store: d.stores.Traces, Gets: tc.args.gets}
			iv := curate.NewIntervener(d.stores.Ledger, traces, curate.InterveneSetting{}, d.stores.Clock.Now)
			a := d.stopIn(t, "s-new", "acme", true)
			a.Mark.Due = []curate.Due{{Kind: curate.QuestionDirection, Decide: decide, At: written}}

			got, err := iv.Ask(ctx, a)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want, want{got.Question.Excerpt, strings.Contains(got.Question.Text(),
				`the part corrected "cd repo && git status"`), err})
		})
	}
}

// A stop with no words to read prepares what the change says alone
func TestIntervenerAskWithoutWords(t *testing.T) {
	d := newDesk(t)
	a := d.stopIn(t, "s-new", "svc", true)
	a.Words = nil

	got, err := d.iv.Ask(context.Background(), a)

	require.NoError(t, err)
	assert.Equal(t, []string{"drafted from a correction"}, got.Question.Prepared)
	assert.Empty(t, got.Question.Words)
}

// A text that is no HMAC fails the expose and shows nothing
func TestIntervenerAskUnsealed(t *testing.T) {
	d := newDesk(t)
	a := d.stopIn(t, "s-new", "svc", true)
	a.Seal = func(text string) string { return text }

	got, err := d.iv.Ask(context.Background(), a)

	assert.ErrorIs(t, err, curate.ErrExposeInvalid)
	assert.Empty(t, exposed(t, got))
}

// An expose a writer left without a probability cannot close its question as no_answer and fails the stop
func TestIntervenerAskUndrawn(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	ask := d.decideOf(t, "k-ask")
	for _, session := range []string{"s-1", "s-2"} {
		tr := trace.Trace{
			ID: trace.NewID(written), Name: trace.NameExpose, SessionID: session, Subject: string(curate.QuestionDirection), Ref: ask, Time: written,
			Input: json.RawMessage(`{"kind":"direction","key":"ab12","surface":"additional_context","rules":"1","choices":["keep","drop"],"round":1}`),
			Draw:  &trace.Draw{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d"}, Source: trace.SourcePush,
		}
		require.NoError(t, d.stores.Traces.Append(ctx, tr.Record()))
	}

	got, err := d.iv.Ask(ctx, d.stopIn(t, "s-3", "svc", true))

	assert.ErrorIs(t, err, trace.ErrDrawInvalid)
	assert.Empty(t, exposed(t, got))
}

// An answer that cannot be appended after its ledger write fails the reply
func TestIntervenerReplyUnappended(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	_, err := d.iv.Ask(ctx, d.stopIn(t, "s-new", "svc", true))
	require.NoError(t, err)

	got, err := d.flakyIntervener(-1, 0).Reply(ctx, curate.Reply{Session: "s-new", Run: "run-s-new", Word: "keep", Person: "ann"})

	assert.ErrorIs(t, err, assert.AnError)
	assert.Empty(t, got.Answer)
}

// A reply whose expose names a draw no answer may take records nothing and leaves the ledger as it was
func TestIntervenerReplyUndrawn(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	tr := trace.Trace{
		ID: trace.NewID(written), Name: trace.NameExpose, SessionID: "s-new", Subject: string(curate.QuestionDirection), Ref: d.decideOf(t, "k-ask"), Time: written,
		Input: json.RawMessage(`{"kind":"direction","key":"ab12","surface":"additional_context","rules":"1","choices":["keep","drop"],"round":1,"run":"run-s-new"}`),
		Draw:  &trace.Draw{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d"}, Source: trace.SourcePush,
	}
	require.NoError(t, d.stores.Traces.Append(ctx, tr.Record()))
	before := statuses(t, d.bench)

	_, err := d.iv.Reply(ctx, curate.Reply{Session: "s-new", Run: "run-s-new", Word: "keep", Person: "ann"})

	assert.ErrorIs(t, err, trace.ErrDrawInvalid)
	assert.Equal(t, before, statuses(t, d.bench))
}

// Whether curate approved or withdrew a version that reaches the place
func TestNoticeable(t *testing.T) {
	carried := lesson("k-carried", 1, "Squash before a merge", acme("work"), knowledge.StatusApproved, knowledge.ApproverNodloop)
	carried.Decision = "d-1"
	undecided := carried
	undecided.Decision = ""
	tcs := []struct {
		name string
		args knowledge.Set
		want bool
	}{
		{"a version nodloop approved through a decide", knowledge.Set{carried}, true},
		{"a version nodloop approved with no decide named", knowledge.Set{undecided}, false},
		{"a person's version", knowledge.Set{personLesson()}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := curate.Noticeable(tc.args, *acme("work"))

			assert.Equal(t, tc.want, got)
		})
	}
}

// Many lessons in one notice are counted and cut at 600 runes before the words that say how to take one back
func TestIntervenerNoticeMany(t *testing.T) {
	var many []knowledge.Knowledge
	for i := range 8 {
		k := lesson(fmt.Sprintf("k-%d", i), 1, strings.Repeat(fmt.Sprintf("lesson %d ", i), 20), acme("work"), knowledge.StatusApproved, knowledge.ApproverNodloop)
		k.Decision = "d-many"
		many = append(many, k)
	}
	b := newBench(t, nil, many, nil)

	got, err := b.intervener(nil).Notice(context.Background(), b.noticeStop(t, "s-new", "work"))

	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got.Notice, "From your earlier decisions nodloop applied 8 lessons here: "), got.Notice)
	assert.True(t, strings.HasSuffix(got.Notice, "…. /nodloop:nod shows them and takes any back"), got.Notice)
	assert.Equal(t, curate.MaxRunes, utf8.RuneCountInString(got.Notice))
}

// The run of an earlier notice that cannot be read fails the notice
// A notice of a stop with no run is one of another place
func TestIntervenerNoticeEarlier(t *testing.T) {
	ctx := context.Background()
	type args struct {
		// The run the earlier notice names
		run string
		// Gets of traces that pass before every later one fails and none fails when negative
		gets int
	}
	type want struct {
		shown bool
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a notice of a stop with no run is one of another place", args{"", -1}, want{true, nil}},
		{"a run that cannot be read fails the notice", args{"run-s-old", 0}, want{false, assert.AnError}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := noticeBench(t)
			old := b.noticeStop(t, "s-old", "work")
			old.Run = tc.args.run
			_, err := b.intervener(nil).Notice(ctx, old)
			require.NoError(t, err)
			traces := &testkit.FlakyTraces{Store: b.stores.Traces, Gets: failing(tc.args.gets)}
			iv := curate.NewIntervener(b.stores.Ledger, traces, curate.InterveneSetting{Build: "0.10.0"}, b.stores.Clock.Now)

			got, err := iv.Notice(ctx, b.noticeStop(t, "s-new", "work"))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.shown, got.Notice != "")
		})
	}
}

// A ledger that cannot be read fails the reply to a question shown and records nothing
// The stop and the notice read none since they take the ledger the stop read
func TestIntervenerAskUnreadableLedger(t *testing.T) {
	ctx := context.Background()
	d := newDesk(t)
	a := d.stopIn(t, "s-new", "svc", true)
	_, err := d.iv.Ask(ctx, a)
	require.NoError(t, err)
	iv := curate.NewIntervener(flakyLedger(t, d.bench, 0), d.stores.Traces, curate.InterveneSetting{Build: "0.10.0"}, d.stores.Clock.Now)

	asked, askErr := iv.Ask(ctx, d.stopIn(t, "s-other", "svc", true))
	replied, replyErr := iv.Reply(ctx, curate.Reply{Session: "s-new", Run: "run-s-new", Word: "keep", Person: "ann"})

	require.NoError(t, askErr)
	assert.ErrorIs(t, replyErr, assert.AnError)
	answers, err := d.stores.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameAnswer)})
	require.NoError(t, err)
	assert.Equal(t, []any{true, false, 0}, []any{exposed(t, asked) != "", replied.Matched, len(answers)})
}

// The audit of a version the ledger no longer holds quotes nothing and reaches everywhere
func TestIntervenerAskGoneVersion(t *testing.T) {
	d := newDesk(t)
	decide := d.carried(t, "r-k-add", curate.Change{Kind: curate.ChangeAdd, ID: "k-gone", Version: 1, Anchor: 1}, curate.Decision{Tier: 1, Action: curate.ActionApply})
	a := d.stopIn(t, "s-new", "acme", true)
	a.Mark.Due = []curate.Due{{Kind: curate.QuestionAudit, Decide: decide, At: written}}

	got, err := d.iv.Ask(context.Background(), a)

	require.NoError(t, err)
	assert.Equal(t, "nodloop added the lesson %s everywhere from your earlier decisions", got.Question.Change)
	assert.Equal(t, []string{"drafted from a correction"}, got.Question.Prepared)
}

// A notice whose text is no HMAC fails and a question exposed beside the notices never counts as one
func TestIntervenerNoticeRecords(t *testing.T) {
	ctx := context.Background()
	b := noticeBench(t)
	question := trace.Trace{
		ID: trace.NewID(written), Name: trace.NameExpose, SessionID: "s-new", Subject: string(curate.QuestionDirection), Ref: "d-1", Time: written,
		Input: json.RawMessage(`{"kind":"direction"}`), Source: trace.SourcePush,
	}
	require.NoError(t, b.stores.Traces.Append(ctx, question.Record()))
	a := b.noticeStop(t, "s-new", "work")
	unsealed := a
	unsealed.Seal = func(text string) string { return text }

	failed, err := b.intervener(nil).Notice(ctx, unsealed)
	require.ErrorIs(t, err, curate.ErrExposeInvalid)
	got, err := b.intervener(nil).Notice(ctx, a)

	require.NoError(t, err)
	assert.Empty(t, failed.Notice)
	assert.NotEmpty(t, got.Notice, "the question of the session is no notice")
}

// A question and a notice shown after a person's raise inside the cycle carry the policy version of the knob event of that raise
// The mark keeps the version before the raise until the next pass
func TestIntervenerAskPolicyInForce(t *testing.T) {
	ctx := context.Background()
	carried := lesson("k-carried", 1, "Squash before a merge", acme("svc"), knowledge.StatusApproved, knowledge.ApproverNodloop)
	carried.Decision = "d-carried"
	b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
		lesson("k-ask", 1, "Use the staging cluster", acme("svc"), knowledge.StatusCandidate, ""), carried,
	}, nil)
	first, err := b.curator().Pass(ctx, b.setting(t))
	require.NoError(t, err)
	legacy, err := (curate.Event{ID: "d-legacy", Ref: "r-k-ask", Source: trace.SourcePush,
		Draw: trace.NewDraw(first.Event.CycleID, first.Event.PolicyVersion, "seed", "d-legacy", "audit", 1)}).Decide(
		curate.Change{Kind: curate.ChangeAdd, ID: "k-ask", Version: 1},
		curate.Decision{Tier: 2, RuleCode: curate.RuleScopeWider, Action: curate.ActionAsk}, nil, written)
	require.NoError(t, err)
	require.NoError(t, b.stores.Traces.Append(ctx, legacy.Record()))
	first.Due = append(first.Due, curate.Due{Kind: curate.QuestionDirection, Decide: legacy.ID, Change: curate.ChangeAdd,
		Ref: knowledge.Ref{ID: "k-ask", Version: 1}, Place: acme("svc")})
	marker := curate.NewMarker(b.stores.Dir)
	held, err := marker.Hold()
	require.NoError(t, err)
	require.NoError(t, held.Write(first.Mark(written)))
	require.NoError(t, held.Release())
	iv := b.intervener(marker)
	raised := 0.3
	require.NoError(t, testkit.Err(iv.Intervene(ctx, curate.Request{Action: trace.ActionKnob, Target: string(curate.KnobAuditTier1), Value: &raised}, person)))
	knobs, err := b.stores.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameKnob)})
	require.NoError(t, err)
	inForce, ok := curate.RestoreKnob(trace.Restore(knobs[0]))
	require.True(t, ok)
	m, err := curate.ReadMark(b.stores.Dir)
	require.NoError(t, err)
	b.correct(t, "run-s-new", trace.Labels{"repo": {"svc"}}, nil)
	a := curate.Asking{Session: "s-new", Run: "run-s-new", Place: *acme("svc"), Pushed: true, Mark: m, All: b.all(t), Seal: hexSeal, Words: runWords}

	asked, err := iv.Ask(ctx, a)
	require.NoError(t, err)
	a.Session = "s-other"
	noticed, err := iv.Notice(ctx, a)
	require.NoError(t, err)

	require.NotEqual(t, m.PolicyVersion, inForce.PolicyVersion, "the raise moved the policy version")
	assert.Equal(t, []string{inForce.PolicyVersion, inForce.PolicyVersion}, []string{asked.Expose.Draw.PolicyVersion, noticed.Expose.Draw.PolicyVersion})
}
