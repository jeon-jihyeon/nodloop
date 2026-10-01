package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// A record dir with one approved item past its deadline and a conversation review that applied it and a refuted outcome
// The item cites one paragraph of the demo and one that does not exist
func loopRecords(t *testing.T, now time.Time) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	old := now.AddDate(0, 0, -knowledge.ReviewDays)
	store, err := knowledgefile.New(dir)
	require.NoError(t, err)
	require.NoError(t, store.Append(ctx, knowledge.Knowledge{
		ID: "k-lag", Version: 1, Kind: knowledge.KindMeaning, Content: "conversions lag clicks",
		Scope: knowledge.Scope{Scope: evidence.Scope{
			ChangeContexts: []evidence.Context{evidence.ContextPlannedChange, evidence.ContextNoKnownChange},
		}},
		Evidence: knowledge.Evidence{ParagraphIDs: []string{
			"metric-anomaly-investigation#Metric anomaly investigation/Check the segment#1", "gone#1",
		}},
		Basis: knowledge.BasisStated, Status: knowledge.StatusApproved, Author: "author", Approver: "ann",
		ApprovedAt: old, Time: old,
	}))
	in, err := json.Marshal(map[string]any{
		"mode": diagnose.ModeInteractive, "change_context": evidence.ContextPlannedChange,
		"knowledge": []diagnose.AppliedKnowledge{{ID: "k-lag", Version: 1, Chars: 40}},
	})
	require.NoError(t, err)
	traces, err := tracefile.New(dir)
	require.NoError(t, err)
	require.NoError(t, traces.Append(ctx, trace.Trace{
		ID: "review", Name: trace.NameDiagnose, Subject: "tq-023", Ref: "ctx", Time: old, Input: in,
		Output: json.RawMessage(`{"status":"ready_for_review","causes":[{"summary":"s","paragraph_ids":["p#1"]}],"checks":[]}`),
	}))
	outcomes, err := feedbackfile.NewOutcomeStore(dir)
	require.NoError(t, err)
	require.NoError(t, outcomes.Append(ctx, feedback.Outcome{
		TraceID: "review", Result: feedback.ResultRefuted, Time: old, Reviewer: "ann",
	}))
	return dir
}

func TestRunKnowledgeLoop(t *testing.T) {
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{
			"health flags the refuted and stale version",
			[]string{"health"},
			want{0, `^\[\{"id":"k-lag","version":1,"status":"approved","applied":1,.*"refuted":1,.*"retire_candidate":true,.*"stale":true\}\]\n$`, `^$`},
		},
		{
			"audit names the broken paragraph",
			[]string{"audit"},
			want{0, `^\[\{"id":"k-lag","version":1,"field":"paragraph_ids","reference":"gone#1"\}\]\n$`, `^$`},
		},
		{"list marks the stale version", []string{"list", "--stale"}, want{0, "^k-lag\tv1\tapproved\tmeaning\tconversions lag clicks\tstale=true\n$", `^$`}},
		{"reaffirm takes the approved version", []string{"reaffirm", "k-lag", "--approver", "ann"}, want{0, "^k-lag\tv1\treaffirmed\tann\n$", `^$`}},
		{"reaffirm needs a name", []string{"reaffirm", "k-lag"}, want{1, `^$`, `^nodloop knowledge: reaffirm: an id and --approver is required`}},
		{"reaffirm of an unknown id fails", []string{"reaffirm", "nope", "--approver", "ann"}, want{1, `^$`, `^nodloop knowledge: .*not found: nope`}},
		{
			"narrow drops the refuted change context",
			[]string{"narrow", "k-lag", "--version", "1", "--author", "jed"},
			want{0, "^k-lag\tv2\tcandidate\tscope change contexts no_known_change\texceptions \\[\\]\nfolder\t", `^$`},
		},
		{"narrow needs a version", []string{"narrow", "k-lag"}, want{1, `^$`, `^nodloop knowledge: narrow: an id and --version is required`}},
		{
			"promote of a refuted version names the missing confirmation",
			[]string{"promote", "k-lag", "--version", "1"},
			want{1, `^$`, `^nodloop knowledge: ` + knowledge.ErrPromoteInvalid.Error() + `: k-lag v1 has no confirmed review\n$`},
		},
		{"promote needs a version", []string{"promote", "k-lag"}, want{1, `^$`, `^nodloop knowledge: promote: an id and --version is required`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := testkit.Open(t).Clock.Now
			records := loopRecords(t, now())
			getenv := func(k string) string {
				return map[string]string{envFileDir: testkit.DemoDir(t), envRecordDir: records}[k]
			}
			var stdout, stderr bytes.Buffer
			got := runKnowledge(tc.args, getenv, nil, now, &stdout, &stderr)
			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}

// A later check that confirmed the review replaces the refutation so the version may be promoted
func TestRunKnowledgePromote(t *testing.T) {
	now := testkit.Open(t).Clock.Now
	records := loopRecords(t, now())
	outcomes, err := feedbackfile.NewOutcomeStore(records)
	require.NoError(t, err)
	require.NoError(t, outcomes.Append(context.Background(), feedback.Outcome{
		TraceID: "review", Result: feedback.ResultConfirmed, Time: now(), Reviewer: "ann",
	}))
	getenv := func(k string) string {
		return map[string]string{envFileDir: testkit.DemoDir(t), envRecordDir: records}[k]
	}
	var stdout, stderr bytes.Buffer

	got := runKnowledge([]string{"promote", "k-lag", "--version", "1", "--author", "jed"}, getenv, nil, now, &stdout, &stderr)

	assert.Equal(t, 0, got)
	assert.Regexp(t, "^k-lag\tv2\tcandidate\tbasis verified\toutcomes \\[review\\]\nfolder\t", stdout.String())
	assert.Empty(t, stderr.String())
}

// An item scoped to the one change context where its review was refuted has nothing left to narrow
// The error names the retire command and never retires on its own
func TestRunKnowledgeNarrowExhausted(t *testing.T) {
	ctx := context.Background()
	now := testkit.Open(t).Clock.Now
	records := loopRecords(t, now())
	store, err := knowledgefile.New(records)
	require.NoError(t, err)
	require.NoError(t, store.Append(ctx, knowledge.Knowledge{
		ID: "k-one", Version: 1, Kind: knowledge.KindMeaning, Content: "one context",
		Scope:    knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{evidence.ContextPlannedChange}}},
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"gone#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusApproved, Author: "author", Approver: "ann", ApprovedAt: now(), Time: now(),
	}))
	in, err := json.Marshal(map[string]any{
		"mode": diagnose.ModeInteractive, "change_context": evidence.ContextPlannedChange,
		"knowledge": []diagnose.AppliedKnowledge{{ID: "k-one", Version: 1, Chars: 20}},
	})
	require.NoError(t, err)
	traces, err := tracefile.New(records)
	require.NoError(t, err)
	require.NoError(t, traces.Append(ctx, trace.Trace{
		ID: "review-one", Name: trace.NameDiagnose, Subject: "tq-023", Ref: "ctx-one", Time: now(), Input: in,
		Output: json.RawMessage(`{"status":"ready_for_review","causes":[],"checks":[]}`),
	}))
	outcomes, err := feedbackfile.NewOutcomeStore(records)
	require.NoError(t, err)
	require.NoError(t, outcomes.Append(ctx, feedback.Outcome{
		TraceID: "review-one", Result: feedback.ResultRefuted, Time: now(), Reviewer: "ann",
	}))
	getenv := func(k string) string {
		return map[string]string{envFileDir: testkit.DemoDir(t), envRecordDir: records}[k]
	}
	var stdout, stderr bytes.Buffer

	got := runKnowledge([]string{"narrow", "k-one", "--version", "1"}, getenv, nil, now, &stdout, &stderr)

	assert.Equal(t, 1, got)
	assert.Empty(t, stdout.String())
	assert.Regexp(t, `^nodloop knowledge: knowledge: narrowing would leave no change context of the version: .*`+
		`Keep the version or retire it by name with nodloop knowledge retire k-one --version 1 --approver <name>\n`, stderr.String())
	all, err := store.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 2, "nothing is proposed or retired")
}

func TestRunQueue(t *testing.T) {
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"queue lists the pending review", []string{"--seed", "1"}, want{0, `^\{"items":\[\{"trace_id":"review",.*\],"seed":1\}\n$`, `^$`}},
		{"queue refuses a negative limit", []string{"--limit", "-1"}, want{1, `^$`, `limit must not be negative`}},
		{"queue takes no action", []string{"oops"}, want{1, `^$`, `unknown action "oops"`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := testkit.Open(t).Clock.Now
			records := loopRecords(t, now())
			getenv := func(k string) string {
				return map[string]string{envFileDir: testkit.DemoDir(t), envRecordDir: records}[k]
			}
			var stdout, stderr bytes.Buffer

			got := runQueue(tc.args, getenv, now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}

func TestRunReport(t *testing.T) {
	type want struct {
		code   int
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args []string
		want want
	}{
		{"online report reads the records", []string{"online"}, want{0, `^\{"since":.*"weeks":\[\].*\}\n$`, `^$`}},
		{"online report since a bad time fails", []string{"online", "--since", "yesterday"}, want{1, `^$`, `cannot parse`}},
		{"report needs an action", nil, want{1, `^$`, `^nodloop report: an action is required`}},
		{"report knows only online", []string{"weekly", "--since", "x"}, want{1, `^$`, `^nodloop report: unknown action "weekly"`}},
		{"online report takes no argument", []string{"online", "weekly"}, want{1, `^$`, `^nodloop report: unknown action "weekly"`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := testkit.Open(t).Clock.Now
			records := loopRecords(t, now())
			getenv := func(k string) string {
				return map[string]string{envFileDir: testkit.DemoDir(t), envRecordDir: records}[k]
			}
			var stdout, stderr bytes.Buffer

			got := runReport(tc.args, getenv, now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, tc.want.stdout, stdout.String())
			assert.Regexp(t, tc.want.stderr, stderr.String())
		})
	}
}

func TestRunFeedbackAudit(t *testing.T) {
	tcs := []struct {
		name string
		args []string
		want bool
	}{
		{"a verdict without the flag", nil, false},
		{"a verdict on an audit sample", []string{"--audit"}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			now := testkit.Open(t).Clock.Now
			records := loopRecords(t, now())
			getenv := func(k string) string {
				return map[string]string{envFileDir: testkit.DemoDir(t), envRecordDir: records}[k]
			}
			args := append([]string{"add", "--trace", "review", "--verdict", "approve"}, tc.args...)
			var stderr bytes.Buffer
			require.Zero(t, runFeedback(args, getenv, now, io.Discard, &stderr), stderr.String())
			store, err := feedbackfile.New(records)
			require.NoError(t, err)
			got, err := store.List(ctx, feedback.Filter{})
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].Audit)
		})
	}
}
