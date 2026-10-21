package curate_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// The intervener of the bench with curate.json at hand whose ledger writes and trace appends fail after the allowed ones
// -1 fails none
func (b bench) failingAt(t *testing.T, writes, appends int) *curate.Intervener {
	t.Helper()
	items, err := knowledgefile.New(b.stores.Dir)
	require.NoError(t, err)
	flaky := &testkit.FlakyKnowledge{Store: items, Appends: failing(writes)}
	ledger := knowledge.NewLedger(flaky, vetofile.NewApprovedFile(t.TempDir(), b.stores.Dir), b.stores.Clock.Now, func(prefix string) string { return prefix + "new" })
	traces := &testkit.FlakyTraces{Store: b.stores.Traces, Appends: failing(appends)}
	return curate.NewIntervener(ledger, traces, curate.InterveneSetting{
		Build: "0.10.0", Coverage: b.coverage, Marks: curate.NewMarker(b.stores.Dir), Cursor: func(_ time.Time, id string) string { return id },
	}, b.stores.Clock.Now)
}

// What a write leaves in the records
type writeState struct {
	Received []string
	// The ledger records and the intervention and answer traces
	Records, Events int
	// Ledger records whose decision names no trace
	Orphans int
}

func (b bench) written(t *testing.T) writeState {
	t.Helper()
	ctx := context.Background()
	all, err := b.stores.Ledger.All(ctx)
	require.NoError(t, err)
	out := writeState{Received: b.received(t), Records: len(all)}
	for _, k := range all {
		if _, err := b.stores.Traces.Get(ctx, k.Decision); k.Decision != "" && errors.Is(err, record.ErrTraceNotFound) {
			out.Orphans++
		}
	}
	for _, name := range []trace.Name{trace.NameIntervention, trace.NameAnswer} {
		found, err := b.stores.Traces.List(ctx, record.TraceFilter{Name: string(name)})
		require.NoError(t, err)
		out.Events += len(found)
	}
	return out
}

// A bench whose two lessons the approver approved in repo work have a prepared compaction
func compactionBench(t *testing.T, approver string) bench {
	t.Helper()
	approved := knowledge.StatusApproved
	b := newBench(t, nil, []knowledge.Knowledge{
		lesson("k-a", 1, "rule a", acme("work"), approved, approver), lesson("k-b", 1, "rule b", acme("work"), approved, approver),
	}, nil)
	_, err := b.stores.Ledger.ProposeCompaction(context.Background(), "k-a", []knowledge.Knowledge{{
		Kind: knowledge.KindMeaning, Content: "rule a and rule b", Run: acme("work"), Author: "claude",
		Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-a", Version: 1}, {ID: "k-b", Version: 1}}},
	}})
	require.NoError(t, err)
	return b
}

// A write of several records whose first try fails at any step leaves what its retry completes exactly once
// 1. a reverse, a judgment of a compaction, a merge, an undo of a compaction curate applied and a yes to a proposal
// 2. the first try stops at a failed ledger write or at a failed append of an event and the same write runs again
// The records, what runs receive and the events stand as after one write that never failed and every record names its event
func TestIntervenerRetryWrites(t *testing.T) {
	ctx := context.Background()
	type write func(iv *curate.Intervener) error
	intervene := func(r curate.Request) write {
		return func(iv *curate.Intervener) error { return testkit.Err(iv.Intervene(ctx, r, person)) }
	}
	reverse := func(t *testing.T) (bench, write) {
		d := newDesk(t)
		kept, err := d.iv.Intervene(ctx, curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}, person)
		require.NoError(t, err)
		return d.bench, intervene(curate.Request{Action: trace.ActionReverse, Target: kept.Trace.ID})
	}
	compaction := func(t *testing.T) (bench, write) {
		return compactionBench(t, "ann"), intervene(curate.Request{Action: trace.ActionJudge, Target: "c-generated", Choice: trace.ChoiceKeep})
	}
	merge := func(t *testing.T) (bench, write) {
		big := "a long lesson for the place of the big repo, "
		for len(big) < 4200 {
			big += big
		}
		approved := knowledge.StatusApproved
		b := newBench(t, answer(0.9, 0.02), []knowledge.Knowledge{
			lesson("k-big1", 1, "first "+big, acme("big"), approved, "ann"), lesson("k-big2", 1, "second "+big, acme("big"), approved, "ann"),
			lesson("k-more", 1, "third "+big, acme("big"), knowledge.StatusCandidate, ""),
		}, nil)
		first, err := b.curator().Pass(ctx, b.setting(t))
		require.NoError(t, err)
		_, err = b.stores.Ledger.ProposeCompaction(ctx, "k-big1", []knowledge.Knowledge{{
			Kind: knowledge.KindMeaning, Content: "first and second " + big[:200], Run: acme("big"), Author: "claude",
			Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-big1", Version: 1}, {ID: "k-big2", Version: 1}}},
		}})
		require.NoError(t, err)
		return b, intervene(curate.Request{Action: trace.ActionAnswer, Target: first.Decided[0].ID, Choice: trace.ChoiceMerge})
	}
	undo := func(t *testing.T) (bench, write) {
		b := compactionBench(t, knowledge.ApproverNodloop)
		b.knobs(t, func(*curate.Knobs) {})
		cov, err := b.coverage(ctx, "c-generated")
		require.NoError(t, err)
		id := trace.NewID(written)
		require.NoError(t, testkit.Err(b.stores.Ledger.ApplyCompaction(ctx, "c-generated", id, cov)))
		event := curate.Event{ID: id, Ref: "r-k-a", Source: trace.SourcePush, Draw: trace.NewDraw("2026-09-21", "0.10.0+1a2b3c4d", "5eed", id, "audit", 1)}
		tr, err := event.Decide(curate.Change{Kind: curate.ChangeCompaction, Compaction: "c-generated", Anchor: 1}, curate.Decision{Tier: 1, Action: curate.ActionApply}, nil, written)
		require.NoError(t, err)
		require.NoError(t, b.stores.Traces.Append(ctx, tr.Record()))
		return b, intervene(curate.Request{Action: trace.ActionAnswer, Target: id, Choice: trace.ChoiceUndo})
	}
	proposed := func(r curate.Request) func(t *testing.T) (bench, write) {
		return func(t *testing.T) (bench, write) {
			d := newDesk(t)
			p, err := d.iv.Intervene(ctx, r, agent)
			require.NoError(t, err)
			require.NotEmpty(t, p.Decide)
			return d.bench, intervene(curate.Request{Action: trace.ActionAnswer, Target: p.Decide, Choice: trace.ChoiceYes})
		}
	}
	judged := proposed(curate.Request{Action: trace.ActionJudge, Target: "k-ask:1", Choice: trace.ChoiceKeep})
	paused := proposed(curate.Request{Action: trace.ActionPause, Target: "add"})
	type args struct {
		open func(t *testing.T) (bench, write)
		// The ledger writes and the trace appends of the first try that go through before the one that fails and -1 for none
		writes, appends int
	}
	tcs := []struct {
		name string
		args args
	}{
		{"a reverse whose ledger write fails", args{reverse, 0, -1}},
		{"a reverse whose intervention fails to append", args{reverse, -1, 0}},
		{"a judgment of a compaction whose ledger write fails", args{compaction, 0, -1}},
		{"a judgment of a compaction whose intervention fails to append", args{compaction, -1, 0}},
		{"a merge whose compaction fails", args{merge, 0, -1}},
		{"a merge whose change fails after its compaction", args{merge, 1, -1}},
		{"a merge whose answer fails to append", args{merge, -1, 0}},
		{"an undo of a compaction whose ledger write fails", args{undo, 0, -1}},
		{"an undo of a compaction whose answer fails to append", args{undo, -1, 0}},
		{"a yes whose judgment fails", args{judged, 0, -1}},
		{"a yes whose judgment fails to append", args{judged, -1, 0}},
		{"a yes whose answer fails to append after the judgment", args{judged, -1, 1}},
		{"a yes whose answer fails to append after the pause", args{paused, -1, 1}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			once, write := tc.args.open(t)
			require.NoError(t, write(once.intervener(curate.NewMarker(once.stores.Dir))))
			b, write := tc.args.open(t)
			require.ErrorIs(t, write(b.failingAt(t, tc.args.writes, tc.args.appends)), assert.AnError)

			err := write(b.intervener(curate.NewMarker(b.stores.Dir)))

			require.NoError(t, err)
			assert.Equal(t, once.written(t), b.written(t))
			assert.Zero(t, b.written(t).Orphans)
		})
	}
}
