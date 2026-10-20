package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

var errStore = errors.New("store failed")

// Reads or appends that fail after the allowed ones succeed
func failAfter(allowed int) testkit.Reads {
	return testkit.Reads{Allowed: allowed, Err: errStore}
}

// Which stores of a server fail
// A zero value fails nothing
type failing struct {
	appends, lists, items, verdicts testkit.Reads
}

// Serves the records of st to the caller over stores that fail as f says
func (f failing) serve(t *testing.T, st testkit.Stores, caller mcp.Caller) testkit.Client {
	t.Helper()
	traces := &testkit.FlakyTraces{Store: st.Traces, Appends: f.appends, Lists: f.lists}
	verdicts := &testkit.FlakyFeedback{Store: st.Feedback, Reads: f.verdicts}
	items := &testkit.FlakyKnowledge{Store: st.Knowledge, Reads: f.items}
	ledger := knowledge.NewLedger(items, vetofile.NewApprovedFile("", st.Dir), st.Clock.Now, func(prefix string) string { return prefix + "generated" })
	source := schema.Source{Traces: traces, Verdicts: verdicts, Outcomes: st.Outcomes, Knowledge: items}
	srv := mcp.New(traces, verdicts, st.Outcomes, ledger, source, compact.New(ledger, traces, verdicts, traces, st.Clock.Now),
		extract.New(ledger, traces, verdicts, st.Clock.Now), st.Clock.Now, mcp.NewSession(st.Clock.Now()), "nodloop", "", caller, mcp.Policy{})
	return testkit.Connect(t, mcp.NewHost(func(context.Context) (*mcp.Server, error) { return srv, nil }, "test", "").ServeTransport)
}

// The arguments of a call after the records it reads were written by a client of ann that proves her
type setup func(t *testing.T, st testkit.Stores, c testkit.Client) map[string]any

// A candidate to approve
func toApprove(t *testing.T, _ testkit.Stores, c testkit.Client) map[string]any {
	t.Helper()
	candidate(t, c)
	return map[string]any{"id": "short", "version": 1, "approver": "ann"}
}

// A candidate to approve in labels a run carries
func toApproveIn(t *testing.T, st testkit.Stores, c testkit.Client) map[string]any {
	t.Helper()
	in := toApprove(t, st, c)
	in["scope"] = map[string]any{"labels": map[string]any{"repo": []any{"other"}}}
	return in
}

// An approved version to reaffirm
func toReaffirm(t *testing.T, st testkit.Stores, c testkit.Client) map[string]any {
	t.Helper()
	in := toApprove(t, st, c)
	require.NoError(t, c.Run(t, "approve", in))
	return in
}

// A compaction whose coverage passed
func toApproveCompaction(t *testing.T, st testkit.Stores, _ testkit.Client) map[string]any {
	t.Helper()
	return map[string]any{"compaction": checkedCompaction(t, st), "approver": "ann"}
}

// A candidate to keep through an intervention
func toKeep(t *testing.T, st testkit.Stores, c testkit.Client) map[string]any {
	t.Helper()
	toApprove(t, st, c)
	return map[string]any{"action": "judge", "target": "short:1", "choice": "keep"}
}

// A run to record
func toRun(*testing.T, testkit.Stores, testkit.Client) map[string]any {
	return map[string]any{"producer": "session", "output": "x"}
}

// A run of repo nodloop the person rejected to propose a lesson from
func toProposeFrom(t *testing.T, _ testkit.Stores, c testkit.Client) map[string]any {
	t.Helper()
	run := recordRun(t, c, "nodloop")
	require.NoError(t, c.Run(t, "feedback", map[string]any{"trace_id": run, "verdict": "reject", "reason": "too long", "reviewer": "ann"}))
	return map[string]any{"id": "short", "kind": "judgment", "content": "give only the conclusion", "from": run}
}

// A place of producer session to ask about
func toAskThePlace(*testing.T, testkit.Stores, testkit.Client) map[string]any {
	return map[string]any{"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}}}
}

// A shell command to check
func toCheckCall(*testing.T, testkit.Stores, testkit.Client) map[string]any {
	return map[string]any{"tool": "Bash", "input": map[string]any{"command": "ls"}}
}

// Nothing to write before the call
func toRead(*testing.T, testkit.Stores, testkit.Client) map[string]any {
	return map[string]any{}
}

// A store that fails a tool answers its failure as the error of the call
// A judgment that landed before the failure says so
func TestServerStoreFailures(t *testing.T) {
	person, stdio := mcp.Caller{Person: "ann", Proves: true}, mcp.Caller{}
	type args struct {
		tool   string
		init   setup
		caller mcp.Caller
		fail   failing
	}
	type want struct {
		errText string
		// A part of the answer of a call that succeeded
		answer string
	}
	failed := want{errText: errStore.Error()}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a run whose append fails", args{"run", toRun, person, failing{appends: failAfter(0)}}, failed},
		{"knowledge_for over a ledger that cannot be read", args{"knowledge_for", toAskThePlace, person, failing{items: failAfter(0)}}, failed},
		{"check_call over a ledger that cannot be read", args{"check_call", toCheckCall, person, failing{items: failAfter(0)}}, failed},
		{"a propose whose verdicts cannot be read", args{"propose", toProposeFrom, person, failing{verdicts: failAfter(0)}}, failed},
		{"a propose whose runs cannot be read", args{"propose", toProposeFrom, person, failing{lists: failAfter(0)}}, failed},
		{"an approval whose intervention cannot be appended says it landed", args{"approve", toApprove, person, failing{appends: failAfter(0)}},
			want{errText: "short v1 is approved and its intervention was not recorded: " + errStore.Error()}},
		{"an approval in labels whose runs cannot be read", args{"approve", toApproveIn, person, failing{lists: failAfter(0)}}, failed},
		{"an approval whose folder cannot be read rides on the answer", args{"approve", toApprove, person, failing{items: failAfter(0)}},
			want{answer: `"folder_error":"` + errStore.Error()}},
		{"a proposal of an approval over a ledger that cannot be read", args{"approve", toApprove, stdio, failing{items: failAfter(0)}}, failed},
		{"a proposal of an approval in labels whose runs cannot be read", args{"approve", toApproveIn, stdio, failing{lists: failAfter(0)}}, failed},
		{"a proposal of an approval whose intervention cannot be appended", args{"approve", toApprove, stdio, failing{appends: failAfter(0)}}, failed},
		{"a reaffirm whose intervention cannot be appended", args{"reaffirm", toReaffirm, person, failing{appends: failAfter(0)}}, failed},
		{"a compaction approval whose intervention cannot be appended says it landed",
			args{"approve_compaction", toApproveCompaction, person, failing{appends: failAfter(0)}},
			want{errText: "is approved and its intervention was not recorded: " + errStore.Error()}},
		{"a proposal of a compaction approval whose intervention cannot be appended",
			args{"approve_compaction", toApproveCompaction, stdio, failing{appends: failAfter(0)}}, failed},
		{"an intervention whose record cannot be appended", args{"intervene", toKeep, person, failing{appends: failAfter(0)}}, failed},
		{"the records of a store that cannot be read", args{"records", func(*testing.T, testkit.Stores, testkit.Client) map[string]any {
			return map[string]any{"kind": "run"}
		}, person, failing{lists: failAfter(0)}}, failed},
		{"the queue of runs that cannot be read", args{"queue", toRead, person, failing{lists: failAfter(0)}}, failed},
		{"the health of runs that cannot be read", args{"knowledge_health", toRead, person, failing{lists: failAfter(0)}}, failed},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			in := tc.args.init(t, st, testkit.Connect(t, serve(st, person)))
			c := tc.args.fail.serve(t, st, tc.args.caller)

			got, err := c.Text(t, tc.args.tool, in)

			assert.Contains(t, errText(err), tc.want.errText)
			assert.Contains(t, got, tc.want.answer)
		})
	}
}

// A failed export of approved.md rides on the answer of each call that decides since the decision landed
func TestServerExportFailureRidesOnTheAnswer(t *testing.T) {
	type args struct {
		tool string
		init setup
	}
	tcs := []struct {
		name string
		args args
		// The versions a run of repo nodloop receives after the call
		want []string
	}{
		{"an approval", args{"approve", toApprove}, []string{"short v1"}},
		{"an intervention", args{"intervene", toKeep}, []string{"short v1"}},
		{"a compaction approval", args{"approve_compaction", toApproveCompaction}, []string{"a v2"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			c := testkit.Connect(t, serve(st, mcp.Caller{Person: "ann", Proves: true}))
			in := tc.args.init(t, st, c)
			// A folder in place of the rules file the approvals before the call wrote
			require.NoError(t, os.RemoveAll(knowledgefile.RulesPath(st.Dir)))
			require.NoError(t, os.Mkdir(knowledgefile.RulesPath(st.Dir), 0o700))
			var got struct {
				ExportError string `json:"export_error"`
			}

			err := c.Call(t, tc.args.tool, in, &got)

			require.NoError(t, err)
			assert.Contains(t, got.ExportError, knowledge.ErrExport.Error())
			assert.Equal(t, tc.want, received(t, c))
		})
	}
}

// The versions a run of producer session in repo nodloop receives as id vN
func received(t *testing.T, c testkit.Client) []string {
	t.Helper()
	var got struct {
		Items []struct {
			ID      string `json:"id"`
			Version int    `json:"version"`
		} `json:"items"`
	}
	require.NoError(t, c.Call(t, "knowledge_for", map[string]any{"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}}}, &got))
	var out []string
	for _, it := range got.Items {
		out = append(out, fmt.Sprintf("%s v%d", it.ID, it.Version))
	}
	return out
}
