package feedback_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestOutcomeEvaluator(t *testing.T) {
	type want struct {
		actor            feedback.Actor
		implicit, proven bool
	}
	tcs := []struct {
		name string
		args record.Outcome
		want want
	}{
		{"legacy named is unknown", record.Outcome{Reviewer: "ann"}, want{feedback.ActorUnknown, true, false}},
		{"legacy session is a model", record.Outcome{Reviewer: "session"}, want{feedback.ActorModel, true, false}},
		{"legacy capture is a model", record.Outcome{Reviewer: "nodloop"}, want{feedback.ActorModel, true, false}},
		{"legacy proof alone names no evaluator", record.Outcome{Reviewer: "ann", Proof: "key"},
			want{feedback.ActorUnknown, true, false}},
		{"future actor remains unknown", record.Outcome{Reviewer: "ann", Actor: "future", Proof: "key"},
			want{feedback.ActorUnknown, true, false}},
		{"person claim proves nobody", record.Outcome{Reviewer: "ann", Actor: "person"},
			want{feedback.ActorPerson, false, false}},
		{"person key proves the person", record.Outcome{Reviewer: "ann", Actor: "person", Proof: "key"},
			want{feedback.ActorPerson, false, true}},
		{"code with a copied proof is no person", record.Outcome{Reviewer: "ann", Actor: "code", Proof: "key"},
			want{feedback.ActorCode, true, false}},
		{"inferred person claim is never proven", record.Outcome{Reviewer: "session", Actor: "person", Proof: "key"},
			want{feedback.ActorModel, true, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := feedback.RestoreOutcome(tc.args)
			assert.Equal(t, tc.want, want{got.Evaluator(), got.Implicit(), got.Proven()})
			assert.Equal(t, tc.args.Actor, got.Record().Actor)
		})
	}
}

func TestNewOutcomeActor(t *testing.T) {
	type args struct {
		actor    feedback.Actor
		reviewer string
	}
	type want struct {
		err   error
		actor feedback.Actor
		text  string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a legacy name stays unknown", args{"", "ann"}, want{nil, feedback.ActorUnknown, "token=[redacted]"}},
		{"session defaults to model", args{"", "session"}, want{nil, feedback.ActorModel, "token=[redacted]"}},
		{"code is explicit", args{feedback.ActorCode, "ann"}, want{nil, feedback.ActorCode, "token=[redacted]"}},
		{"model is explicit", args{feedback.ActorModel, "ann"}, want{nil, feedback.ActorModel, "token=[redacted]"}},
		{"unknown is explicit", args{feedback.ActorUnknown, "ann"}, want{nil, feedback.ActorUnknown, "token=[redacted]"}},
		{"person keeps their text", args{feedback.ActorPerson, "ann"}, want{nil, feedback.ActorPerson, "token=examplevalue"}},
		{"unknown enum is refused", args{"future", "ann"}, want{feedback.ErrActorUnknown, feedback.ActorUnknown, ""}},
		{"inferred person claim is refused", args{feedback.ActorPerson, "session"},
			want{feedback.ErrActorUnexpected, feedback.ActorUnknown, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := feedback.NewOutcome("r1", feedback.ResultConfirmed, "token=examplevalue", "token=examplevalue",
				tc.args.reviewer, tc.args.actor, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.actor, got.Evaluator())
			assert.Equal(t, tc.want.text, got.Note)
			assert.Equal(t, tc.want.text, got.ConfirmedCause)
		})
	}
}

func TestActorDeclared(t *testing.T) {
	type args struct {
		actor    feedback.Actor
		proof    trace.Proof
		reviewer string
	}
	tcs := []struct {
		name string
		args args
		want feedback.Actor
	}{
		{"a terminal reads as a person", args{"", trace.ProofTerminal, "ann"}, feedback.ActorPerson},
		{"a key reads as a person", args{"", trace.ProofKey, "ann"}, feedback.ActorPerson},
		{"no proof names nobody", args{"", trace.ProofNone, "ann"}, ""},
		{"an inferred reviewer stays unnamed under a key", args{"", trace.ProofKey, feedback.ReviewerSession}, ""},
		{"a named actor stays as named", args{feedback.ActorCode, trace.ProofKey, "ann"}, feedback.ActorCode},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.actor.Declared(tc.args.proof, tc.args.reviewer))
		})
	}
}

func TestOutcomeProof(t *testing.T) {
	type args struct {
		actor feedback.Actor
		proof trace.Proof
	}
	type want struct {
		proof  trace.Proof
		proven bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a terminal proves a person", args{feedback.ActorPerson, trace.ProofTerminal}, want{trace.ProofTerminal, true}},
		{"a key proves a person", args{feedback.ActorPerson, trace.ProofKey}, want{trace.ProofKey, true}},
		{"a person without proof stays unproven", args{feedback.ActorPerson, trace.ProofNone}, want{trace.ProofNone, false}},
		{"an unknown proof proves nobody", args{feedback.ActorPerson, "future"}, want{"future", false}},
		{"a code check has no person proof", args{feedback.ActorCode, trace.ProofKey}, want{"", false}},
		{"a model has no person proof", args{feedback.ActorModel, trace.ProofTerminal}, want{"", false}},
		{"a legacy outcome has no person proof", args{"", trace.ProofKey}, want{"", false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := feedback.NewOutcome("r1", feedback.ResultConfirmed, "", "", "ann", tc.args.actor, time.Time{})
			require.NoError(t, err)
			got = got.WithProof(tc.args.proof)
			assert.Equal(t, tc.want, want{got.Proof, got.Proven()})
		})
	}
}

func TestResultValid(t *testing.T) {
	tcs := []struct {
		name string
		args feedback.Result
		want bool
	}{
		{"confirmed is valid", feedback.ResultConfirmed, true},
		{"refuted is valid", feedback.ResultRefuted, true},
		{"inconclusive is valid", feedback.ResultInconclusive, true},
		{"unknown result is invalid", "maybe", false},
		{"empty result is invalid", "", false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Valid())
		})
	}
}

func TestNewOutcome(t *testing.T) {
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.FixedZone("UTC+9", 9*60*60))
	utc := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	type args struct {
		traceID        string
		result         feedback.Result
		confirmedCause string
		reviewer       string
	}
	type want struct {
		outcome feedback.Outcome
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"confirmed keeps its cause and the named reviewer",
			args{traceID: "t1", result: feedback.ResultConfirmed, confirmedCause: "cause", reviewer: "sam"},
			want{outcome: feedback.Outcome{
				TraceID: "t1", Time: utc, Result: feedback.ResultConfirmed, ConfirmedCause: "cause", Note: "note",
				Reviewer: "sam",
			}},
		},
		{
			"refuted keeps the named reviewer",
			args{traceID: "t1", result: feedback.ResultRefuted, reviewer: "sam"},
			want{outcome: feedback.Outcome{
				TraceID: "t1", Time: utc, Result: feedback.ResultRefuted, Note: "note", Reviewer: "sam",
			}},
		},
		{
			"inconclusive is a known result",
			args{traceID: "t1", result: feedback.ResultInconclusive, reviewer: "sam"},
			want{outcome: feedback.Outcome{
				TraceID: "t1", Time: utc, Result: feedback.ResultInconclusive, Note: "note", Reviewer: "sam",
			}},
		},
		{
			"refuted with a confirmed cause fails",
			args{traceID: "t1", result: feedback.ResultRefuted, confirmedCause: "cause", reviewer: "sam"},
			want{err: feedback.ErrCauseUnexpected},
		},
		{
			"inconclusive with a confirmed cause fails",
			args{traceID: "t1", result: feedback.ResultInconclusive, confirmedCause: "cause", reviewer: "sam"},
			want{err: feedback.ErrCauseUnexpected},
		},
		{"unknown result fails", args{traceID: "t1", result: "maybe", reviewer: "sam"}, want{err: feedback.ErrResultUnknown}},
		{"missing trace id fails", args{result: feedback.ResultConfirmed, reviewer: "sam"}, want{err: feedback.ErrTraceIDRequired}},
		{"missing reviewer fails", args{traceID: "t1", result: feedback.ResultConfirmed}, want{err: feedback.ErrReviewerRequired}},
		{
			"the reserved name of the capture fails",
			args{traceID: "t1", result: feedback.ResultConfirmed, reviewer: feedback.ReviewerNodloop},
			want{err: feedback.ErrReviewerReserved},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := feedback.NewOutcome(
				tc.args.traceID, tc.args.result, tc.args.confirmedCause, "note", tc.args.reviewer, "", now,
			)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.outcome, got)
		})
	}
}

func TestOutcomeImplicit(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want bool
	}{
		{"session reviewer is implicit", feedback.ReviewerSession, true},
		{"nodloop reviewer is implicit", feedback.ReviewerNodloop, true},
		{"legacy author does not prove a person", "author", true},
		{"a named reviewer alone does not prove a person", "ann", true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, feedback.Outcome{Reviewer: tc.args}.Implicit())
		})
	}
}

func TestOutcomesLatest(t *testing.T) {
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	confirmed := feedback.Outcome{TraceID: "t1", Time: base.Add(time.Hour), Result: feedback.ResultConfirmed}
	refuted := feedback.Outcome{TraceID: "t1", Time: base, Result: feedback.ResultRefuted}
	other := feedback.Outcome{TraceID: "t2", Time: base, Result: feedback.ResultRefuted}
	sameTime := feedback.Outcome{TraceID: "t2", Time: base, Result: feedback.ResultInconclusive}
	tcs := []struct {
		name string
		args feedback.Outcomes
		want feedback.Outcomes
	}{
		{"no outcomes give nothing", nil, nil},
		{"newest outcome wins when listed first", feedback.Outcomes{confirmed, other, refuted}, feedback.Outcomes{confirmed, other}},
		{"newest outcome wins when listed last", feedback.Outcomes{refuted, other, confirmed}, feedback.Outcomes{confirmed, other}},
		{"tie in time keeps the outcome listed first", feedback.Outcomes{other, sameTime}, feedback.Outcomes{other}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Latest())
		})
	}
}

func TestOutcomeRefutedSince(t *testing.T) {
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	type args struct {
		result feedback.Result
		at     time.Time
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a refutation after the time counts", args{feedback.ResultRefuted, base.Add(-time.Minute)}, true},
		{"a refutation at the time counts", args{feedback.ResultRefuted, base}, true},
		{"a refutation before the time does not count", args{feedback.ResultRefuted, base.Add(time.Minute)}, false},
		{"a confirmation never counts", args{feedback.ResultConfirmed, base.Add(-time.Minute)}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, feedback.Outcome{Result: tc.args.result, Time: base}.RefutedSince(tc.args.at))
		})
	}
}
