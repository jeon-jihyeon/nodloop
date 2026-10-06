package mcp_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func newServer(st testkit.Stores, session string) *mcp.Server {
	compactor := compact.New(st.Ledger, st.Traces, st.Feedback, st.Traces, st.Clock.Now)
	return mcp.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, compactor, extract.New(st.Ledger, st.Traces, st.Feedback, st.Clock.Now), st.Clock.Now, session, "nodloop", "--record-dir /records")
}

func connect(t *testing.T, st testkit.Stores) testkit.Client {
	t.Helper()
	srv := newServer(st, mcp.NewSession(st.Clock.Now()))
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
		{"feedback on an unknown trace", "feedback", map[string]any{"trace_id": "nope", "verdict": "approve"}, trace.ErrNotFound.Error()},
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
}

// A compaction of two items of one place: folder, draft, the conversation's coverage check and approval
func TestServerCompaction(t *testing.T) {
	st := testkit.Open(t)
	c := connect(t, st)
	runID := recordRun(t, c, "nodloop")
	for _, id := range []string{"a", "b"} {
		require.NoError(t, c.Run(t, "propose", map[string]any{
			"id": id, "kind": "judgment", "content": "rule " + id, "trace_ids": []any{runID},
			"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}},
		}))
		require.NoError(t, c.Run(t, "approve", map[string]any{"id": id, "version": 1, "approver": "ann"}))
	}
	var proposed struct {
		Compaction   string `json:"compaction"`
		CheckCommand string `json:"check_command"`
	}
	require.NoError(t, c.Call(t, "propose_compaction", map[string]any{"anchor": "a", "items": []any{map[string]any{
		"id": "a", "kind": "judgment", "content": "rule a and rule b", "from": []any{"a", "b"},
		"producer": "session", "labels": map[string]any{"repo": []any{"nodloop"}},
	}}}, &proposed))
	assert.Equal(t, "nodloop knowledge check "+proposed.Compaction+" --record-dir /records", proposed.CheckCommand)
	assert.ErrorContains(t, c.Run(t, "approve_compaction", map[string]any{"compaction": proposed.Compaction, "approver": "ann"}),
		compact.ErrNoCoverage.Error())
	var lost struct {
		Passed bool `json:"passed"`
	}
	require.NoError(t, c.Call(t, "check_compaction", map[string]any{"compaction": proposed.Compaction, "items": []any{
		map[string]any{"old": "a", "covered_by": []any{"a"}}, map[string]any{"old": "b", "covered_by": []any{"a"}, "lost": []any{"rule b"}},
	}}, &lost))
	assert.False(t, lost.Passed)
	require.NoError(t, c.Run(t, "check_compaction", map[string]any{"compaction": proposed.Compaction, "items": []any{
		map[string]any{"old": "a", "covered_by": []any{"a"}}, map[string]any{"old": "b", "covered_by": []any{"a"}},
	}}))

	assert.NoError(t, c.Run(t, "approve_compaction", map[string]any{"compaction": proposed.Compaction, "approver": "ann"}))
}

// An edit read through extraction becomes an add, and the same lesson from a second edit is a duplicate of the approved item
func TestServerExtraction(t *testing.T) {
	st := testkit.Open(t)
	c := connect(t, st)
	pass := map[string]any{"states": true, "holds": true, "fits": true, "why": "ok"}
	lesson := "Run git with -C <dir> instead of changing into the directory"
	first, second := recordRun(t, c, "nodloop"), recordRun(t, c, "nodloop")
	for _, id := range []string{first, second} {
		require.NoError(t, c.Run(t, "feedback", map[string]any{
			"trace_id": id, "verdict": "edit", "reason_code": "approach", "edited_output": "git -C repo status",
		}))
	}
	var read struct {
		Edited string `json:"edited"`
		Rules  string `json:"rules"`
		Items  []any  `json:"items"`
	}
	require.NoError(t, c.Call(t, "extraction", map[string]any{"from": first}, &read))
	assert.Equal(t, "git -C repo status", read.Edited)
	assert.Equal(t, extract.Rules, read.Rules)
	assert.Empty(t, read.Items)
	var added struct {
		Relation  extract.Relation `json:"relation"`
		Candidate struct {
			ID    string             `json:"id"`
			Scope knowledge.RunScope `json:"scope"`
		} `json:"candidate"`
	}
	require.NoError(t, c.Call(t, "propose_extraction", map[string]any{
		"from": first, "relation": "add", "kind": "judgment", "content": lesson, "critique": pass,
	}, &added))
	assert.Equal(t, extract.RelationAdd, added.Relation)
	assert.Equal(t, knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}, added.Candidate.Scope)
	require.NoError(t, c.Run(t, "approve", map[string]any{"id": added.Candidate.ID, "version": 1, "approver": "ann"}))

	var dup struct {
		Related struct {
			ID string `json:"id"`
		} `json:"related"`
		Candidate any `json:"candidate"`
	}
	require.NoError(t, c.Call(t, "propose_extraction", map[string]any{
		"from": second, "relation": "duplicate", "relates_to": added.Candidate.ID, "kind": "judgment", "content": lesson, "critique": pass,
	}, &dup))

	assert.Equal(t, added.Candidate.ID, dup.Related.ID)
	assert.Nil(t, dup.Candidate)
	err := c.Run(t, "propose_extraction", map[string]any{
		"from": second, "relation": "add", "kind": "judgment", "content": lesson, "critique": map[string]any{"states": true, "holds": false, "fits": true, "why": "x"},
	})
	assert.ErrorContains(t, err, extract.ErrCriticRefused.Error())
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
