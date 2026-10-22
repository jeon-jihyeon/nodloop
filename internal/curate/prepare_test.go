package curate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestIntervenerPrepare(t *testing.T) {
	type args struct{ proposal, emptySession, emptyMark, noSeal bool }
	type want struct {
		prepared bool
		err      error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an ordinary candidate stays pending", args{}, want{}},
		{"an explicitly requested proposal is prepared", args{proposal: true}, want{true, nil}},
		{"an unknown session prepares nothing", args{proposal: true, emptySession: true}, want{}},
		{"an empty mark prepares nothing", args{proposal: true, emptyMark: true}, want{}},
		{"a question without a seal fails safely", args{proposal: true, noSeal: true}, want{false, curate.ErrExposeInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDesk(t)
			ctx := context.Background()
			if tc.args.proposal {
				require.NoError(t, testkit.Err(d.iv.Intervene(ctx,
					curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep, Session: "s-new"}, agent)))
			}
			a := d.stopIn(t, "s-new", "acme", true)
			if tc.args.emptySession {
				a.Session = ""
			}
			if tc.args.emptyMark {
				a.Mark.Due = nil
			}
			if tc.args.noSeal {
				a.Seal = nil
			}
			shown, err := d.iv.Prepare(ctx, a)
			found, readErr := d.stores.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameExpose)})
			require.NoError(t, readErr)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.prepared, shown.Question.Key != "")
			assert.Empty(t, found)
		})
	}
}

func TestIntervenerObserve(t *testing.T) {
	type args struct{ key, question, option, read, append, turn, timestamp bool }
	type want struct {
		exposed bool
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the host tool call records one exposure", args{}, want{true, nil}},
		{"another key records nothing", args{key: true}, want{}},
		{"changed wording records nothing", args{question: true}, want{}},
		{"changed option text records nothing", args{option: true}, want{}},
		{"a record read failure records nothing", args{read: true}, want{false, assert.AnError}},
		{"an exposure append failure is returned", args{append: true}, want{false, assert.AnError}},
		{"a writer turn failure records nothing", args{turn: true}, want{false, assert.AnError}},
		{"a host call without a timestamp records nothing", args{timestamp: true}, want{false, curate.ErrExposeInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			d := newDesk(t)
			require.NoError(t, testkit.Err(d.iv.Intervene(ctx,
				curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep, Session: "s-new"}, agent)))
			a := d.stopIn(t, "s-new", "acme", false)
			prepared, err := d.iv.Prepare(ctx, a)
			require.NoError(t, err)
			q := prepared.Question
			r := curate.Reply{Key: q.Key, Question: strings.TrimSuffix(q.Text(), "\nReply yes or notyet"), Seal: hexSeal,
				Options: []curate.Option{{Label: "yes", Description: "Go ahead"}, {Label: "notyet", Description: "Leave everything as it is"}}}
			if tc.args.key {
				r.Key = "0000"
			}
			if tc.args.question {
				r.Question = "A different question"
			}
			if tc.args.option {
				r.Options[0].Description = "A different effect"
			}
			traces := &testkit.FlakyTraces{Store: d.stores.Traces}
			if tc.args.read {
				traces.Lists.Err = assert.AnError
			}
			if tc.args.append {
				traces.Appends.Err = assert.AnError
			}
			setting := curate.InterveneSetting{}
			if tc.args.turn {
				setting.Turn = func() (func(), error) { return nil, assert.AnError }
			}
			iv := curate.NewIntervener(d.stores.Ledger, traces, setting, d.stores.Clock.Now)
			at := d.stores.Clock.Now()
			if tc.args.timestamp {
				at = time.Time{}
			}
			shown, err := iv.Observe(ctx, a, r, at)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.exposed, shown.Expose.ID != "")
			assert.Equal(t, map[bool]time.Time{true: at}[tc.want.exposed], shown.Expose.Time)
		})
	}
}
