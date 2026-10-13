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
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func newServer(st testkit.Stores, session string, policy mcp.Policy) *mcp.Server {
	compactor := compact.New(st.Ledger, st.Traces, st.Feedback, st.Traces, st.Clock.Now)
	return mcp.New(st.Traces, st.Feedback, st.Outcomes, st.Ledger, compactor, extract.New(st.Ledger, st.Traces, st.Feedback, st.Clock.Now), st.Clock.Now, session, "nodloop", "--record-dir /records", "", policy)
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
		"from": first, "relation": "add", "kind": "judgment", "content": lesson, "keys": []string{"repo"}, "critique": pass,
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
			require.NoError(t, st.Traces.Append(context.Background(), old))
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
