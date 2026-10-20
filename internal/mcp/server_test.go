package mcp_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A server under a key of the person ann that proves her so the tests of the tools reach the ledger
// The calls that prove no person are tested in TestServerJudgmentProof
func newServer(st testkit.Stores, session string, policy mcp.Policy) *mcp.Server {
	compactor := compact.New(st.Ledger, st.Traces, st.Feedback, st.Traces, st.Clock.Now)
	return mcp.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, compactor, extract.New(st.Ledger, st.Traces, st.Feedback, st.Clock.Now),
		st.Clock.Now, session, "nodloop", "--record-dir /records", mcp.Caller{Person: "ann", Proves: true}, policy)
}

func connect(t *testing.T, st testkit.Stores) testkit.Client {
	t.Helper()
	return connectWith(t, st, mcp.Policy{})
}

func connectWith(t *testing.T, st testkit.Stores, policy mcp.Policy) testkit.Client {
	t.Helper()
	srv := newServer(st, mcp.NewSession(st.Clock.Now()), policy)
	return testkit.Connect(t, mcp.NewHost(func(context.Context) (*mcp.Server, error) { return srv, nil }, "test", "").ServeTransport)
}

// Records one run of producer session in the repo and answers its trace id
func recordRun(t *testing.T, c testkit.Client, repo string) string {
	t.Helper()
	var run struct {
		TraceID string `json:"trace_id"`
	}
	require.NoError(t, c.Call(t, "run", map[string]any{
		"producer": "session", "labels": map[string]any{"repo": []any{repo}}, "output": "cd repo && git status",
	}, &run))
	return run.TraceID
}

// The loop of one run: a run, an edit of it, a proposal from it, an approval and the items the next run of the place gets
func TestServerLoop(t *testing.T) {
	st := testkit.Open(t)
	c := connect(t, st)
	runID := recordRun(t, c, "nodloop")
	recordRun(t, c, "other")

	err := c.Run(t, "propose", map[string]any{"kind": "judgment", "content": "x", "from": runID})
	assert.ErrorContains(t, err, knowledge.ErrNotCorrected.Error())
	require.NoError(t, c.Run(t, "feedback", map[string]any{
		"trace_id": runID, "verdict": "edit", "reason_code": "other", "edited_output": "git -C repo status",
	}))
	var proposed struct {
		ID    string             `json:"id"`
		Scope knowledge.RunScope `json:"scope"`
	}
	require.NoError(t, c.Call(t, "propose", map[string]any{
		"id": "git-c", "kind": "judgment", "content": "use git -C instead of cd", "from": runID,
	}, &proposed))
	assert.Equal(t, knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}, proposed.Scope)
	require.NoError(t, c.Run(t, "approve", map[string]any{"id": "git-c", "version": 1, "approver": "ann"}))
	tcs := []struct {
		name string
		args string
		want []string
	}{
		{"a run of the repo gets the item", "nodloop", []string{"git-c"}},
		{"a run of another repo gets nothing", "other", nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			require.NoError(t, c.Call(t, "knowledge_for", map[string]any{"producer": "session", "labels": map[string]any{"repo": []any{tc.args}}}, &got))
			var ids []string
			for _, it := range got.Items {
				ids = append(ids, it.ID)
			}
			assert.Equal(t, tc.want, ids)
		})
	}
}

// approve takes the scope the user picked over the proposed one
func TestServerApproveScope(t *testing.T) {
	type args struct {
		scope map[string]any
	}
	type want struct {
		err string
		// The repos whose runs get the item
		repos []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no scope approves it as proposed", args{}, want{repos: []string{"nodloop"}}},
		{"empty labels reach every repo", args{map[string]any{"labels": map[string]any{}}}, want{repos: []string{"nodloop", "other"}}},
		{"a recorded label moves it there", args{map[string]any{"labels": map[string]any{"repo": []any{"other"}}}}, want{repos: []string{"other"}}},
		{"a label no run carries is refused", args{map[string]any{"labels": map[string]any{"repo": []any{"nowhere"}}}},
			want{err: knowledge.ErrScopeUnobserved.Error()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := connect(t, testkit.Open(t))
			runID := recordRun(t, c, "nodloop")
			recordRun(t, c, "other")
			require.NoError(t, c.Run(t, "feedback", map[string]any{"trace_id": runID, "verdict": "reject", "reason_code": "form", "reason": "too long"}))
			require.NoError(t, c.Run(t, "propose", map[string]any{"id": "short", "kind": "judgment", "content": "give only the conclusion", "from": runID}))
			in := map[string]any{"id": "short", "version": 1, "approver": "ann"}
			if tc.args.scope != nil {
				in["scope"] = tc.args.scope
			}

			err := c.Run(t, "approve", in)

			if tc.want.err != "" {
				assert.ErrorContains(t, err, tc.want.err)
				return
			}
			require.NoError(t, err)
			var reached []string
			for _, repo := range []string{"nodloop", "other"} {
				var got struct {
					Items []any `json:"items"`
				}
				require.NoError(t, c.Call(t, "knowledge_for", map[string]any{"producer": "session", "labels": map[string]any{"repo": []any{repo}}}, &got))
				if len(got.Items) > 0 {
					reached = append(reached, repo)
				}
			}
			assert.Equal(t, tc.want.repos, reached)
		})
	}
}

func TestServerRefusals(t *testing.T) {
	st := testkit.Open(t)
	c := connect(t, st)
	runID := recordRun(t, c, "nodloop")
	tcs := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"a run without a producer", "run", map[string]any{"output": "x"}, `missing properties: ["producer"]`},
		{"a run with an empty producer", "run", map[string]any{"producer": "", "output": "x"}, trace.ErrProducerRequired.Error()},
		{"feedback on an unknown trace", "feedback", map[string]any{"trace_id": "nope", "verdict": "approve"}, record.ErrTraceNotFound.Error()},
		{"a label no run carries", "propose", map[string]any{
			"kind": "judgment", "content": "x", "trace_ids": []any{runID}, "producer": "session", "labels": map[string]any{"repo": []any{"nodlop"}},
		}, "no run of session carries repo=nodlop"},
		{"a proposal without a scope", "propose", map[string]any{"kind": "judgment", "content": "x", "trace_ids": []any{runID}}, knowledge.ErrScopeInvalid.Error()},
		{"a reason code outside the set", "feedback", map[string]any{"trace_id": runID, "verdict": "reject", "reason_code": "status2"}, "status2"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorContains(t, c.Run(t, tc.tool, tc.args), tc.want)
		})
	}
}

// The queue lists unjudged runs with their outputs and health counts what a person said about the runs that applied an item
func TestServerQueueAndHealth(t *testing.T) {
	st := testkit.Open(t)
	c := connect(t, st)
	runID := recordRun(t, c, "nodloop")
	var queue struct {
		Items []struct {
			TraceID string `json:"trace_id"`
		} `json:"items"`
		Outputs map[string]string `json:"outputs"`
	}
	require.NoError(t, c.Call(t, "queue", map[string]any{}, &queue))
	require.Len(t, queue.Items, 1)
	assert.Equal(t, "cd repo && git status", queue.Outputs[runID])
	require.NoError(t, c.Run(t, "outcome", map[string]any{"trace_id": runID, "result": "confirmed"}))
	var health struct {
		Items  []any `json:"items"`
		Issues []any `json:"issues"`
	}
	require.NoError(t, c.Call(t, "knowledge_health", map[string]any{}, &health))
	assert.Empty(t, health.Items)
	assert.Empty(t, health.Issues)
	var loopReport struct {
		Name   string `json:"name"`
		Report struct {
			Totals struct {
				Runs int `json:"runs"`
			} `json:"totals"`
		} `json:"report"`
	}
	require.NoError(t, c.Call(t, "report", map[string]any{"name": "loop"}, &loopReport))
	assert.Equal(t, "loop", loopReport.Name)
	assert.Equal(t, 1, loopReport.Report.Totals.Runs)
	assert.ErrorContains(t, c.Run(t, "report", map[string]any{"name": "nope"}), "nope")
}

// The items a and b approved in one place by a client whose AfterVerdict calls count in calls
func compactable(t *testing.T, calls *atomic.Int32) testkit.Client {
	t.Helper()
	c := connectWith(t, testkit.Open(t), mcp.Policy{AfterVerdict: func(context.Context) { calls.Add(1) }})
	runID := recordRun(t, c, "nodloop")
	for _, id := range []string{"a", "b"} {
		require.NoError(t, c.Run(t, "propose", map[string]any{
			"id": id, "kind": "judgment", "content": "rule " + id, "trace_ids": []any{runID},
			"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}},
		}))
		require.NoError(t, c.Run(t, "approve", map[string]any{"id": id, "version": 1, "approver": "ann"}))
	}
	return c
}

// A compaction of two items of one place: folder, draft, the conversation's coverage checks and approval
// AfterVerdict follows each approval and never a refused one
func TestServerCompaction(t *testing.T) {
	lost := []any{map[string]any{"old": "a", "covered_by": []any{"a"}}, map[string]any{"old": "b", "covered_by": []any{"a"}, "lost": []any{"rule b"}}}
	covered := []any{map[string]any{"old": "a", "covered_by": []any{"a"}}, map[string]any{"old": "b", "covered_by": []any{"a"}}}
	type want struct {
		// Whether each check passed in order
		passed  []bool
		err     error
		errText string
		// AfterVerdict calls after the approval of the compaction
		// The two approvals of the setup make two of them
		afterVerdicts int32
	}
	tcs := []struct {
		name string
		// The coverage of each check before the approval in order
		args [][]any
		want want
	}{
		{"an approval before any check is refused for no coverage", nil, want{nil, testkit.ErrTool, compact.ErrNoCoverage.Error(), 2}},
		{"a check that lost a rule leaves the approval refused", [][]any{lost}, want{[]bool{false}, testkit.ErrTool, knowledge.ErrCoverageNotPassed.Error() + ": b v1 loses rule b", 2}},
		{"a check that covers every item after one that lost a rule lets the approval through", [][]any{lost, covered}, want{[]bool{false, true}, nil, "", 3}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			c := compactable(t, &calls)
			var folder struct {
				Anchor string `json:"anchor"`
				Items  []struct {
					ID string `json:"id"`
				} `json:"items"`
				Rules string `json:"rules"`
			}
			require.NoError(t, c.Call(t, "compaction", map[string]any{"id": "a"}, &folder))
			var proposed struct {
				Compaction   string `json:"compaction"`
				CheckCommand string `json:"check_command"`
			}
			require.NoError(t, c.Call(t, "propose_compaction", map[string]any{"anchor": "a", "items": []any{map[string]any{
				"id": "a", "kind": "judgment", "content": "rule a and rule b", "from": []any{"a", "b"},
				"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}},
			}}}, &proposed))
			var passed []bool
			for _, items := range tc.args {
				var check struct {
					Passed bool `json:"passed"`
				}
				require.NoError(t, c.Call(t, "check_compaction", map[string]any{"compaction": proposed.Compaction, "items": items}, &check))
				passed = append(passed, check.Passed)
			}

			err := c.Run(t, "approve_compaction", map[string]any{"compaction": proposed.Compaction, "approver": "ann"})

			assert.Equal(t, "a", folder.Anchor)
			assert.Len(t, folder.Items, 2)
			assert.Equal(t, compact.Rules, folder.Rules)
			assert.Equal(t, "nodloop knowledge check "+proposed.Compaction+" --record-dir /records", proposed.CheckCommand)
			assert.Equal(t, tc.want.passed, passed)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, errText(err), tc.want.errText)
			assert.Equal(t, tc.want.afterVerdicts, calls.Load(), "the approvals and never a refused compaction")
		})
	}
}

// Each compaction tool refuses an id it does not know before it writes anything
func TestServerCompactionUnknown(t *testing.T) {
	tcs := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"a folder of an unknown item", "compaction", map[string]any{"id": "nope"}, knowledge.ErrNotFound.Error()},
		{"a draft of an unknown anchor", "propose_compaction", map[string]any{"anchor": "nope", "items": []any{}}, knowledge.ErrNotFound.Error()},
		{"an approval of an unknown compaction", "approve_compaction", map[string]any{"compaction": "nope", "approver": "ann"}, compact.ErrNoCoverage.Error() + ": nope"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			c := compactable(t, &calls)

			err := c.Run(t, tc.tool, tc.args)

			assert.ErrorIs(t, err, testkit.ErrTool)
			assert.Contains(t, errText(err), tc.want)
			assert.Equal(t, int32(2), calls.Load(), "only the approvals of the setup")
		})
	}
}

// The lesson the edits of the extraction tests teach
const lesson = "Run git with -C <dir> instead of changing into the directory"

// A critique that passes every question
var passing = map[string]any{"states": true, "holds": true, "fits": true, "why": "ok"}

// Three runs of repo nodloop the user edited the same way and a run id no trace has
func editedRuns(t *testing.T, c testkit.Client) []string {
	t.Helper()
	runs := []string{recordRun(t, c, "nodloop"), recordRun(t, c, "nodloop"), recordRun(t, c, "nodloop")}
	for _, id := range runs {
		require.NoError(t, c.Run(t, "feedback", map[string]any{
			"trace_id": id, "verdict": "edit", "reason_code": "approach", "edited_output": "git -C repo status",
		}))
	}
	return append(runs, "nope")
}

// No step before the call so no item relates to it
func extractedNothing(*testing.T, testkit.Client, []string) string {
	return ""
}

// The candidate the first edit proposed as an add
func extractedAdd(t *testing.T, c testkit.Client, runs []string) string {
	t.Helper()
	var added struct {
		Candidate struct {
			ID string `json:"id"`
		} `json:"candidate"`
	}
	require.NoError(t, c.Call(t, "propose_extraction", map[string]any{
		"from": runs[0], "relation": "add", "kind": "judgment", "content": lesson, "keys": []string{"repo"}, "critique": passing,
	}, &added))
	return added.Candidate.ID
}

// The candidate of the first edit corroborated by the second and approved
func extractedApproved(t *testing.T, c testkit.Client, runs []string) string {
	t.Helper()
	id := extractedAdd(t, c, runs)
	require.NoError(t, c.Run(t, "propose_extraction", map[string]any{
		"from": runs[1], "relation": "duplicate", "relates_to": id, "kind": "judgment", "content": lesson, "critique": passing,
	}))
	require.NoError(t, c.Run(t, "approve", map[string]any{"id": id, "version": 1, "approver": "ann"}))
	return id
}

// The extraction of an edit reads the edited output and the rules and the candidates waiting in the place
func TestServerExtraction(t *testing.T) {
	type args struct {
		// The steps before the call that answer the id of the candidate they proposed
		init func(t *testing.T, c testkit.Client, runs []string) string
		// The index of the run the call reads
		from int
	}
	type want struct {
		err     error
		errText string
		edited  string
		rules   string
		items   int
		// How many candidates wait and each is the one of the steps
		candidates int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a first edit reads its edited output and the rules with nothing reached or waiting", args{extractedNothing, 0},
			want{edited: "git -C repo status", rules: extract.Rules}},
		{"a second edit reads the candidate the first proposed as waiting", args{extractedAdd, 1},
			want{edited: "git -C repo status", rules: extract.Rules, candidates: 1}},
		{"a third edit after the approval reads the approved item as reached", args{extractedApproved, 2},
			want{edited: "git -C repo status", rules: extract.Rules, items: 1}},
		{"a run no trace has is refused", args{extractedNothing, 3}, want{err: testkit.ErrTool, errText: record.ErrTraceNotFound.Error()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := connect(t, testkit.Open(t))
			runs := editedRuns(t, c)
			id := tc.args.init(t, c, runs)
			var got struct {
				Edited     string   `json:"edited"`
				Rules      string   `json:"rules"`
				Items      []any    `json:"items"`
				Candidates []idOnly `json:"candidates"`
			}

			err := c.Call(t, "extraction", map[string]any{"from": runs[tc.args.from]}, &got)

			waiting := idsOf(got.Candidates)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, errText(err), tc.want.errText)
			assert.Equal(t, tc.want.edited, got.Edited)
			assert.Equal(t, tc.want.rules, got.Rules)
			assert.Len(t, got.Items, tc.want.items)
			assert.Len(t, waiting, tc.want.candidates)
			assert.Subset(t, []string{id}, waiting)
		})
	}
}

// An edit read through extraction becomes an add
// The same lesson from a second edit corroborates the waiting candidate
// From a third edit after approval it is a duplicate of the approved item
func TestServerProposeExtraction(t *testing.T) {
	type args struct {
		// The steps before the call that answer the id of the candidate the call relates to
		init func(t *testing.T, c testkit.Client, runs []string) string
		// The index of the run the call reads
		from     int
		relation string
		critique map[string]any
	}
	type want struct {
		err      error
		errText  string
		relation extract.Relation
		// The scope of the candidate the answer names and nil without one
		scope *knowledge.RunScope
		// The candidate and the related item of the answer as idOf names them
		candidate    string
		related      string
		corroborated bool
	}
	nodloop := &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an edit read through extraction becomes an add", args{extractedNothing, 0, "add", passing},
			want{relation: extract.RelationAdd, scope: nodloop, candidate: newID}},
		{"the same lesson from a second edit corroborates the waiting candidate", args{extractedAdd, 1, "duplicate", passing},
			want{relation: extract.RelationDuplicate, scope: nodloop, candidate: stepsID, related: stepsID, corroborated: true}},
		{"the same lesson after the approval is a duplicate of the approved item", args{extractedApproved, 2, "duplicate", passing},
			want{relation: extract.RelationDuplicate, related: stepsID}},
		{"a critique that does not hold is refused", args{extractedNothing, 2, "add", map[string]any{"states": true, "holds": false, "fits": true, "why": "x"}},
			want{err: testkit.ErrTool, errText: extract.ErrCriticRefused.Error()}},
		{"a run no trace has is refused", args{extractedNothing, 3, "add", passing}, want{err: testkit.ErrTool, errText: record.ErrTraceNotFound.Error()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := connect(t, testkit.Open(t))
			runs := editedRuns(t, c)
			id := tc.args.init(t, c, runs)
			var got struct {
				Relation  extract.Relation `json:"relation"`
				Related   idOnly           `json:"related"`
				Candidate struct {
					ID    string              `json:"id"`
					Scope *knowledge.RunScope `json:"scope"`
				} `json:"candidate"`
				Corroborated bool `json:"corroborated"`
			}

			err := c.Call(t, "propose_extraction", map[string]any{
				"from": runs[tc.args.from], "relation": tc.args.relation, "relates_to": id, "kind": "judgment", "content": lesson,
				"keys": []string{"repo"}, "critique": tc.args.critique,
			}, &got)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, errText(err), tc.want.errText)
			assert.Equal(t, tc.want.relation, got.Relation)
			assert.Equal(t, tc.want.scope, got.Candidate.Scope)
			assert.Equal(t, tc.want.candidate, idOf(got.Candidate.ID, id))
			assert.Equal(t, tc.want.related, idOf(got.Related.ID, id))
			assert.Equal(t, tc.want.corroborated, got.Corroborated)
		})
	}
}

// A lesson a person retired comes back through extraction: the read lists it with who retired it and why
// A duplicate of it proposes it again and an update of it is refused since only a duplicate names a retired version
func TestServerProposeExtractionRetired(t *testing.T) {
	type retired struct {
		ID       string           `json:"id"`
		Approver string           `json:"approver"`
		Reason   knowledge.Reason `json:"reason"`
	}
	type want struct {
		err     error
		errText string
		// The version of the candidate the answer names and zero without one
		version int
		next    string
	}
	tcs := []struct {
		name string
		args extract.Relation
		want want
	}{
		{"a duplicate proposes the retired version again", extract.RelationDuplicate, want{version: 2, next: "retired earlier and is proposed again"}},
		{"an update of it is refused", extract.RelationUpdate, want{err: testkit.ErrTool, errText: "only a duplicate names a retired version"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			c := connect(t, st)
			runs := editedRuns(t, c)
			id := extractedAdd(t, c, runs)
			require.NoError(t, testkit.Err(st.Ledger.Retire(context.Background(), id, 1, "ann")))
			var read struct {
				Candidates []idOnly  `json:"candidates"`
				Retired    []retired `json:"retired"`
			}
			require.NoError(t, c.Call(t, "extraction", map[string]any{"from": runs[1]}, &read))
			var got struct {
				Candidate struct {
					Version int `json:"version"`
				} `json:"candidate"`
				Next string `json:"next"`
			}

			err := c.Call(t, "propose_extraction", map[string]any{
				"from": runs[1], "relation": tc.args, "relates_to": id, "kind": "judgment", "content": lesson, "critique": passing,
			}, &got)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, errText(err), tc.want.errText)
			assert.Empty(t, read.Candidates)
			assert.Equal(t, []retired{{ID: id, Approver: "ann", Reason: knowledge.ReasonPerson}}, read.Retired)
			assert.Equal(t, tc.want.version, got.Candidate.Version)
			assert.Contains(t, got.Next, tc.want.next)
		})
	}
}

// An item of an answer read by its id only
type idOnly struct {
	ID string `json:"id"`
}

func idsOf(items []idOnly) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// How an id of an answer relates to the one the steps before the call proposed
const (
	stepsID = "the id of the steps" // the candidate the steps proposed
	newID   = "a new id"            // an id the steps never named
)

// The id as stepsID or newID names it and empty for none
func idOf(got, steps string) string {
	switch got {
	case "":
		return ""
	case steps:
		return stepsID
	}
	return newID
}

// A proposal may name a new tenant only when it says so, and an approved veto answers check_call
func TestServerNewLabelsAndCheckCall(t *testing.T) {
	st := testkit.Open(t)
	c := connect(t, st)
	runID := recordRun(t, c, "nodloop")
	require.NoError(t, c.Run(t, "feedback", map[string]any{"trace_id": runID, "verdict": "reject", "reason_code": "approach", "reason": "never sed -i"}))
	scope := map[string]any{"repo": []any{"newrepo"}}

	refused := c.Run(t, "propose", map[string]any{"kind": "judgment", "content": "x", "producer": "session", "labels": scope, "trace_ids": []any{runID}})
	var proposed struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	require.NoError(t, c.Call(t, "propose", map[string]any{
		"id": "no-sed", "kind": "judgment", "content": "Never edit with sed -i", "from": runID, "new_labels": true,
		"labels": scope,
		"veto":   map[string]any{"tool": "Bash", "when": []any{map[string]any{"field": "commands", "match": `(?m)^sed -i`}}, "example": map[string]any{"command": "sed -i s/a/b/ f"}},
	}, &proposed))
	require.NoError(t, c.Run(t, "approve", map[string]any{"id": proposed.ID, "version": proposed.Version, "approver": "ann"}))
	var blocked, allowed map[string]any
	require.NoError(t, c.Call(t, "check_call", map[string]any{"tool": "Bash", "input": map[string]any{"command": "sed -i s/a/b/ f"}}, &blocked))
	require.NoError(t, c.Call(t, "check_call", map[string]any{"tool": "Bash", "input": map[string]any{"command": "ls"}}, &allowed))

	assert.ErrorContains(t, refused, knowledge.ErrScopeUnobserved.Error())
	assert.Equal(t, map[string]any{"action": "block", "veto": "no-sed", "reason": "Never edit with sed -i"}, blocked)
	assert.Equal(t, map[string]any{"action": "allow"}, allowed)
}

// A server whose approved item git-c reaches the runs of session in repo nodloop
func taught(t *testing.T, policy mcp.Policy) testkit.Client {
	t.Helper()
	c := connectWith(t, testkit.Open(t), policy)
	runID := recordRun(t, c, "nodloop")
	require.NoError(t, c.Run(t, "feedback", map[string]any{"trace_id": runID, "verdict": "reject", "reason": "use git -C"}))
	require.NoError(t, c.Run(t, "propose", map[string]any{"id": "git-c", "kind": "judgment", "content": "use git -C", "from": runID}))
	require.NoError(t, c.Run(t, "approve", map[string]any{"id": "git-c", "version": 1, "approver": "ann"}))
	return c
}

// A drawn holdout key gets no item and the refs of every item the call would have answered
func TestServerKnowledgeForHoldout(t *testing.T) {
	type args struct {
		share knowledge.Holdout
		key   string
	}
	type want struct {
		items    int
		context  bool
		withheld []knowledge.Ref
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a drawn key gets the withheld refs and no item", args{0.999, "window-1"}, want{0, false, []knowledge.Ref{{ID: "git-c", Version: 1}}}},
		{"no key is never drawn", args{0.999, ""}, want{1, true, nil}},
		{"no share withholds nothing", args{0, "window-1"}, want{1, true, nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := taught(t, mcp.Policy{Holdout: tc.args.share})
			var got struct {
				Items    []any           `json:"items"`
				Context  string          `json:"context"`
				Withheld []knowledge.Ref `json:"withheld"`
			}

			args := map[string]any{"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}}, "holdout_key": tc.args.key}
			require.NoError(t, c.Call(t, "knowledge_for", args, &got))

			assert.Len(t, got.Items, tc.want.items)
			assert.Equal(t, tc.want.context, got.Context != "")
			assert.Equal(t, tc.want.withheld, got.Withheld)
		})
	}
}

// A run names what it applied or what the holdout withheld and never both
// Each recorded run runs AfterRun
func TestServerRunWithheld(t *testing.T) {
	gitC := []any{map[string]any{"id": "git-c", "version": 1}}
	holdout := knowledge.Holdout(0.5)
	// The first key of the form w-N whose draw withholds as asked
	key := func(withheld bool) string {
		for i := 0; ; i++ {
			if k := fmt.Sprintf("w-%d", i); holdout.WithholdsRun("session", k) == withheld {
				return k
			}
		}
	}
	type args struct {
		applied, withheld []any
		key               string
	}
	type row struct {
		arm           string
		runs, undrawn int
	}
	type want struct {
		err       string
		effect    []row
		afterRuns int32
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a withheld run of a withheld draw counts in the withheld arm", args{nil, gitC, key(true)}, want{"<nil>", []row{
			{"applied", 0, 0}, {"withheld", 1, 0},
		}, 2}},
		{"an applied run of an applied draw counts in the applied arm", args{gitC, nil, key(false)}, want{"<nil>", []row{
			{"applied", 1, 0}, {"withheld", 0, 0},
		}, 2}},
		{"a run without a holdout key is undrawn", args{gitC, nil, ""}, want{"<nil>", []row{
			{"applied", 0, 1}, {"withheld", 0, 1},
		}, 2}},
		{"applied items on a withheld draw are undrawn", args{gitC, nil, key(true)}, want{"<nil>", []row{
			{"applied", 0, 1}, {"withheld", 0, 1},
		}, 2}},
		{"applied and withheld together are refused", args{gitC, gitC, key(true)}, want{testkit.ErrTool.Error() + ": " + knowledge.ErrWithheldApplied.Error(), []row{
			{"applied", 0, 0}, {"withheld", 0, 0},
		}, 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var afterRuns atomic.Int32
			c := taught(t, mcp.Policy{Holdout: holdout, AfterRun: func(context.Context) { afterRuns.Add(1) }})
			var effect struct {
				Report []loop.EffectRow `json:"report"`
			}

			err := c.Run(t, "run", map[string]any{
				"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}}, "output": "x",
				"applied": tc.args.applied, "withheld": tc.args.withheld, "holdout_key": tc.args.key,
			})
			require.NoError(t, c.Call(t, "report", map[string]any{"name": "effect"}, &effect))

			assert.Equal(t, tc.want.err, fmt.Sprint(err))
			var got []row
			for _, r := range effect.Report {
				got = append(got, row{string(r.Arm), r.Runs, r.Undrawn})
			}
			assert.Equal(t, tc.want.effect, got)
			assert.Equal(t, tc.want.afterRuns, afterRuns.Load())
		})
	}
}

// A propose from a run of one host without labels scopes the lesson to the place so a run of another host receives it once approved
func TestServerProposeFromLeavesTheHostOut(t *testing.T) {
	tcs := []struct {
		name string
		// The labels of the run that asks for knowledge after the approval
		args map[string]any
		// Items that run receives
		want int
	}{
		{"a run of another host in the same place receives the lesson", map[string]any{"repo": []any{"o"}, "host": []any{"codex"}}, 1},
		{"a run of the corrected host receives the lesson", map[string]any{"repo": []any{"o"}, "host": []any{"claude-code"}}, 1},
		{"a run of the place without a host receives the lesson", map[string]any{"repo": []any{"o"}}, 1},
		{"a run of another place receives nothing", map[string]any{"repo": []any{"svc"}, "host": []any{"codex"}}, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := connect(t, testkit.Open(t))
			corrected := hostRun(t, c, "claude-code")
			hostRun(t, c, "codex")
			require.NoError(t, c.Run(t, "feedback", map[string]any{"trace_id": corrected, "verdict": "reject", "reason": "use git -C"}))
			var proposed struct {
				Scope knowledge.RunScope `json:"scope"`
			}
			var items struct {
				Items []any `json:"items"`
			}

			require.NoError(t, c.Call(t, "propose", map[string]any{"id": "git-c", "kind": "judgment", "content": "use git -C", "from": corrected}, &proposed))
			require.NoError(t, c.Run(t, "approve", map[string]any{"id": "git-c", "version": 1, "approver": "ann"}))
			require.NoError(t, c.Call(t, "knowledge_for", map[string]any{"producer": "session", "labels": tc.args}, &items))

			assert.Equal(t, trace.Labels{"repo": {"o"}}, proposed.Scope.Labels)
			assert.Len(t, items.Items, tc.want)
		})
	}
}

// Records one run of producer session in repo o under the host and answers its trace id
func hostRun(t *testing.T, c testkit.Client, host string) string {
	t.Helper()
	var answer struct {
		TraceID string `json:"trace_id"`
	}
	require.NoError(t, c.Call(t, "run", map[string]any{
		"producer": "session", "labels": map[string]any{"repo": []any{"o"}, "host": []any{host}}, "output": "cd repo && git status",
	}, &answer))
	return answer.TraceID
}

// The model a run names lands in its trace and a run without one records none
func TestServerRunModel(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"the model of the run lands in the trace", "gpt-4o-mini", "gpt-4o-mini"},
		{"a run without a model records none", "", ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			c := connect(t, st)
			var run struct {
				TraceID string `json:"trace_id"`
			}

			require.NoError(t, c.Call(t, "run", map[string]any{"producer": "bot", "output": "x", "model": tc.args}, &run))

			got, err := st.Traces.Get(context.Background(), run.TraceID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Model)
		})
	}
}

// The duration a run names lands in its trace and a negative one records nothing
func TestServerRunDuration(t *testing.T) {
	type want struct {
		ms   int64
		fail bool
	}
	tcs := []struct {
		name string
		args int64
		want want
	}{
		{"the duration of the run lands in the trace", 1500, want{1500, false}},
		{"a run without a duration records none", 0, want{0, false}},
		{"a negative duration is refused", -1, want{0, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			c := connect(t, st)
			var run struct {
				TraceID string `json:"trace_id"`
			}

			err := c.Call(t, "run", map[string]any{"producer": "bot", "output": "x", "duration_ms": tc.args}, &run)

			if tc.want.fail {
				assert.ErrorContains(t, err, "negative duration")
				return
			}
			require.NoError(t, err)
			got, err := st.Traces.Get(context.Background(), run.TraceID)
			require.NoError(t, err)
			assert.Equal(t, tc.want.ms, got.DurationMS)
		})
	}
}

// Every recorded verdict and outcome and approval and reaffirm runs AfterVerdict and a refused call runs nothing
func TestServerAfterVerdict(t *testing.T) {
	type args struct {
		tool string
		// The arguments of the call given a run recorded after the setup
		call func(run string) map[string]any
	}
	type want struct {
		err error
		// AfterVerdict calls after the call
		// The verdict and the approval of the setup make two of them
		calls int32
	}
	reaffirm := func(id string) func(string) map[string]any {
		return func(string) map[string]any { return map[string]any{"id": id, "version": 1, "approver": "ann"} }
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a verdict runs it", args{"feedback", func(run string) map[string]any { return map[string]any{"trace_id": run, "verdict": "approve"} }},
			want{nil, 3}},
		{"an outcome runs it", args{"outcome", func(run string) map[string]any { return map[string]any{"trace_id": run, "result": "confirmed"} }},
			want{nil, 3}},
		{"a reaffirm runs it", args{"reaffirm", reaffirm("git-c")}, want{nil, 3}},
		{"a verdict on an unknown run runs nothing", args{"feedback", func(string) map[string]any { return map[string]any{"trace_id": "nope", "verdict": "approve"} }},
			want{testkit.ErrTool, 2}},
		{"an outcome outside the results runs nothing", args{"outcome", func(run string) map[string]any { return map[string]any{"trace_id": run, "result": "maybe"} }},
			want{testkit.ErrTool, 2}},
		{"a reaffirm of an unknown item runs nothing", args{"reaffirm", reaffirm("nope")}, want{testkit.ErrTool, 2}},
		{"a second approval runs nothing", args{"approve", reaffirm("git-c")}, want{testkit.ErrTool, 2}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			c := taught(t, mcp.Policy{AfterVerdict: func(context.Context) { calls.Add(1) }})
			run := recordRun(t, c, "nodloop")

			err := c.Run(t, tc.args.tool, tc.args.call(run))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.calls, calls.Load())
		})
	}
}

// With retention set the reports read the runs since its cutoff only
// A retention a prune would refuse reads every run as no retention does
func TestServerRetentionWindow(t *testing.T) {
	type args struct {
		days retain.Days
		// The days before now the last prune of the records cut at and none when 0
		pruned int
	}
	type want struct {
		runs  int
		since bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a week reads the runs of the week", args{7, 0}, want{1, true}},
		{"no retention reads every run", args{0, 0}, want{2, false}},
		{"a retention under a week reads every run", args{3, 0}, want{2, false}},
		{"a prune another surface ran sets the window without retention", args{0, 7}, want{1, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			marks := retain.NewMarker(st.Dir)
			if tc.args.pruned > 0 {
				cutoff := st.Clock.Now().Add(-time.Duration(tc.args.pruned) * 24 * time.Hour)
				_, err := marks.Claim(context.Background(), st.Clock.Now(), cutoff, retain.Interval)
				require.NoError(t, err)
			}
			c := connectWith(t, st, mcp.Policy{Retention: tc.args.days, Marks: marks})
			old, err := trace.NewRun("session", "", nil, []byte(`{"applied":[]}`), []byte("old"), st.Clock.Now().Add(-8*24*time.Hour))
			require.NoError(t, err)
			require.NoError(t, st.Traces.Append(context.Background(), old.Record()))
			recordRun(t, c, "nodloop")
			var loopReport struct {
				Since  *time.Time `json:"since"`
				Report struct {
					Totals struct {
						Runs int `json:"runs"`
					} `json:"totals"`
				} `json:"report"`
			}

			require.NoError(t, c.Call(t, "report", map[string]any{"name": "loop"}, &loopReport))

			assert.Equal(t, tc.want.runs, loopReport.Report.Totals.Runs)
			assert.Equal(t, tc.want.since, loopReport.Since != nil)
		})
	}
}

// Only the runes of a run are capped so an eleventh item approves and the answers count no items
func TestServerRunChars(t *testing.T) {
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	item := func(id, content string, status knowledge.Status) knowledge.Knowledge {
		k := knowledge.Knowledge{
			ID: id, Version: 1, Kind: knowledge.KindMeaning, Content: content,
			Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}},
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}, Basis: knowledge.BasisStated,
			Status: status, Author: "author", Time: at,
		}
		if status == knowledge.StatusApproved {
			k.Approver, k.ApprovedAt = "ann", at
		}
		return k
	}
	type args struct {
		// Runes of each of the ten approved items
		runes int
	}
	type want struct {
		err     error
		errText string
		// The status of the approval answer and nil when refused
		status     any
		overBudget bool
		// Keys of the folder answer of the approval
		folder []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an eleventh short item approves and the place is under budget", args{10},
			want{status: "approved", folder: []string{"budget", "chars", "full", "items", "producer"}}},
		{"ten long items an import left past RunChars are over budget and refuse one more", args{1000},
			want{err: testkit.ErrTool, errText: knowledge.ErrFolderFull.Error(), overBudget: true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			ctx := context.Background()
			var seeds []knowledge.Knowledge
			for i := range 10 {
				seeds = append(seeds, item(fmt.Sprintf("k%d", i), fmt.Sprintf("%0*d", tc.args.runes, i), knowledge.StatusApproved))
			}
			require.NoError(t, testkit.Err(st.Ledger.Import(ctx, append(seeds, item("k-new", "one more", knowledge.StatusCandidate)))))
			c := connect(t, st)

			var approved map[string]any
			err := c.Call(t, "approve", map[string]any{"id": "k-new", "version": 1, "approver": "ann"}, &approved)
			var place map[string]any
			require.NoError(t, c.Call(t, "knowledge_for", map[string]any{"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}}}, &place))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Contains(t, errText(err), tc.want.errText)
			assert.Equal(t, tc.want.status, approved["status"])
			assert.Equal(t, tc.want.overBudget, place["over_budget"] == true)
			assert.Equal(t, tc.want.folder, keys(approved["folder"]))
		})
	}
}
