package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestInstructionsTrace(t *testing.T) {
	at := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	hmac := strings.Repeat("ab", 32)
	type args struct {
		in      instructionsInput
		session string
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
			"a rules file names its path, the HMAC of its content and what the host reported",
			args{instructionsInput{Path: "CLAUDE.md", HMAC: hmac, MemoryType: "Project", LoadReason: "session_start"}, "s1"},
			want{`{"path":"CLAUDE.md","hmac":"` + hmac + `","memory_type":"Project","load_reason":"session_start"}`, nil},
		},
		{"a file of no session is refused", args{instructionsInput{Path: "CLAUDE.md", HMAC: hmac}, ""}, want{err: errSessionTrace}},
		{"a file without its path is refused", args{instructionsInput{HMAC: hmac}, "s1"}, want{err: errSessionTrace}},
		{"a content kept as text is refused", args{instructionsInput{Path: "CLAUDE.md", HMAC: "always use git -C"}, "s1"}, want{err: errSessionTrace}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.in.trace(tc.args.session, at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.input, string(got.Input))
		})
	}
	got, err := instructionsInput{Path: "CLAUDE.md", HMAC: hmac}.trace("s1", at)
	require.NoError(t, err)
	assert.Equal(t, []any{trace.NameInstructions, "s1", at}, []any{got.Name, got.SessionID, got.Time})
	assert.NoError(t, schema.Validate(string(trace.NameInstructions), got.Record()))
}

func TestCaptureTrace(t *testing.T) {
	at := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	read := captureOutput{Messages: 3, Verdicts: 1, Answers: 1, Runs: []string{"r1", "r2", "r2"}, Records: []string{"v1", "a1"}, Classify: []string{"c1"}}
	type args struct {
		in      captureInput
		session string
		out     captureOutput
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
			"a pass names the offsets it read and the records it wrote",
			args{captureInput{From: 120, To: 4096}, "s1", read},
			want{
				`{"from":120,"to":4096}`,
				`{"messages":3,"verdicts":1,"answers":1,"signals":0,"runs":["r1","r2","r2"],"records":["v1","a1"],"classify":["c1"]}`, nil,
			},
		},
		{
			"a pass that read nothing keeps empty lists",
			args{captureInput{From: 4096, To: 4096}, "s1", captureOutput{}},
			want{`{"from":4096,"to":4096}`, `{"messages":0,"verdicts":0,"answers":0,"signals":0,"runs":[],"records":[],"classify":[]}`, nil},
		},
		{"a pass of no session is refused", args{captureInput{To: 10}, "", read}, want{err: errSessionTrace}},
		{"a pass that reads backward is refused", args{captureInput{From: 10, To: 5}, "s1", read}, want{err: errSessionTrace}},
		{"an offset below 0 is refused", args{captureInput{From: -1, To: 5}, "s1", read}, want{err: errSessionTrace}},
		{
			"more records than messages are refused",
			args{captureInput{To: 10}, "s1", captureOutput{Messages: 1, Verdicts: 1, Signals: 1}}, want{err: errSessionTrace},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.in.trace(tc.args.session, tc.args.out, at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want, want{string(got.Input), string(got.Output), tc.want.err})
		})
	}
	got, err := captureInput{To: 10}.trace("s1", read, at)
	require.NoError(t, err)
	assert.Equal(t, []any{trace.NameCapture, "s1", at}, []any{got.Name, got.SessionID, got.Time})
	assert.NoError(t, schema.Validate(string(trace.NameCapture), got.Record()))
}
