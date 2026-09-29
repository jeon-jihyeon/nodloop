package diagnose_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestReadRecorded(t *testing.T) {
	input := json.RawMessage(`{"mode":"interactive","change_context":"unknown",` +
		`"knowledge":[{"id":"k","version":2,"chars":30}]}`)
	output := json.RawMessage(`{"status":"hold","causes":[],"checks":[]}`)
	type args struct {
		name   trace.Name
		input  json.RawMessage
		output json.RawMessage
		tags   []string
	}
	type want struct {
		recorded diagnose.Recorded
		err      error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a forced hold of the conversation",
			args{trace.NameDiagnose, input, output, []string{diagnose.TagGateHold}},
			want{recorded: diagnose.Recorded{
				Mode: diagnose.ModeInteractive, ChangeContext: evidence.ContextUnknown,
				Knowledge: []diagnose.AppliedKnowledge{{ID: "k", Version: 2, Chars: 30}},
				Diagnosis: diagnose.Diagnosis{Status: evidence.StatusHold, Causes: []diagnose.Cause{}, Checks: diagnose.Checks{}},
				Forced:    true,
			}},
		},
		{"another trace name", args{trace.NameContext, input, output, nil}, want{err: diagnose.ErrMalformed}},
		{
			"an input that does not decode",
			args{trace.NameDiagnose, json.RawMessage(`[]`), output, nil}, want{err: diagnose.ErrMalformed},
		},
		{
			"an output that does not decode",
			args{trace.NameDiagnose, input, json.RawMessage(`[]`), nil}, want{err: diagnose.ErrMalformed},
		},
		{
			"an unknown status",
			args{trace.NameDiagnose, input, json.RawMessage(`{"status":"maybe"}`), nil}, want{err: diagnose.ErrMalformed},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := diagnose.ReadRecorded(trace.Trace{
				ID: "r", Name: tc.args.name, Input: tc.args.input, Output: tc.args.output, Tags: tc.args.tags,
			})
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.recorded, got)
		})
	}
}

func TestDiagnosisCitations(t *testing.T) {
	tcs := []struct {
		name string
		args diagnose.Diagnosis
		want []string
	}{
		{"nothing cited", diagnose.Diagnosis{}, nil},
		{
			"causes before checks once each",
			diagnose.Diagnosis{
				Causes: []diagnose.Cause{{ParagraphIDs: []string{"a#1", "b#1"}}, {ParagraphIDs: []string{"a#1"}}},
				Checks: diagnose.Checks{{ParagraphIDs: []string{"c#1", "b#1"}}},
			},
			[]string{"a#1", "b#1", "c#1"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Citations())
		})
	}
}
