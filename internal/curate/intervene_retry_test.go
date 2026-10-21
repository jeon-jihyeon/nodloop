package curate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
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
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
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
	out := writeState{Received: b.received(t), Records: len(all), Orphans: len(b.orphans(t))}
	for _, name := range []trace.Name{trace.NameIntervention, trace.NameAnswer} {
		found, err := b.stores.Traces.List(ctx, record.TraceFilter{Name: string(name)})
		require.NoError(t, err)
		out.Events += len(found)
	}
	return out
}

// The decision of every ledger record that names one no trace holds
func (b bench) orphans(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	all, err := b.stores.Ledger.All(ctx)
	require.NoError(t, err)
	out := []string{}
	for _, k := range all {
		if _, err := b.stores.Traces.Get(ctx, k.Decision); k.Decision != "" && errors.Is(err, record.ErrTraceNotFound) {
			out = append(out, k.Decision)
		}
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

// A write of the person through an intervener
type write func(iv *curate.Intervener) error

// The request intervened under the person
func intervening(r curate.Request) write {
	return func(iv *curate.Intervener) error { return testkit.Err(iv.Intervene(context.Background(), r, person)) }
}

// A desk whose judgment of k-add with the choice the write reverses
func reversing(choice trace.Choice) func(t *testing.T) (bench, write) {
	return func(t *testing.T) (bench, write) {
		t.Helper()
		d := newDesk(t)
		judged, err := d.iv.Intervene(context.Background(), curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: choice}, person)
		require.NoError(t, err)
		return d.bench, intervening(curate.Request{Action: trace.ActionReverse, Target: judged.Trace.ID})
	}
}

// A compaction of two lessons of the person that the write judges keep
func judgingCompaction(t *testing.T) (bench, write) {
	t.Helper()
	return compactionBench(t, "ann"), intervening(curate.Request{Action: trace.ActionJudge, Target: "c-generated", Choice: trace.ChoiceKeep})
}

// A compaction the person approved already that the write judges keep again
func judgingAgain(t *testing.T) (bench, write) {
	t.Helper()
	b, w := judgingCompaction(t)
	require.NoError(t, w(b.intervener(nil)))
	return b, w
}

// A question about k-more that needs room in the folder of the big repo and the compaction prepared for it that the write merges
func merging(t *testing.T) (bench, write) {
	t.Helper()
	ctx := context.Background()
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
	return b, intervening(curate.Request{Action: trace.ActionAnswer, Target: first.Decided[0].ID, Choice: trace.ChoiceMerge})
}

// A compaction curate applied in tier 1 into k-generated that the write undoes through the answer of its decide
func undoing(t *testing.T) (bench, write) {
	t.Helper()
	ctx := context.Background()
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
	return b, intervening(curate.Request{Action: trace.ActionAnswer, Target: id, Choice: trace.ChoiceUndo})
}

// A write of several records whose first try fails at any step leaves what its retry completes exactly once
// 1. a reverse, a judgment of a compaction, a merge, an undo of a compaction curate applied and a yes to a proposal
// 2. the first try stops at a failed ledger write or at a failed append of an event and the same write runs again
// An answer whose intervention fails to append after the answer is taken by the retry and never refused as closed
// The records, what runs receive and the events stand as after one write that never failed and every record names its event
func TestIntervenerRetryWrites(t *testing.T) {
	ctx := context.Background()
	// An empty target of the proposal names the question about k-ask
	proposed := func(r curate.Request) func(t *testing.T) (bench, write) {
		return func(t *testing.T) (bench, write) {
			d := newDesk(t)
			asked := r
			if asked.Target == "" {
				asked.Target = d.decideOf(t, "k-ask")
			}
			p, err := d.iv.Intervene(ctx, asked, agent)
			require.NoError(t, err)
			require.NotEmpty(t, p.Decide)
			return d.bench, intervening(curate.Request{Action: trace.ActionAnswer, Target: p.Decide, Choice: trace.ChoiceYes})
		}
	}
	judged := proposed(curate.Request{Action: trace.ActionJudge, Target: "k-ask:1", Choice: trace.ChoiceKeep})
	paused := proposed(curate.Request{Action: trace.ActionPause, Target: "add"})
	answered := proposed(curate.Request{Action: trace.ActionAnswer, Choice: trace.ChoiceKeep})
	dismissed := proposed(curate.Request{Action: trace.ActionDismiss})
	type args struct {
		open func(t *testing.T) (bench, write)
		// The ledger writes and the trace appends of the first try that go through before the one that fails and -1 for none
		writes, appends int
	}
	tcs := []struct {
		name string
		args args
	}{
		{"a reverse whose ledger write fails", args{reversing(trace.ChoiceKeep), 0, -1}},
		{"a reverse whose intervention fails to append", args{reversing(trace.ChoiceKeep), -1, 0}},
		{"a reverse of a drop whose intervention fails to append", args{reversing(trace.ChoiceUndo), -1, 0}},
		{"a judgment of a compaction whose ledger write fails", args{judgingCompaction, 0, -1}},
		{"a judgment of a compaction whose intervention fails to append", args{judgingCompaction, -1, 0}},
		{"a merge whose compaction fails", args{merging, 0, -1}},
		{"a merge whose change fails after its compaction", args{merging, 1, -1}},
		{"a merge whose answer fails to append", args{merging, -1, 0}},
		{"a merge whose intervention fails to append after its answer", args{merging, -1, 1}},
		{"an undo of a compaction whose ledger write fails", args{undoing, 0, -1}},
		{"an undo of a compaction whose answer fails to append", args{undoing, -1, 0}},
		{"an undo of a compaction whose intervention fails to append after its answer", args{undoing, -1, 1}},
		{"a yes whose judgment fails", args{judged, 0, -1}},
		{"a yes whose judgment fails to append", args{judged, -1, 0}},
		{"a yes whose answer fails to append after the judgment", args{judged, -1, 1}},
		{"a yes whose answer fails to append after the pause", args{paused, -1, 1}},
		{"a yes whose intervention fails to append after its answer", args{judged, -1, 2}},
		{"a yes whose answer fails to append after the answer it carried out", args{answered, -1, 2}},
		{"a yes whose answer fails to append after the dismiss it carried out", args{dismissed, -1, 1}},
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

// The traces of one of two writers at once
// 1. the first holds its first append until the other read the traces or the wait ran out
// So the other reads a ledger write of the first whose intervention is not on record yet unless a turn keeps it out
// 2. the other lets the first go on once a read of the traces returned
type racingTraces struct {
	*tracefile.Store
	first bool
	// Closed once the first reached its append
	appending chan struct{}
	// Closed once the other read the traces
	read     chan struct{}
	appended *sync.Once
	listed   *sync.Once
}

func (r racingTraces) Append(ctx context.Context, tr record.Trace) error {
	if r.first {
		r.appended.Do(func() {
			close(r.appending)
			select {
			case <-r.read:
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
	return r.Store.Append(ctx, tr)
}

func (r racingTraces) List(ctx context.Context, f record.TraceFilter) ([]record.Trace, error) {
	out, err := r.Store.List(ctx, f)
	if !r.first {
		r.listed.Do(func() { close(r.read) })
	}
	return out, err
}

// Two writes of the person at once over the same records each stand under an id of their own
// 1. the second starts once the first wrote the ledger and before it recorded its intervention
// 2. the turn of the record directory keeps the second out until the first ended so no retry takes a write still in progress
func TestIntervenerTurns(t *testing.T) {
	keeping := func(t *testing.T) (bench, write) {
		t.Helper()
		return newDesk(t).bench, intervening(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep})
	}
	type want struct {
		// The intervention traces and the distinct ids among them
		events, ids int
	}
	tcs := []struct {
		name string
		open func(t *testing.T) (bench, write)
		want want
	}{
		{"a keep of a candidate", keeping, want{2, 2}},
		{"a keep of a compaction", judgingCompaction, want{2, 2}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, write := tc.open(t)
			appending, read := make(chan struct{}), make(chan struct{})
			racing := func(first bool) *curate.Intervener {
				traces := racingTraces{
					Store: b.stores.Traces, first: first, appending: appending, read: read, appended: &sync.Once{}, listed: &sync.Once{},
				}
				return curate.NewIntervener(b.stores.Ledger, traces, curate.InterveneSetting{
					Build: "0.10.0", Coverage: b.coverage, Marks: curate.NewMarker(b.stores.Dir), Turn: curate.FileTurn(b.stores.Dir),
					Cursor: func(_ time.Time, id string) string { return id },
				}, b.stores.Clock.Now)
			}
			errs := make(chan error, 1)
			go func() { errs <- write(racing(true)) }()
			<-appending

			err := write(racing(false))

			require.NoError(t, err)
			require.NoError(t, <-errs)
			found, err := b.stores.Traces.List(ctx, record.TraceFilter{Name: string(trace.NameIntervention)})
			require.NoError(t, err)
			ids := map[string]bool{}
			for _, tr := range found {
				ids[tr.ID] = true
			}
			assert.Equal(t, tc.want, want{len(found), len(ids)})
			assert.Empty(t, b.orphans(t))
		})
	}
}

// A write whose turn cannot be taken fails before it reads or writes anything
func TestIntervenerTurnFails(t *testing.T) {
	d := newDesk(t)
	require.NoError(t, os.Mkdir(filepath.Join(d.stores.Dir, curate.TurnFile), 0o700))
	before := d.written(t)
	iv := curate.NewIntervener(d.stores.Ledger, d.stores.Traces, curate.InterveneSetting{
		Build: "0.10.0", Marks: curate.NewMarker(d.stores.Dir), Turn: curate.FileTurn(d.stores.Dir), Cursor: func(_ time.Time, id string) string { return id },
	}, d.stores.Clock.Now)

	err := intervening(curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep})(iv)

	assert.Error(t, err)
	assert.Equal(t, before, d.written(t))
}

// A write of several records takes no other write whose event failed to append on the ids it writes
// 1. the other write of the person or of another person still names a decision no trace holds
// 2. the write is refused with nothing written where the other write changed what it writes
// and stands under an id of its own otherwise
func TestIntervenerRetryWritesTakeNoOtherWrite(t *testing.T) {
	bob := curate.Submitter{Proof: trace.ProofKey, Person: "bob", Source: trace.SourceManual}
	judge := func(target string, choice trace.Choice) curate.Request {
		return curate.Request{Action: trace.ActionJudge, Target: target, Choice: choice}
	}
	type args struct {
		open func(t *testing.T) (bench, write)
		// The judgment whose intervention fails to append before the write and who made it
		other curate.Request
		by    curate.Submitter
	}
	type want struct {
		err error
		// Whether the write changed what runs receive or what waits
		changed bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a reverse of a keep a later undo changed", args{reversing(trace.ChoiceKeep), judge("k-add:1", trace.ChoiceUndo), person},
			want{knowledge.ErrReverseStale, false},
		},
		{
			"an undo of a compaction whose item a later undo changed", args{undoing, judge("k-generated:1", trace.ChoiceUndo), person},
			want{knowledge.ErrReverseStale, false},
		},
		{"a merge of a change the person dropped", args{merging, judge("k-more:1", trace.ChoiceUndo), person}, want{curate.ErrTargetInvalid, false}},
		{"a merge after a reaffirm of an item of its folder", args{merging, judge("k-big1:1", trace.ChoiceKeep), person}, want{nil, true}},
		{
			"a judgment of an approved compaction after a restore of an item it retired", args{judgingAgain, judge("k-a:1", trace.ChoiceKeep), person},
			want{nil, false},
		},
		{"a judgment of a compaction another person approved", args{judgingCompaction, judge("c-generated", trace.ChoiceKeep), bob}, want{nil, false}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, write := tc.args.open(t)
			require.ErrorIs(t, testkit.Err(b.failingAt(t, -1, 0).Intervene(ctx, tc.args.other, tc.args.by)), assert.AnError)
			other, before := b.orphans(t), b.received(t)
			require.NotEmpty(t, other)

			err := write(b.intervener(curate.NewMarker(b.stores.Dir)))

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.changed, !slices.Equal(before, b.received(t)))
			assert.Subset(t, b.orphans(t), other)
		})
	}
}

func TestIntervenerRetryKeepsVersionsApart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDesk(t)
	first := curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}
	_, err := d.failingAt(t, -1, 0).Intervene(ctx, first, person)
	require.ErrorIs(t, err, assert.AnError)
	next, _, err := d.stores.Ledger.Propose(ctx, knowledge.Knowledge{
		ID: "k-add", Kind: knowledge.KindJudgment, Content: "Include the failing assertion", Run: acme("acme"),
		Author: "author", Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "k-add", Version: 1}}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, next.Version)

	got, err := d.iv.Intervene(ctx, curate.Request{
		Action: trace.ActionJudge, Target: "k-add:2", Choice: trace.ChoiceKeep,
	}, person)

	require.NoError(t, err)
	all, err := d.stores.Ledger.All(ctx)
	require.NoError(t, err)
	current, ok := all.Approved().Find("k-add")
	require.True(t, ok)
	assert.Equal(t, 2, current.Version)
	require.NotEmpty(t, got.Wrote)
	assert.Equal(t, 2, got.Wrote[0].Version)
}

func TestIntervenerRetryRestoredVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := newDesk(t)
	_, err := d.iv.Intervene(ctx, curate.Request{
		Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceUndo,
	}, person)
	require.NoError(t, err)
	request := curate.Request{Action: trace.ActionJudge, Target: "k-add:1", Choice: trace.ChoiceKeep}
	_, err = d.failingAt(t, -1, 0).Intervene(ctx, request, person)
	require.ErrorIs(t, err, assert.AnError)
	before, err := d.stores.Ledger.All(ctx)
	require.NoError(t, err)

	got, err := d.iv.Intervene(ctx, request, person)

	require.NoError(t, err)
	after, err := d.stores.Ledger.All(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	require.Len(t, got.Wrote, 1)
	assert.Equal(t, 2, got.Wrote[0].Version)
	assert.Empty(t, d.orphans(t))
}

func TestIntervenerRefusesBeforeCompactionWrite(t *testing.T) {
	type args struct {
		choice trace.Choice
		source trace.Source
	}
	tcs := []struct {
		name string
		args args
	}{
		{"undo names no compaction judgment", args{trace.ChoiceUndo, trace.SourceManual}},
		{"scope names no compaction judgment", args{trace.ChoiceScope, trace.SourceManual}},
		{"a pushed source proves no self selected intervention", args{trace.ChoiceKeep, trace.SourcePush}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := compactionBench(t, "ann")
			before := b.written(t)
			by := person
			by.Source = tc.args.source

			_, err := b.intervener(nil).Intervene(context.Background(), curate.Request{
				Action: trace.ActionJudge, Target: "c-generated", Choice: tc.args.choice,
			}, by)

			assert.ErrorIs(t, err, trace.ErrInterventionInvalid)
			assert.Equal(t, before, b.written(t))
		})
	}
}
