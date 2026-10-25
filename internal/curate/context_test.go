package curate_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestCuratorHoldsChangeHypotheses(t *testing.T) {
	type args struct {
		timed      bool
		conclusion string
	}
	tcs := []struct {
		name string
		args args
	}{
		{"temporary applicability", args{true, "proposed"}},
		{"context or time conflict", args{false, "conflict"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := lesson("acme", 1, "Use the presentation for this context", acme("acme"), knowledge.StatusCandidate, "")
			k.Extract = "acme-extract"
			if tc.args.timed {
				k.Run.ValidUntil = written.Add(24 * time.Hour)
			}
			b := newBench(t, answer(0.999, 0.001), []knowledge.Knowledge{k}, nil)
			output, err := json.Marshal(map[string]any{"conclusion": tc.args.conclusion,
				"related": knowledge.Ref{ID: "k-person", Version: 1}})
			require.NoError(t, err)
			ctx := context.Background()
			require.NoError(t, b.stores.Traces.Append(ctx, (trace.Trace{ID: k.Extract, Name: trace.NameExtract,
				Ref: "r-acme", Time: written, Output: output}).Record()))
			b.knobs(t, func(k *curate.Knobs) { k.Activated = true })
			got, err := b.curator().Pass(ctx, b.setting(t))
			require.NoError(t, err)
			require.Len(t, got.Decided, 1)
			assert.Equal(t, curate.ActionHold, got.Decided[0].Decision.Action)
			assert.Equal(t, curate.RuleTriageProtected, got.Decided[0].Decision.RuleCode)
			assert.Empty(t, got.Due)
			calls, err := b.stores.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameClassify)})
			require.NoError(t, err)
			assert.Empty(t, calls)
			all, err := b.stores.Ledger.All(ctx)
			require.NoError(t, err)
			current, err := all.Version("acme", 1)
			require.NoError(t, err)
			assert.Equal(t, knowledge.StatusCandidate, current.Status)
		})
	}
}

func TestCuratorRecoveryPreservesContext(t *testing.T) {
	timed := lesson("acme", 1, "Use the temporary presentation", acme("acme"), knowledge.StatusCandidate, "")
	timed.Run.ValidUntil = written.Add(24 * time.Hour)
	old := lesson("acme", 1, "Follow the ordinary convention", acme("acme"),
		knowledge.StatusApproved, knowledge.ApproverNodloop)
	next := lesson("acme", 2, "Use the incident exception", acme("acme"), knowledge.StatusCandidate, "")
	next.Base, next.Extract = 1, "acme-extract"
	type args struct {
		items   []knowledge.Knowledge
		kind    curate.ChangeKind
		version int
		action  curate.Action
		reason  knowledge.Reason
	}
	type want struct {
		statuses, decisions []string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"old apply of a timed item", args{[]knowledge.Knowledge{timed}, curate.ChangeAdd, 1, curate.ActionApply, ""},
			want{[]string{"acme v1 candidate"}, []string{"add acme v1 hold triage_protected 2"}}},
		{"old apply of a conflict", args{[]knowledge.Knowledge{old, next}, curate.ChangeUpdate, 2, curate.ActionApply, ""},
			want{[]string{"acme v1 approved nodloop", "acme v2 candidate"}, []string{"update acme v2 hold triage_protected 2"}}},
		{"old withdrawal from a conflict", args{[]knowledge.Knowledge{old}, curate.ChangeWithdraw, 1,
			curate.ActionWithdraw, knowledge.ReasonConflict}, want{[]string{"acme v1 approved nodloop"}, []string{}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			b := newBench(t, answer(0.999, 0.001), tc.args.items, nil)
			require.NoError(t, b.stores.Traces.Append(ctx, (trace.Trace{
				ID: "acme-extract", Name: trace.NameExtract, Ref: "r-acme", Time: written,
				Output: json.RawMessage(`{"conclusion":"conflict","related":{"id":"acme","version":1}}`),
			}).Record()))
			b.knobs(t, func(k *curate.Knobs) { k.Activated = true })
			event := curate.Event{ID: "acme-decision", Ref: "r-acme", Source: trace.SourcePush,
				Draw: trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "5eed", "acme-decision", "audit", 0.05)}
			decision := curate.Decision{Tier: 1, Action: tc.args.action, Reason: tc.args.reason}
			if tc.args.action == curate.ActionWithdraw {
				decision.Failure, decision.Reopen = curate.FailureUnderexplored, curate.ReopenOtherSession
			}
			tr, err := event.Decide(curate.Change{Kind: tc.args.kind, ID: "acme", Version: tc.args.version, Anchor: 1},
				decision, nil, written.Add(2*time.Hour))
			require.NoError(t, err)
			require.NoError(t, b.stores.Traces.Append(ctx, tr.Record()))
			got, err := b.curator().Pass(ctx, b.setting(t))
			require.NoError(t, err)
			assert.Equal(t, tc.want.statuses, statuses(t, b))
			assert.Equal(t, tc.want.decisions, decisionsOf(got.Decided))
			assert.Empty(t, got.Due)
		})
	}
}
