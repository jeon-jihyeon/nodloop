package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// A record dir where items a and b rest on rejected reviews of tq-001 and tq-017
// The demo labels expect no_action for tq-001 and hold for tq-017
func compactionEnv(t *testing.T) func(string) string {
	t.Helper()
	ctx := context.Background()
	records, home := t.TempDir(), t.TempDir()
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	traces, err := tracefile.New(records)
	require.NoError(t, err)
	verdicts, err := feedbackfile.New(records)
	require.NoError(t, err)
	for _, tr := range []trace.Trace{{ID: "t1", Subject: "tq-001"}, {ID: "t2", Subject: "tq-017"}} {
		tr.Name, tr.Time, tr.Output = trace.NameDiagnose, at, json.RawMessage(`{"status":"hold"}`)
		require.NoError(t, traces.Append(ctx, tr))
		fb, err := feedback.New(tr.ID, feedback.VerdictReject, "wrong", nil, "", at)
		require.NoError(t, err)
		require.NoError(t, verdicts.Append(ctx, fb))
	}
	items := filepath.Join(t.TempDir(), "items.jsonl")
	require.NoError(t, os.WriteFile(items, []byte(
		`{"id":"a","version":1,"kind":"meaning","content":"lag","scope":{"metrics":["conversion_count"]},`+
			`"evidence":{"feedback_trace_ids":["t1"]},"basis":"stated","status":"approved","approver":"ann","author":"ann",`+
			`"time":"2026-09-28T00:00:00Z"}`+"\n"+
			`{"id":"b","version":1,"kind":"meaning","content":"basis","scope":{"metrics":["conversion_count"]},`+
			`"evidence":{"feedback_trace_ids":["t2"]},"basis":"stated","status":"approved","approver":"ann","author":"ann",`+
			`"time":"2026-09-28T00:00:00Z"}`+"\n"), 0o600))
	getenv := func(k string) string {
		return map[string]string{envFileDir: testkit.DemoDir(t), envRecordDir: records, "HOME": home}[k]
	}
	var stderr bytes.Buffer
	code := runKnowledge([]string{"import", "--file", items}, getenv, nil, testkit.Open(t).Clock.Now, io.Discard, &stderr)
	require.Equal(t, 0, code, stderr.String())
	return getenv
}

// A review prompt of the event
func eventPrompt(event string) gomock.Matcher {
	return gomock.Cond(func(r llm.Request) bool { return strings.Contains(r.Prompt, "# Event "+event+"\n") })
}

// The model drafts a merge of a and b and reviews each replay event with its expected status
func compactionClient(t *testing.T) *llmmock.MockClient {
	t.Helper()
	review := func(status string) llm.Response {
		return llm.Response{Output: json.RawMessage(
			`{"status":"` + status + `","observations":[],"causes":[],"checks":[],"open_questions":[],"hold_reasons":["gap"]}`,
		)}
	}
	merged := `{"items":[{"kind":"meaning","content":"lag and basis","metrics":["conversion_count"],"from":["a","b"]}]}`
	drafting := gomock.Cond(func(r llm.Request) bool { return r.System == compact.Rules })
	client := llmmock.NewMockClient(gomock.NewController(t))
	client.EXPECT().Complete(gomock.Any(), drafting).Return(llm.Response{Output: json.RawMessage(merged)}, nil).AnyTimes()
	client.EXPECT().Complete(gomock.Any(), eventPrompt("tq-001")).Return(review("no_action"), nil).AnyTimes()
	client.EXPECT().Complete(gomock.Any(), eventPrompt("tq-017")).Return(review("hold"), nil).AnyTimes()
	return client
}

func TestRunCompact(t *testing.T) {
	type want struct {
		code           int
		stdout, stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"compact without an id fails", []string{"compact"}, want{1, `^$`, `^nodloop knowledge: compact: an id is required`}},
		{
			"compact drafts through the model and proposes",
			[]string{"compact", "a", "--model", "haiku"},
			want{0, `^folder\ta\t2 items\t2 replay events\t0 unverifiable events\ncompaction\tc-[0-9a-f]+\n` +
				`new\tk-[0-9a-f]+\tv1\tcandidate\tmeaning\tlag and basis\nold\ta\tv1\tapproved\tmeaning\tlag\n` +
				`old\tb\tv1\tapproved\tmeaning\tbasis\nnext\tnodloop knowledge replay c-[0-9a-f]+ reviews 2 events`, `^$`},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv := compactionEnv(t)
			var stdout, stderr bytes.Buffer

			got := runKnowledge(tc.args, getenv, compactionClient(t), testkit.Open(t).Clock.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got, stderr.String())
			assert.Regexp(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}

// Every case starts from a compaction of a and b proposed through compact
// `{id}` stands for the compaction id that compact printed
func TestRunCompaction(t *testing.T) {
	type args struct {
		// Commands that must succeed after compact and before the one under test
		before [][]string
		args   []string
	}
	type want struct {
		code           int
		stdout, stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"compaction shows the items and no replay yet",
			args{args: []string{"compaction", "{id}"}},
			want{0, `event\ttq-001\texpected no_action\tgot -\t-\nevent\ttq-017\texpected hold\tgot -\t-\nreplay\tpassed=false\n$`, `^$`},
		},
		{
			"approve before a replay is refused with the events",
			args{args: []string{"approve-compaction", "{id}", "--approver", "jed"}},
			want{1, `^$`, `tq-001 expects no_action and has no replay`},
		},
		{
			"approve without an approver fails",
			args{args: []string{"approve-compaction", "{id}"}},
			want{1, `^$`, `--approver is required`},
		},
		{
			"replay of an event outside the replay is refused",
			args{args: []string{"replay", "{id}", "--events", "tq-002"}},
			want{1, `^replay\t{id}\t1 events\n$`, `event is not a replay event of the compaction: tq-002`},
		},
		{
			"replay without events prints the event count first and the result per event",
			args{args: []string{"replay", "{id}", "--parallel", "1"}},
			want{0, `^replay\t{id}\t2 events\nevent\ttq-001\texpected no_action\tgot no_action\t\S+\n` +
				`event\ttq-017\texpected hold\tgot hold\t\S+\nreplay\tpassed=true\n$`, `tq-001\treplay\tno_action`},
		},
		{
			"replay of named events drops an empty entry and an empty flag",
			args{args: []string{"replay", "{id}", "--events", "tq-001,", "--events", ""}},
			want{0, `^replay\t{id}\t1 events\nevent\ttq-001\texpected no_action\tgot no_action\t\S+\n` +
				`event\ttq-017\texpected hold\tgot -\t-\nreplay\tpassed=false\n$`, `tq-001\treplay\tno_action`},
		},
		{
			"replay in the shape of the mcp replay command still parses events appended after it",
			args{args: []string{"replay", "{id}", "--data-dir", "{data}", "--record-dir", "{records}", "--events", "tq-001"}},
			want{0, `^replay\t{id}\t1 events\nevent\ttq-001\texpected no_action\tgot no_action\t\S+\n` +
				`event\ttq-017\texpected hold\tgot -\t-\nreplay\tpassed=false\n$`, `tq-001\treplay\tno_action`},
		},
		{
			"approve after the replay approves the new item and retires the old ones",
			args{before: [][]string{{"replay", "{id}"}}, args: []string{"approve-compaction", "{id}", "--approver", "jed"}},
			want{0, `new\tk-[0-9a-f]+\tv1\tapproved\tmeaning\tlag and basis\nold\ta\tv1\tretired\tmeaning\tlag\n` +
				`old\tb\tv1\tretired\tmeaning\tbasis\n$`, `^$`},
		},
	}
	proposed := regexp.MustCompile(`compaction\t(c-[0-9a-f]+)\n`)
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, client, now := compactionEnv(t), compactionClient(t), testkit.Open(t).Clock.Now
			var compacted, stdout, stderr bytes.Buffer
			require.Equal(t, 0, runKnowledge([]string{"compact", "a"}, getenv, client, now, &compacted, &stderr), stderr.String())
			m := proposed.FindStringSubmatch(compacted.String())
			require.Len(t, m, 2, compacted.String())
			r := strings.NewReplacer("{id}", m[1], "{data}", getenv(envFileDir), "{records}", getenv(envRecordDir))
			withID := func(args []string) []string {
				out := make([]string, 0, len(args))
				for _, a := range args {
					out = append(out, r.Replace(a))
				}
				return out
			}
			for _, before := range tc.args.before {
				require.Equal(t, 0, runKnowledge(withID(before), getenv, client, now, io.Discard, &stderr), stderr.String())
			}
			stderr.Reset()

			got := runKnowledge(withID(tc.args.args), getenv, client, now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got, stderr.String())
			assert.Regexp(t, r.Replace(tc.want.stdout), stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}
