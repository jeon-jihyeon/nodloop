package analysis_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// Answers every command with a fixed stdout or error and keeps what it was given
type fakeRunner struct {
	stdout string
	err    error
	argv   *[]string
	stdin  *[]byte
	// The timeout of the last run
	timeout *time.Duration
}

func (r fakeRunner) Run(argv []string, stdin []byte, timeout time.Duration) ([]byte, error) {
	*r.argv, *r.stdin, *r.timeout = argv, stdin, timeout
	return []byte(r.stdout), r.err
}

func TestPolicyAnalyzeCommand(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ev := evidence.Event{ID: "ev-1", ChangeContext: evidence.ContextUnknown, Points: []evidence.Point{
		{Time: start, Metric: "latency_ms", Value: 120, Dims: map[string]string{"host": "a"}},
		{Time: start.Add(time.Hour), Metric: "latency_ms", Value: 480, Dims: map[string]string{"host": "a"}},
	}}
	ref := analysis.Ref{EventID: "ev-1", Start: start, End: start.Add(time.Hour)}
	window := analysis.Window{Start: start, End: start.Add(time.Hour), Points: 2}
	report := func(observations string) string {
		return `{"observation_version":1,"observations":[` + observations + `]}`
	}
	const (
		mild  = `{"metric":"latency_ms","current":200,"baseline":120,"change":1.6,"severity":0.3,"adequate":true,"summary":"p99 rose"}`
		sharp = `{"metric":"latency_ms","target":{"host":"a"},"current":480,"baseline":120,"change":4,"severity":0.9,` +
			`"adequate":true,"summary":"p99 quadrupled","samples":2,"window":{"start":"2026-10-01T00:00:00Z","end":"2026-10-01T01:00:00Z","points":2}}`
	)
	failed := func(reason string) analysis.Observations {
		return analysis.Observations{{Rule: analysis.RuleCommand, Window: window, Ref: ref, Summary: "command analyzer p99 failed: " + reason}}
	}
	type args struct {
		stdout string
		err    error
	}
	tcs := []struct {
		name string
		args args
		want analysis.Observations
	}{
		{
			"reported observations come back by severity with the name leading each summary",
			args{stdout: report(mild + "," + sharp)},
			analysis.Observations{
				{
					Rule: analysis.RuleCommand, Target: map[string]string{"host": "a"}, Metric: "latency_ms", Window: window, Current: 480,
					Baseline: 120, Change: 4, Severity: 0.9, Adequate: true, Detail: analysis.Detail{Samples: 2}, Ref: ref,
					Summary: "p99: p99 quadrupled",
				},
				{
					Rule: analysis.RuleCommand, Metric: "latency_ms", Current: 200, Baseline: 120, Change: 1.6, Severity: 0.3,
					Adequate: true, Ref: ref, Summary: "p99: p99 rose",
				},
			},
		},
		{"no observation reports nothing", args{stdout: report("")}, nil},
		{"a timeout names the analyzer", args{err: fmt.Errorf("%w: after 10s", analysis.ErrCommandTimeout)}, failed(analysis.ErrCommandTimeout.Error() + ": after 10s")},
		{"a failed run names the analyzer", args{err: fmt.Errorf("%w: exit status 1", analysis.ErrCommandFailed)}, failed(analysis.ErrCommandFailed.Error() + ": exit status 1")},
		{"output that is not JSON names the analyzer", args{stdout: "nope"}, failed(analysis.ErrCommandOutput.Error() + ": invalid character 'o' in literal null (expecting 'u')")},
		{"another version fails", args{stdout: `{"observation_version":2,"observations":[]}`}, failed(analysis.ErrCommandOutput.Error() + ": observation_version must be 1")},
		{"a missing version fails", args{stdout: `{"observations":[]}`}, failed(analysis.ErrCommandOutput.Error() + ": observation_version must be 1")},
		{
			"an unknown key fails",
			args{stdout: `{"observation_version":1,"observations":[],"extra":1}`},
			failed(analysis.ErrCommandOutput.Error() + `: json: unknown field "extra"`),
		},
		{
			"a missing number fails instead of reading zero",
			args{stdout: report(`{"metric":"latency_ms","current":1,"baseline":1,"severity":0.1,"adequate":true,"summary":"s"}`)},
			failed("observations[0]: " + analysis.ErrCommandOutput.Error() + ": change is required"),
		},
		{
			"a severity above one fails",
			args{stdout: report(`{"metric":"latency_ms","current":1,"baseline":1,"change":1,"severity":2,"adequate":true,"summary":"s"}`)},
			failed("observations[0]: " + analysis.ErrCommandOutput.Error() + ": severity 2 is outside 0 to 1"),
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var argv []string
			var stdin []byte
			var timeout time.Duration
			run := fakeRunner{stdout: tc.args.stdout, err: tc.args.err, argv: &argv, stdin: &stdin, timeout: &timeout}
			policy, err := analysis.LoadPolicy([]byte("version: v1\nanalyzers:\n  - rule: command\n    name: p99\n    command: [./p99.py, --fast]\n"), run)
			require.NoError(t, err)

			got, err := policy.Analyze(ev)

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, []string{"./p99.py", "--fast"}, argv)
			assert.Equal(t, 10*time.Second, timeout)
			var in map[string]any
			require.NoError(t, json.Unmarshal(stdin, &in))
			assert.Equal(t, map[string]any{
				"observation_version": float64(1), "event_id": "ev-1", "change_context": "unknown",
				"points": []any{
					map[string]any{"time": "2026-10-01T00:00:00Z", "metric": "latency_ms", "value": float64(120), "dims": map[string]any{"host": "a"}},
					map[string]any{"time": "2026-10-01T01:00:00Z", "metric": "latency_ms", "value": float64(480), "dims": map[string]any{"host": "a"}},
				},
			}, in)
		})
	}
}

func TestLoadPolicyCommand(t *testing.T) {
	var argv []string
	var stdin []byte
	var timeout time.Duration
	run := fakeRunner{argv: &argv, stdin: &stdin, timeout: &timeout}
	type want struct {
		specs []analysis.RuleSpec
		err   error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			"a command analyzer reads its name command and timeout without metrics",
			"  - rule: command\n    name: p99\n    command: [./p99.py]\n    timeout: 30s\n",
			want{specs: []analysis.RuleSpec{{Rule: analysis.RuleCommand, Name: "p99", Command: []string{"./p99.py"}, Timeout: 30 * time.Second}}},
		},
		{"a command analyzer without a name is incomplete", "  - rule: command\n    command: [./p99.py]\n", want{err: analysis.ErrIncompleteCommand}},
		{"a command analyzer without a command is incomplete", "  - rule: command\n    name: p99\n", want{err: analysis.ErrIncompleteCommand}},
		{
			"two command analyzers with one name are refused",
			"  - rule: command\n    name: p99\n    command: [./a.py]\n  - rule: command\n    name: p99\n    command: [./b.py]\n",
			want{err: analysis.ErrCommandName},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := analysis.LoadPolicy([]byte("version: v1\nanalyzers:\n"+tc.args), run)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.specs, got.Analyzers)
		})
	}
}
