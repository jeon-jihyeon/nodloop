package consolidate_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/consolidate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func place(producer, repo string) *knowledge.RunScope {
	if repo == "" {
		return &knowledge.RunScope{Producer: producer}
	}
	return &knowledge.RunScope{Producer: producer, Labels: trace.Labels{"repo": {repo}}}
}

// Proposes a candidate taught by one run of its own
func propose(t *testing.T, st testkit.Stores, id string, kind knowledge.Kind, content string, run *knowledge.RunScope) {
	t.Helper()
	_, _, err := st.Ledger.Propose(context.Background(), knowledge.Knowledge{
		ID: id, Kind: kind, Content: content, Author: "author", Run: run, Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"run-" + id}},
	})
	require.NoError(t, err)
}

func consolidateTraces(t *testing.T, st testkit.Stores) trace.Traces {
	t.Helper()
	found, err := st.Traces.List(context.Background(), record.TraceFilter{Name: string(trace.NameConsolidate)})
	require.NoError(t, err)
	for _, tr := range found {
		require.NoError(t, schema.Validate(string(trace.NameConsolidate), tr))
	}
	return trace.RestoreAll(found)
}

func ref(id string) knowledge.Ref {
	return knowledge.Ref{ID: id, Version: 1}
}

// Ten waiting candidates of one place where two say the same record one trace naming that pair and leave the ledger as it was
func TestConsolidatorRunRecordsThePair(t *testing.T) {
	ctx := context.Background()
	pair := []consolidate.Group{{Members: []knowledge.Ref{ref("port-a"), ref("port-b")}, Why: "both name port 8080"}}
	tcs := []struct {
		name string
		// The model the consolidator asks for
		args string
		// The model of the request and of the trace
		want string
	}{
		{"a named model is asked and recorded", "haiku", "haiku"},
		{"no model leaves the choice to the client", "", ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			propose(t, st, "port-a", knowledge.KindMeaning, "The api of o/svc listens on port 8080", place("session", "acme"))
			propose(t, st, "port-b", knowledge.KindMeaning, "o/svc serves its api on 8080", place("session", "acme"))
			for i := range 8 {
				propose(t, st, fmt.Sprintf("other-%d", i), knowledge.KindMeaning, fmt.Sprintf("Fact %d of acme", i), place("session", "acme"))
			}
			before, err := st.Ledger.All(ctx)
			require.NoError(t, err)
			client := llmmock.NewMockClient(gomock.NewController(t))
			var req llm.Request
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, r llm.Request) (llm.Response, error) {
				req = r
				return llm.Response{
					Output:      json.RawMessage(`{"groups":[{"members":[{"id":"port-a","version":1},{"id":"port-b","version":1}],"why":"both name port 8080"}]}`),
					InputTokens: 900, OutputTokens: 40, CostUSD: 0.02,
				}, nil
			})

			got, err := consolidate.New(st.Ledger, st.Traces, client, tc.args, st.Clock.Now).Run(ctx)

			require.NoError(t, err)
			traces := consolidateTraces(t, st)
			require.Len(t, traces, 1)
			tr := traces[0]
			var in struct {
				Clusters [][]knowledge.Ref `json:"clusters"`
			}
			require.NoError(t, json.Unmarshal(tr.Input, &in))
			require.Len(t, in.Clusters, 1)
			after, err := st.Ledger.All(ctx)
			require.NoError(t, err)

			assert.Equal(t, consolidate.Rules, req.System)
			assert.JSONEq(t, consolidate.Schema, string(req.Schema))
			assert.Equal(t, tc.want, req.Model)
			assert.Contains(t, req.Prompt, "# Waiting candidates of session")
			assert.Contains(t, req.Prompt, "[port-a v1 meaning] The api of o/svc listens on port 8080\nScope: runs of session. repo=acme")
			assert.Equal(t, []consolidate.Result{{Producer: "session", Groups: pair, Candidates: 10, Clustered: 10}}, got)
			assert.Equal(t, "session", tr.Subject)
			assert.Equal(t, tc.want, tr.Model)
			assert.Empty(t, tr.Error)
			assert.JSONEq(t, `{"groups":[{"members":[{"id":"port-a","version":1},{"id":"port-b","version":1}],"why":"both name port 8080"}],"candidates":10,"clustered":10}`,
				string(tr.Output))
			assert.Len(t, in.Clusters[0], 10)
			assert.Equal(t, trace.Usage{InputTokens: 900, OutputTokens: 40, CostUSD: 0.02}, tr.Usage)
			assert.Equal(t, testkit.ClockStep.Milliseconds(), tr.DurationMS, "the run takes one step of the clock from its start to its trace")
			assert.Equal(t, before, after, "a run writes no knowledge record")
		})
	}
}

// The code checks keep the groups that pass and name each dropped group in the trace error
func TestConsolidatorRunChecksTheGroups(t *testing.T) {
	member := func(id string, version int) string { return fmt.Sprintf(`{"id":%q,"version":%d}`, id, version) }
	group := func(members ...string) string {
		return `{"members":[` + strings.Join(members, ",") + `],"why":"same"}`
	}
	answer := func(groups ...string) string { return `{"groups":[` + strings.Join(groups, ",") + `]}` }
	ab := consolidate.Group{Members: []knowledge.Ref{ref("a"), ref("b")}, Why: "same"}
	type args struct {
		output string
		// The call fails with it
		err error
	}
	type want struct {
		groups []consolidate.Group
		// In the trace error and empty for none
		traceErr string
		runErr   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a group that passes is kept", args{answer(group(member("a", 1), member("b", 1))), nil}, want{[]consolidate.Group{ab}, "", nil}},
		{"no group keeps none", args{answer(), nil}, want{[]consolidate.Group{}, "", nil}},
		{
			"a member outside the cluster drops its group",
			args{answer(group(member("a", 1), member("x", 1))), nil},
			want{[]consolidate.Group{}, "group a v1, x v1: " + consolidate.ErrMemberUnknown.Error() + ": x v1", nil},
		},
		{
			"a version the cluster did not send drops its group",
			args{answer(group(member("a", 2), member("b", 1))), nil},
			want{[]consolidate.Group{}, consolidate.ErrMemberUnknown.Error() + ": a v2", nil},
		},
		{
			"a candidate in two groups drops the later group and keeps the first",
			args{answer(group(member("a", 1), member("b", 1)), group(member("b", 1), member("d", 1))), nil},
			want{[]consolidate.Group{ab}, consolidate.ErrMemberRepeated.Error() + ": b v1", nil},
		},
		{
			"a member named twice in one group drops it",
			args{answer(group(member("a", 1), member("a", 1))), nil},
			want{[]consolidate.Group{}, consolidate.ErrMemberRepeated.Error() + ": a v1", nil},
		},
		{"a group of one is dropped", args{answer(group(member("a", 1))), nil}, want{[]consolidate.Group{}, consolidate.ErrGroupSmall.Error(), nil}},
		{
			"a meaning and a judgment in one group are dropped",
			args{answer(group(member("a", 1), member("c", 1))), nil},
			want{[]consolidate.Group{}, consolidate.ErrKindsDiffer.Error(), nil},
		},
		{
			"a dropped group leaves the others",
			args{answer(group(member("a", 1), member("c", 1)), group(member("a", 1), member("b", 1))), nil},
			want{[]consolidate.Group{ab}, consolidate.ErrKindsDiffer.Error(), nil},
		},
		{
			"an answer outside the schema fails the cluster",
			args{`{"groups":"none"}`, nil},
			want{[]consolidate.Group{}, consolidate.ErrAnswerInvalid.Error(), consolidate.ErrAnswerInvalid},
		},
		{"a failed call is recorded with its error", args{"", assert.AnError}, want{[]consolidate.Group{}, "cluster of ", assert.AnError}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			propose(t, st, "a", knowledge.KindMeaning, "acme bills in UTC", place("session", "acme"))
			propose(t, st, "b", knowledge.KindMeaning, "acme invoices use UTC", place("session", "acme"))
			propose(t, st, "c", knowledge.KindJudgment, "check UTC before a billing fix", place("session", "acme"))
			propose(t, st, "d", knowledge.KindMeaning, "acme rounds cents down", place("session", "acme"))
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: json.RawMessage(tc.args.output), CostUSD: 0.01}, tc.args.err)

			got, err := consolidate.New(st.Ledger, st.Traces, client, "", st.Clock.Now).Run(context.Background())

			assert.ErrorIs(t, err, tc.want.runErr)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want.groups, got[0].Groups)
			traces := consolidateTraces(t, st)
			require.Len(t, traces, 1)
			assert.Equal(t, tc.want.traceErr == "", traces[0].Error == "", traces[0].Error)
			assert.Contains(t, traces[0].Error, tc.want.traceErr)
			assert.InDelta(t, 0.01, traces[0].Usage.CostUSD, 1e-9, "the call is charged whatever the checks drop")
		})
	}
}

// One call per cluster of two or more and one trace per producer that sent one
func TestConsolidatorRunClusters(t *testing.T) {
	type candidate struct {
		id       string
		producer string
		repo     string
	}
	type want struct {
		calls int
		// The Candidates and Clustered counts of each producer with a trace
		results []consolidate.Result
		// The producer of each consolidate trace
		traced []string
	}
	tcs := []struct {
		name string
		args []candidate
		want want
	}{
		{
			"overlapping scopes join and disjoint ones stay apart per producer",
			[]candidate{{"s1", "session", "acme"}, {"s2", "session", "acme"}, {"s3", "session", "other"}, {"b1", "bot", ""}, {"b2", "bot", ""}},
			want{2, []consolidate.Result{
				{Producer: "bot", Groups: []consolidate.Group{}, Candidates: 2, Clustered: 2},
				{Producer: "session", Groups: []consolidate.Group{}, Candidates: 3, Clustered: 2},
			}, []string{"bot", "session"}},
		},
		{"a cluster of one is never sent", []candidate{{"s1", "session", "acme"}, {"s2", "session", "other"}}, want{0, []consolidate.Result{}, []string{}}},
		{"no candidate sends nothing", nil, want{0, []consolidate.Result{}, []string{}}},
		{
			"a producer whose clusters are all of one gets no trace",
			[]candidate{{"s1", "session", "acme"}, {"s2", "session", "acme"}, {"b1", "bot", "acme"}},
			want{1, []consolidate.Result{{Producer: "session", Groups: []consolidate.Group{}, Candidates: 2, Clustered: 2}}, []string{"session"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			for _, c := range tc.args {
				propose(t, st, c.id, knowledge.KindMeaning, "lesson "+c.id, place(c.producer, c.repo))
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: json.RawMessage(`{"groups":[]}`)}, nil).Times(tc.want.calls)

			got, err := consolidate.New(st.Ledger, st.Traces, client, "", st.Clock.Now).Run(context.Background())

			traced := []string{}
			for _, tr := range consolidateTraces(t, st) {
				traced = append(traced, tr.Subject)
			}

			assert.NoError(t, err)
			assert.Equal(t, tc.want.results, got)
			assert.ElementsMatch(t, tc.want.traced, traced)
		})
	}
}

// A waiting candidate is sent and an approved item never is
func TestConsolidatorRunReadsWaitingCandidatesOnly(t *testing.T) {
	ctx := context.Background()
	tcs := []struct {
		name string
		// Approvers of candidate a
		args []string
		// Calls and consolidate traces
		want int
	}{
		{"a waiting candidate is sent", nil, 1},
		{"an approved item is no candidate", []string{"ann"}, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			propose(t, st, "a", knowledge.KindMeaning, "acme bills in UTC", place("session", "acme"))
			propose(t, st, "b", knowledge.KindMeaning, "acme invoices use UTC", place("session", "acme"))
			for _, approver := range tc.args {
				require.NoError(t, testkit.Err(st.Ledger.Approve(ctx, "a", 1, approver)))
			}
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: json.RawMessage(`{"groups":[]}`)}, nil).Times(tc.want)

			_, err := consolidate.New(st.Ledger, st.Traces, client, "", st.Clock.Now).Run(ctx)

			assert.NoError(t, err)
			assert.Len(t, consolidateTraces(t, st), tc.want)
		})
	}
}

// A failed read of the ledger asks nothing and a failed append is returned
func TestConsolidatorRunStoreFailures(t *testing.T) {
	tcs := []struct {
		name string
		args int
		want int
	}{
		{"a failed read of the ledger asks nothing", 0, 0},
		{"a failed append of the trace is returned", 1, 1},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			propose(t, st, "a", knowledge.KindMeaning, "acme bills in UTC", place("session", "acme"))
			propose(t, st, "b", knowledge.KindMeaning, "acme invoices use UTC", place("session", "acme"))
			items, err := knowledgefile.New(st.Dir)
			require.NoError(t, err)
			ledger := knowledge.NewLedger(
				&testkit.FlakyKnowledge{Store: items, Reads: testkit.Reads{Allowed: tc.args, Err: assert.AnError}},
				vetofile.NewApprovedFile(t.TempDir(), st.Dir), st.Clock.Now, func(prefix string) string { return prefix + "generated" },
			)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: json.RawMessage(`{"groups":[]}`)}, nil).Times(tc.want)
			traces := &testkit.FlakyTraces{Store: st.Traces, Appends: testkit.Reads{Err: assert.AnError}}

			_, err = consolidate.New(ledger, traces, client, "", st.Clock.Now).Run(context.Background())

			assert.ErrorIs(t, err, assert.AnError)
		})
	}
}
