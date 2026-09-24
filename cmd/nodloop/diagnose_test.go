package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestRunDiagnose(t *testing.T) {
	confirm := diagnose.Check{
		Step: "confirm the signal", Purpose: "signal",
		ParagraphIDs: []string{"metric-anomaly-investigation#Metric anomaly investigation/Confirm the signal#1"},
	}
	segment := diagnose.Check{
		Step: "check the segment", Purpose: "segment",
		ParagraphIDs: []string{"metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1"},
	}
	ready := diagnose.Diagnosis{
		Status: diagnose.StatusReadyForReview, Observations: []string{"clicks up"},
		Causes:        []diagnose.Cause{{Summary: "low quality traffic", ParagraphIDs: segment.ParagraphIDs}},
		Checks:        []diagnose.Check{confirm, segment},
		OpenQuestions: []string{},
	}
	hold := diagnose.Diagnosis{
		Status: diagnose.StatusHold, Observations: []string{}, Causes: []diagnose.Cause{},
		Checks: []diagnose.Check{confirm, segment}, OpenQuestions: []string{}, HoldReasons: []string{"gap in the window"},
	}
	readyJSON, err := json.Marshal(ready)
	require.NoError(t, err)
	readyShown, err := json.MarshalIndent(ready, "", "  ")
	require.NoError(t, err)
	holdJSON, err := json.Marshal(hold)
	require.NoError(t, err)
	holdShown, err := json.MarshalIndent(hold, "", "  ")
	require.NoError(t, err)
	regular := filepath.Join(t.TempDir(), "regular")
	require.NoError(t, os.WriteFile(regular, nil, 0o600))
	data := testkit.DemoDir(t)
	sonnetReview := []trace.Trace{{Name: trace.NameDiagnose, Subject: "tq-005", Model: "sonnet"}}
	type args struct {
		args []string
		// The model the requests carry
		model string
		// Replies of the model in call order
		replies []llm.Response
		err     error
	}
	type want struct {
		code   int
		stdout string
		// Regexp matched against stderr
		stderr string
		// Diagnose traces in the record dir reduced to what the flags set
		reviews []trace.Trace
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"ready prints the diagnosis and the trace",
			args{
				[]string{"--event", "tq-005", "--model", "sonnet"}, "sonnet",
				[]llm.Response{{Output: readyJSON, CostUSD: 0.01}}, nil,
			},
			want{0, string(readyShown) + "\n", "^trace [0-9a-f]+\n$", sonnetReview},
		},
		{
			"all knowledge mode prints the diagnosis",
			args{
				[]string{"--event", "tq-005", "--model", "sonnet", "--knowledge", "all"}, "sonnet",
				[]llm.Response{{Output: readyJSON}}, nil,
			},
			want{0, string(readyShown) + "\n", "^trace [0-9a-f]+\n$", sonnetReview},
		},
		{
			"session and tags reach the trace",
			args{
				[]string{"--event", "tq-017", "--examples", "0", "--session", "s1", "--tag", "feedback:off", "--tag", "x"}, "",
				[]llm.Response{{Output: holdJSON, CostUSD: 0.01}}, nil,
			},
			want{
				0, string(holdShown) + "\n", "^trace [0-9a-f]+\n$",
				[]trace.Trace{{Name: trace.NameDiagnose, Subject: "tq-017", SessionID: "s1", Tags: []string{"feedback:off", "x"}}},
			},
		},
		{
			"model failure records the failed review",
			args{[]string{"--event", "tq-005", "--model", "sonnet"}, "sonnet", []llm.Response{{}}, assert.AnError},
			want{1, "", "^nodloop diagnose: .*" + assert.AnError.Error(), sonnetReview},
		},
		{
			"missing event flag fails",
			args{nil, "", nil, nil},
			want{1, "", "^nodloop diagnose: --event is required\n\nusage:", nil},
		},
		{
			"unknown knowledge mode fails",
			args{[]string{"--event", "tq-005", "--knowledge", "bogus"}, "", nil, nil},
			want{1, "", "^nodloop diagnose: " + diagnose.ErrUnknownKnowledgeMode.Error() + `: "bogus"\n$`, nil},
		},
		{
			"unknown event fails",
			args{[]string{"--event", "nope"}, "", nil, nil},
			want{1, "", `^nodloop diagnose: .*not found`, nil},
		},
		{
			"record dir that is a file fails",
			args{[]string{"--event", "tq-005", "--record-dir", regular}, "", nil, nil},
			want{1, "", "^nodloop diagnose: record dir: ", nil},
		},
		{
			"unknown source fails",
			args{[]string{"--event", "tq-005", "--source", "postgres"}, "", nil, nil},
			want{1, "", `^nodloop diagnose: unknown source: "postgres"\n$`, nil},
		},
		{
			"unknown flag fails",
			args{[]string{"--nope"}, "", nil, nil},
			want{1, "", "^flag provided but not defined: -nope\n", nil},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records := t.TempDir()
			getenv := func(k string) string {
				return map[string]string{envFileDir: data, envRecordDir: records}[k]
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			for _, res := range tc.args.replies {
				req := gomock.Cond(func(req llm.Request) bool {
					return req.Model == tc.args.model && string(req.Schema) == diagnose.Schema && req.System == diagnose.Rules
				})
				client.EXPECT().Complete(gomock.Any(), req).Return(res, tc.args.err)
			}
			var stdout, stderr bytes.Buffer

			got := runDiagnose(tc.args.args, getenv, client, testkit.Open(t).Clock.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Equal(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
			store, err := tracefile.New(records)
			require.NoError(t, err)
			traces, err := store.List(ctx, trace.Filter{Name: trace.NameDiagnose})
			require.NoError(t, err)
			var reviews []trace.Trace
			for _, tr := range traces {
				reviews = append(reviews, trace.Trace{
					Name: tr.Name, Subject: tr.Subject, SessionID: tr.SessionID, Model: tr.Model, Tags: tr.Tags,
				})
			}
			assert.Equal(t, tc.want.reviews, reviews)
		})
	}
}
