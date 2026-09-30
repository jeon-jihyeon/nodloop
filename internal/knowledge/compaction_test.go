package knowledge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// One folder of conversion items
// 1. a and b are meanings and j is a judgment with a veto and all three are replayable
// 2. p cites only a paragraph so a compaction leaves it out
// 3. far sits in another folder because it names another change context
type compactionSeeds struct {
	a, b, j, p, far knowledge.Knowledge
	at              time.Time
}

func newCompactionSeeds() compactionSeeds {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	conversions := knowledge.Scope{Scope: evidence.Scope{
		ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange, evidence.ContextPlannedChange},
		Metrics:        []string{"conversion_count"},
	}}
	base := knowledge.Knowledge{
		Version: 1, Kind: knowledge.KindMeaning, Scope: conversions, Basis: knowledge.BasisStated,
		Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Author: "author", Time: at,
	}
	a := base
	a.ID, a.Content, a.Evidence = "a", "lag", knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}
	b := base
	b.ID, b.Content, b.Basis = "b", "basis", knowledge.BasisVerified
	b.Evidence = knowledge.Evidence{
		FeedbackTraceIDs: []string{"t2", "t1"}, OutcomeTraceIDs: []string{"o1"}, ParagraphIDs: []string{"p#1"},
	}
	j := base
	j.ID, j.Kind, j.Content, j.Basis = "j", knowledge.KindJudgment, "never sed -i", knowledge.BasisVerified
	j.Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{"t3"}}
	j.Veto = &knowledge.Veto{
		Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`}},
		Example: map[string]any{"command": "sed -i s/a/b/ f"},
	}
	p := base
	p.ID, p.Content, p.Evidence = "p", "paragraph only", knowledge.Evidence{ParagraphIDs: []string{"p#2"}}
	far := base
	far.ID, far.Content, far.Evidence = "far", "clicks", knowledge.Evidence{FeedbackTraceIDs: []string{"t4"}}
	far.Scope = knowledge.Scope{Scope: evidence.Scope{
		ChangeContexts: []evidence.Context{evidence.ContextMeasurementChanged}, Metrics: []string{"click_count"},
	}}
	return compactionSeeds{a: a, b: b, j: j, p: p, far: far, at: at}
}

func (s compactionSeeds) all() []knowledge.Knowledge {
	return []knowledge.Knowledge{s.a, s.b, s.j, s.p, s.far}
}

// A meaning that replaces a and b and a judgment that keeps the veto of j under its id
func (s compactionSeeds) drafts() []knowledge.Knowledge {
	meaning := knowledge.Knowledge{
		Kind: knowledge.KindMeaning, Content: "lag and basis", Scope: s.a.Scope, Author: "claude",
		Evidence: knowledge.Evidence{
			Knowledge: []knowledge.Ref{{ID: "a", Version: 1}, {ID: "b", Version: 1}}, ParagraphIDs: []string{"p#3"},
			FeedbackTraceIDs: []string{"ignored"},
		},
	}
	judgment := knowledge.Knowledge{
		ID: "j", Kind: knowledge.KindJudgment, Content: "never edit in place", Scope: s.a.Scope, Author: "claude",
		Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "j", Version: 1}}},
		Veto: &knowledge.Veto{
			Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`}},
			Example: map[string]any{"command": "sed -i s/c/d/ g"},
		},
	}
	return []knowledge.Knowledge{meaning, judgment}
}

// A ledger over a store in dir whose ids count up per prefix so two generated ids never collide
// Returns the path of its veto file
func newTestLedger(t *testing.T, dir string, at time.Time) (*knowledge.Ledger, string) {
	t.Helper()
	store, err := file.New(dir)
	require.NoError(t, err)
	home := t.TempDir()
	counts := map[string]int{}
	newID := func(prefix string) string {
		counts[prefix]++
		return fmt.Sprintf("%s%d", prefix, counts[prefix])
	}
	l := knowledge.NewLedger(store, vetofile.NewApprovedFile(home, "records"), func() time.Time { return at }, newID)
	return l, vetofile.NewApprovedFile(home, "records").Path()
}

// Drops the newest records from the file as a proposal cut between two appends leaves it
func dropNewest(t *testing.T, dir string, n int) {
	t.Helper()
	path := filepath.Join(dir, "knowledge.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.SplitAfter(string(data), "\n")
	lines = lines[:len(lines)-1-n]
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600))
}

func TestLedgerProposeCompaction(t *testing.T) {
	seeds := newCompactionSeeds()
	now := seeds.at.Add(time.Hour)
	type args struct {
		anchor string
		drafts []knowledge.Knowledge
	}
	type want struct {
		compaction knowledge.Compaction
		err        error
	}
	meaning := knowledge.Knowledge{
		ID: "k-1", Version: 1, Kind: knowledge.KindMeaning, Content: "lag and basis", Scope: seeds.a.Scope,
		Evidence: knowledge.Evidence{
			FeedbackTraceIDs: []string{"t1", "t2"}, OutcomeTraceIDs: []string{"o1"}, ParagraphIDs: []string{"p#1", "p#3"},
			Knowledge: []knowledge.Ref{{ID: "a", Version: 1}, {ID: "b", Version: 1}},
		},
		Basis: knowledge.BasisStated, Status: knowledge.StatusCandidate, Time: now, Author: "claude",
		Compaction: "c-1", CompactionSize: 2,
	}
	judgment := seeds.drafts()[1]
	judgment.Version, judgment.Base, judgment.Basis, judgment.Status = 2, 1, knowledge.BasisVerified, knowledge.StatusCandidate
	judgment.Time, judgment.Compaction, judgment.CompactionSize = now, "c-1", 2
	judgment.Evidence.FeedbackTraceIDs = []string{"t3"}
	replaced := knowledge.Set{seeds.a, seeds.b, seeds.j}

	oneDraft := seeds.drafts()[:1]
	namesNothing := seeds.drafts()
	namesNothing[1].Evidence.Knowledge = nil
	outsideFolder := seeds.drafts()
	outsideFolder[0].Evidence.Knowledge = append(outsideFolder[0].Evidence.Knowledge, knowledge.Ref{ID: "far", Version: 1})
	olderVersion := seeds.drafts()
	olderVersion[0].Evidence.Knowledge = append(olderVersion[0].Evidence.Knowledge, knowledge.Ref{ID: "a", Version: 2})
	outsideID := seeds.drafts()
	outsideID[0].ID = "far"
	repeatedID := seeds.drafts()
	repeatedID[0].ID = "j"
	noContent := seeds.drafts()
	noContent[0].Content = ""
	noVeto := seeds.drafts()
	noVeto[1].Veto = nil
	weakVeto := seeds.drafts()
	weakVeto[1].Veto.When = []knowledge.VetoCondition{{Field: "command", Match: `--in-place`}}
	weakVeto[1].Veto.Example = map[string]any{"command": "sed --in-place s/a/b/ f"}
	exampleVeto := seeds.drafts()
	exampleVeto[1].Veto.When = []knowledge.VetoCondition{{Field: "command", Match: `^sed -i s/a/b/ f$`}}
	exampleVeto[1].Veto.Example = seeds.j.Veto.Example

	planned := []evidence.Context{evidence.ContextPlannedChange}
	overlapping := seeds.drafts()
	second := overlapping[0]
	second.Evidence.Knowledge = []knowledge.Ref{{ID: "b", Version: 1}}
	overlapping[0].Evidence.Knowledge = []knowledge.Ref{{ID: "a", Version: 1}}
	overlapping = append(overlapping, second)
	partitioned := seeds.drafts()
	second.Scope.ChangeContexts = planned
	partitioned[0].Evidence.Knowledge = []knowledge.Ref{{ID: "a", Version: 1}}
	partitioned[0].Exceptions = planned
	partitioned = append(partitioned, second)
	byException := meaning
	byException.Exceptions = planned
	byException.Evidence = knowledge.Evidence{
		FeedbackTraceIDs: []string{"t1"}, ParagraphIDs: []string{"p#3"}, Knowledge: []knowledge.Ref{{ID: "a", Version: 1}},
	}
	byContext := meaning
	byContext.ID, byContext.Basis = "k-2", knowledge.BasisVerified
	byContext.Scope.ChangeContexts = planned
	byContext.Evidence = knowledge.Evidence{
		FeedbackTraceIDs: []string{"t2", "t1"}, OutcomeTraceIDs: []string{"o1"}, ParagraphIDs: []string{"p#1", "p#3"},
		Knowledge: []knowledge.Ref{{ID: "b", Version: 1}},
	}
	threeWay := judgment
	byException.CompactionSize, threeWay.CompactionSize, byContext.CompactionSize = 3, 3, 3

	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"candidates carry the compaction id and evidence built from the old items they name",
			args{"a", seeds.drafts()},
			want{compaction: knowledge.Compaction{ID: "c-1", Items: knowledge.Set{meaning, judgment}, Replaced: replaced}},
		},
		{"an anchor that is not approved is not found", args{"none", seeds.drafts()}, want{err: knowledge.ErrNotFound}},
		{"an anchor that cites only paragraphs is refused", args{"p", seeds.drafts()}, want{err: knowledge.ErrParagraphOnly}},
		{
			"a folder of one replayable item is refused",
			args{"far", seeds.drafts()},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{"no draft is refused", args{"a", nil}, want{err: knowledge.ErrCompactionInvalid}},
		{"an old item no draft names is refused", args{"a", oneDraft}, want{err: knowledge.ErrCompactionInvalid}},
		{"a draft that names nothing is refused", args{"a", namesNothing}, want{err: knowledge.ErrCompactionInvalid}},
		{"a name outside the folder is refused", args{"a", outsideFolder}, want{err: knowledge.ErrCompactionInvalid}},
		{"a name of an older version is refused", args{"a", olderVersion}, want{err: knowledge.ErrCompactionInvalid}},
		{
			"a draft that takes the id of an item outside the compaction is refused",
			args{"a", outsideID},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{"two drafts of one id are refused", args{"a", repeatedID}, want{err: knowledge.ErrCompactionInvalid}},
		{"an invalid draft is refused with its own error", args{"a", noContent}, want{err: knowledge.ErrContentRequired}},
		{"two meanings of one folder overlap", args{"a", overlapping}, want{err: knowledge.ErrCompactionOverlap}},
		{
			"exceptions that cover every context of the other meaning partition them",
			args{"a", partitioned},
			want{compaction: knowledge.Compaction{
				ID: "c-1", Items: knowledge.Set{byException, threeWay, byContext}, Replaced: replaced,
			}},
		},
		{"dropping the veto of an old judgment is refused", args{"a", noVeto}, want{err: knowledge.ErrCompactionVeto}},
		{
			"a new veto that lets the old example through is refused",
			args{"a", weakVeto},
			want{err: knowledge.ErrCompactionVeto},
		},
		{
			"a new veto that blocks only the old example is refused",
			args{"a", exampleVeto},
			want{err: knowledge.ErrCompactionVeto},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), now)
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))

			got, err := l.ProposeCompaction(ctx, tc.args.anchor, tc.args.drafts)
			all, listErr := l.All(ctx)
			require.NoError(t, listErr)
			stored, _ := l.Compaction(ctx, got.ID)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.compaction, got)
			assert.Equal(t, got.Items, stored.Items)
			assert.ElementsMatch(t, got.Replaced, stored.Replaced)
			assert.Len(t, all, len(seeds.all())+len(tc.want.compaction.Items), "a refused compaction appends nothing")
		})
	}
}

// Two meanings of one folder split by the value of one dim
// Both except planned changes so each reaches no known changes only
func TestLedgerProposeCompactionScope(t *testing.T) {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	planned := []evidence.Context{evidence.ContextPlannedChange}
	scope := knowledge.Scope{Scope: evidence.Scope{
		ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange, evidence.ContextPlannedChange},
		Metrics:        []string{"conversion_count", "click_count"},
	}}
	base := knowledge.Knowledge{
		Version: 1, Kind: knowledge.KindMeaning, Exceptions: planned, Basis: knowledge.BasisStated,
		Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Author: "author", Time: at,
	}
	shop, sports := base, base
	shop.ID, shop.Content, shop.Evidence = "shop", "shop fact", knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}
	shop.Scope = scope
	shop.Scope.Dims = map[string]string{"topic": "shopping"}
	sports.ID, sports.Content, sports.Evidence = "sports", "sports fact", knowledge.Evidence{FeedbackTraceIDs: []string{"t2"}}
	sports.Scope = scope
	sports.Scope.Dims = map[string]string{"topic": "sports"}
	draftOf := func(old knowledge.Knowledge, edit func(*knowledge.Knowledge)) knowledge.Knowledge {
		d := knowledge.Knowledge{
			ID: old.ID, Kind: old.Kind, Content: old.Content + " kept", Scope: old.Scope, Exceptions: old.Exceptions,
			Author: "claude", Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: old.ID, Version: 1}}},
		}
		edit(&d)
		return d
	}
	keep := func(*knowledge.Knowledge) {}
	namesBoth := func(d *knowledge.Knowledge) {
		d.ID, d.Evidence.Knowledge = "", []knowledge.Ref{{ID: "shop", Version: 1}, {ID: "sports", Version: 1}}
	}
	merged := func(edit func(*knowledge.Knowledge)) []knowledge.Knowledge {
		return []knowledge.Knowledge{draftOf(shop, func(d *knowledge.Knowledge) {
			namesBoth(d)
			edit(d)
		})}
	}
	type want struct {
		// Dims of every proposed item
		dims []map[string]string
		err  error
	}
	shopping, sporting := map[string]string{"topic": "shopping"}, map[string]string{"topic": "sports"}
	tcs := []struct {
		name   string
		drafts []knowledge.Knowledge
		want   want
	}{
		{
			"dims keep two meanings of one folder apart",
			[]knowledge.Knowledge{draftOf(shop, keep), draftOf(sports, keep)},
			want{dims: []map[string]string{shopping, sporting}},
		},
		{
			"a merged meaning that drops the dims is refused",
			merged(func(d *knowledge.Knowledge) { d.Scope.Dims = nil }),
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"a merged meaning that keeps one dim value would carry the sports fact to shopping events and is refused",
			merged(keep),
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"a draft that drops the exceptions its items share is refused",
			[]knowledge.Knowledge{draftOf(shop, func(d *knowledge.Knowledge) { d.Exceptions = nil }), draftOf(sports, keep)},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"a draft that adds a change context is refused",
			[]knowledge.Knowledge{
				draftOf(shop, func(d *knowledge.Knowledge) {
					d.Scope.ChangeContexts = append(d.Scope.ChangeContexts, evidence.ContextMeasurementChanged)
				}),
				draftOf(sports, keep),
			},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"a draft that adds a metric is refused",
			[]knowledge.Knowledge{
				draftOf(shop, func(d *knowledge.Knowledge) { d.Scope.Metrics = append(d.Scope.Metrics, "order_count") }),
				draftOf(sports, keep),
			},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"a draft without metrics reaches every metric and is refused",
			[]knowledge.Knowledge{draftOf(shop, func(d *knowledge.Knowledge) { d.Scope.Metrics = nil }), draftOf(sports, keep)},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"two meanings split by metrics with none in common pass",
			[]knowledge.Knowledge{
				draftOf(shop, func(d *knowledge.Knowledge) { d.Scope.Metrics = []string{"conversion_count"} }),
				draftOf(shop, func(d *knowledge.Knowledge) { d.ID, d.Scope.Metrics = "", []string{"click_count"} }),
				draftOf(sports, keep),
			},
			want{dims: []map[string]string{shopping, shopping, sporting}},
		},
		{
			"two meanings whose metric lists share one overlap",
			[]knowledge.Knowledge{
				draftOf(shop, func(d *knowledge.Knowledge) { d.Scope.Metrics = []string{"conversion_count"} }),
				draftOf(shop, func(d *knowledge.Knowledge) { d.ID = "" }),
				draftOf(sports, keep),
			},
			want{err: knowledge.ErrCompactionOverlap},
		},
		{
			"two meanings whose dims differ in keys but not in values overlap",
			[]knowledge.Knowledge{
				draftOf(shop, keep),
				draftOf(shop, func(d *knowledge.Knowledge) {
					d.ID, d.Scope.Dims = "", map[string]string{"topic": "shopping", "platform": "ios"}
				}),
				draftOf(sports, keep),
			},
			want{err: knowledge.ErrCompactionOverlap},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), at)
			require.NoError(t, testkit.Err(l.Import(ctx, []knowledge.Knowledge{shop, sports})))

			got, err := l.ProposeCompaction(ctx, "shop", tc.drafts)
			var dims []map[string]string
			for _, k := range got.Items {
				dims = append(dims, k.Scope.Dims)
			}

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.dims, dims)
		})
	}
}

func TestSetPendingCompaction(t *testing.T) {
	seeds := newCompactionSeeds()
	at := seeds.at.Add(time.Hour)
	candidate := func(id string, version int, refs ...knowledge.Ref) knowledge.Knowledge {
		return knowledge.Knowledge{
			ID: id, Version: version, Kind: knowledge.KindMeaning, Content: id, Status: knowledge.StatusCandidate, Time: at,
			Author: "claude", Compaction: "c-1", CompactionSize: 2, Evidence: knowledge.Evidence{Knowledge: refs},
		}
	}
	merged := candidate("k-1", 1, knowledge.Ref{ID: "a", Version: 1}, knowledge.Ref{ID: "b", Version: 1})
	kept := candidate("j", 2, knowledge.Ref{ID: "j", Version: 1})
	moved := func(k knowledge.Knowledge, status knowledge.Status) knowledge.Knowledge {
		k.Status, k.Approver = status, "ann"
		return k
	}
	type args struct {
		// Newest first as the store lists them
		records knowledge.Set
		items   knowledge.Set
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"no compaction", args{knowledge.Set{seeds.a, seeds.b}, knowledge.Set{seeds.a}}, ""},
		{
			"open candidates over the items",
			args{knowledge.Set{kept, merged, seeds.a, seeds.b}, knowledge.Set{seeds.b}},
			"c-1",
		},
		{"open candidates over other items", args{knowledge.Set{kept, merged, seeds.far}, knowledge.Set{seeds.far}}, ""},
		{
			"a retired candidate abandons it",
			args{knowledge.Set{moved(kept, knowledge.StatusRetired), kept, merged}, knowledge.Set{seeds.a}},
			"",
		},
		{
			"every candidate approved closes it",
			args{
				knowledge.Set{moved(kept, knowledge.StatusApproved), moved(merged, knowledge.StatusApproved), kept, merged},
				knowledge.Set{seeds.a},
			},
			"",
		},
		{
			"an approval cut after one candidate is still pending",
			args{knowledge.Set{moved(merged, knowledge.StatusApproved), kept, merged}, knowledge.Set{seeds.j}},
			"c-1",
		},
		{
			"a proposal cut after one candidate can never be approved so it is not pending",
			args{knowledge.Set{merged, seeds.a, seeds.b}, knowledge.Set{seeds.a}},
			"",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.records.PendingCompaction(tc.args.items))
		})
	}
}

// A second proposal over the same folder waits until the first is abandoned or can never be approved
func TestLedgerProposeCompactionAgain(t *testing.T) {
	type args struct {
		// Candidates of the first proposal retired before the second
		retired []knowledge.Ref
		// Newest records dropped as if the first proposal was cut between two appends
		cut int
	}
	type want struct {
		err     error
		records int
		// Approving the second proposal after a passing replay
		approveErr error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"refused while the first is pending", args{}, want{knowledge.ErrCompactionPending, 7, knowledge.ErrNotFound}},
		{
			"accepted once a candidate of the first is retired",
			args{retired: []knowledge.Ref{{ID: "k-1", Version: 1}}},
			want{nil, 10, nil},
		},
		{"a proposal cut between two appends is replaced by the retry", args{cut: 1}, want{nil, 8, nil}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seeds := newCompactionSeeds()
			dir := t.TempDir()
			l, _ := newTestLedger(t, dir, seeds.at.Add(time.Hour))
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			_, err := l.ProposeCompaction(ctx, "a", seeds.drafts())
			require.NoError(t, err)
			for _, ref := range tc.args.retired {
				_, err = l.Retire(ctx, ref.ID, ref.Version, "ann")
				require.NoError(t, err)
			}
			dropNewest(t, dir, tc.args.cut)

			second, err := l.ProposeCompaction(ctx, "b", seeds.drafts())
			all, listErr := l.All(ctx)
			require.NoError(t, listErr)
			_, approveErr := l.ApproveCompaction(ctx, second.ID, "jed", knowledge.Replay{
				Compaction: second.ID,
				Events:     []knowledge.ReplayEvent{{EventID: "e1", Expected: evidence.StatusHold, Got: evidence.StatusHold}},
			})

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, all, tc.want.records)
			assert.ErrorIs(t, approveErr, tc.want.approveErr)
		})
	}
}

func TestLedgerProposeCompactionVetoTools(t *testing.T) {
	type args struct {
		oldTools string
		newTools string
	}
	type want struct {
		items int
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"changed tool is refused", args{"Bash", "Write"}, want{0, knowledge.ErrCompactionVeto}},
		{"removed tool is refused", args{"Bash|Write", "Bash"}, want{0, knowledge.ErrCompactionVeto}},
		{"added tool is accepted", args{"Bash", "Bash|Write"}, want{2, nil}},
		{"reordered tools and whitespace are accepted", args{" Bash | Write ", "Write|Bash"}, want{2, nil}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seeds := newCompactionSeeds()
			seeds.j.Veto.Tool = tc.args.oldTools
			l, _ := newTestLedger(t, t.TempDir(), seeds.at.Add(time.Hour))
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			drafts := seeds.drafts()
			drafts[1].Veto.Tool = tc.args.newTools

			got, err := l.ProposeCompaction(ctx, "a", drafts)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, got.Items, tc.want.items)
			all, err := l.All(ctx)
			require.NoError(t, err)
			assert.Len(t, all, len(seeds.all())+tc.want.items)
		})
	}
}

// The old veto of j blocks sed -i on a go file unless the file is generated
func TestLedgerProposeCompactionVetoConditions(t *testing.T) {
	sed := knowledge.VetoCondition{Field: "command", Match: `sed\s+-i`}
	goFile := knowledge.VetoCondition{Field: "command", Match: `\.go\b`, Unless: `_gen\.go`}
	type args struct {
		when    []knowledge.VetoCondition
		example string
	}
	type want struct {
		items int
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"conditions kept as written are accepted", args{[]knowledge.VetoCondition{sed, goFile}, "sed -i s/a/b/ f.go"}, want{2, nil}},
		{"a dropped condition is accepted", args{[]knowledge.VetoCondition{sed}, "sed -i s/a/b/ f.md"}, want{2, nil}},
		{
			"a dropped unless is accepted",
			args{[]knowledge.VetoCondition{sed, {Field: "command", Match: `\.go\b`}}, "sed -i s/a/b/ f_gen.go"},
			want{2, nil},
		},
		{
			"a match narrowed to the old example is refused",
			args{[]knowledge.VetoCondition{{Field: "command", Match: `^sed -i s/a/b/ f\.go$`}}, "sed -i s/a/b/ f.go"},
			want{0, knowledge.ErrCompactionVeto},
		},
		{
			"a widened match is refused because inclusion is only checked as written",
			args{[]knowledge.VetoCondition{{Field: "command", Match: `sed\s+(-i|--in-place)`}}, "sed -i s/a/b/ f.go"},
			want{0, knowledge.ErrCompactionVeto},
		},
		{
			"an added unless is refused",
			args{[]knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`, Unless: `\.md`}}, "sed -i s/a/b/ f.go"},
			want{0, knowledge.ErrCompactionVeto},
		},
		{
			"a changed unless is refused",
			args{[]knowledge.VetoCondition{sed, {Field: "command", Match: `\.go\b`, Unless: `_test\.go`}}, "sed -i s/a/b/ f.go"},
			want{0, knowledge.ErrCompactionVeto},
		},
		{
			"an added condition is refused",
			args{[]knowledge.VetoCondition{sed, goFile, {Field: "command", Match: `\sf\.go$`}}, "sed -i s/a/b/ f.go"},
			want{0, knowledge.ErrCompactionVeto},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seeds := newCompactionSeeds()
			seeds.j.Veto.When = []knowledge.VetoCondition{sed, goFile}
			seeds.j.Veto.Example = map[string]any{"command": "sed -i s/a/b/ f.go"}
			l, _ := newTestLedger(t, t.TempDir(), seeds.at.Add(time.Hour))
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			drafts := seeds.drafts()
			drafts[1].Veto.When = tc.args.when
			drafts[1].Veto.Example = map[string]any{"command": tc.args.example}

			got, err := l.ProposeCompaction(ctx, "a", drafts)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, got.Items, tc.want.items)
			all, err := l.All(ctx)
			require.NoError(t, err)
			assert.Len(t, all, len(seeds.all())+tc.want.items)
		})
	}
}

func TestLedgerApproveRefusesCompactionCandidate(t *testing.T) {
	tcs := []struct {
		name string
		args int
	}{
		{"meaning candidate", 0},
		{"judgment candidate with a veto", 1},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seeds := newCompactionSeeds()
			ledger, _ := newTestLedger(t, t.TempDir(), seeds.at)
			require.NoError(t, testkit.Err(ledger.Import(ctx, seeds.all())))
			drafts := seeds.drafts()
			drafts[0].ID = "a"
			proposed, err := ledger.ProposeCompaction(ctx, "a", drafts)
			require.NoError(t, err)
			require.Greater(t, len(proposed.Items), tc.args)
			before, err := ledger.All(ctx)
			require.NoError(t, err)
			_, err = ledger.Approve(ctx, proposed.Items[tc.args].ID, proposed.Items[tc.args].Version, "reviewer")
			assert.ErrorIs(t, err, knowledge.ErrCompactionInvalid)
			after, err := ledger.All(ctx)
			assert.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestLedgerCompactionCompleteness(t *testing.T) {
	type args struct {
		sizes []int
	}
	type want struct {
		err      error
		items    int
		appended int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"complete proposal remains approvable after retry", args{[]int{2, 2}}, want{nil, 2, 5}},
		{"partial proposal cannot be approved", args{[]int{2}}, want{knowledge.ErrCompactionIncomplete, 0, 0}},
		{"legacy proposal requires a new proposal", args{[]int{0, 0}}, want{knowledge.ErrCompactionIncomplete, 0, 0}},
		{"inconsistent sizes are refused", args{[]int{2, 3}}, want{knowledge.ErrCompactionIncomplete, 0, 0}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seeds := newCompactionSeeds()
			dir := t.TempDir()
			l, vetoPath := newTestLedger(t, dir, seeds.at)
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			drafts := seeds.drafts()
			drafts[0].Evidence.Knowledge = append(drafts[0].Evidence.Knowledge, knowledge.Ref{ID: "j", Version: 1})
			proposed, err := l.ProposeCompaction(ctx, "a", drafts)
			require.NoError(t, err)
			path := filepath.Join(dir, "knowledge.jsonl")
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			lines = lines[:len(seeds.all())+len(tc.args.sizes)]
			for i, size := range tc.args.sizes {
				candidate := proposed.Items[i]
				candidate.CompactionSize = size
				b, err := json.Marshal(candidate)
				require.NoError(t, err)
				lines[len(seeds.all())+i] = string(b)
			}
			require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
			before, err := l.All(ctx)
			require.NoError(t, err)
			id := before[0].Compaction
			got, lookupErr := l.Compaction(ctx, id)
			_, previewErr := l.Preview(ctx, id)
			_, individualErr := l.Approve(ctx, proposed.Items[0].ID, proposed.Items[0].Version, "jed")
			replay := knowledge.Replay{Compaction: id, Events: []knowledge.ReplayEvent{
				{EventID: "e1", Expected: evidence.StatusHold, Got: evidence.StatusHold},
			}}
			_, approveErr := l.ApproveCompaction(ctx, id, "jed", replay)
			after, err := l.All(ctx)
			require.NoError(t, err)
			_, retryErr := l.ApproveCompaction(ctx, id, "jed", replay)
			retried, err := l.All(ctx)
			require.NoError(t, err)
			vetoes, err := os.ReadFile(vetoPath)
			require.NoError(t, err)

			assert.ErrorIs(t, lookupErr, tc.want.err)
			assert.ErrorIs(t, previewErr, tc.want.err)
			assert.ErrorIs(t, individualErr, knowledge.ErrCompactionInvalid)
			assert.ErrorIs(t, approveErr, tc.want.err)
			assert.ErrorIs(t, retryErr, tc.want.err)
			assert.Len(t, got.Items, tc.want.items)
			assert.Equal(t, tc.want.appended, len(after)-len(before))
			assert.Len(t, after.Vetoes(), 1)
			assert.Contains(t, string(vetoes), "id: j")
			assert.Equal(t, after, retried)
		})
	}
}

// The preview reads as approved while the ledger keeps its records
func TestPreview(t *testing.T) {
	seeds := newCompactionSeeds()
	now := seeds.at.Add(time.Hour)
	tcs := []struct {
		name string
		args knowledge.Ref
		want error
	}{
		{"a new item reads as approved", knowledge.Ref{ID: "k-1", Version: 1}, nil},
		{"a kept id reads at its new version", knowledge.Ref{ID: "j", Version: 2}, nil},
		{
			"the old version of a kept id reads as superseded",
			knowledge.Ref{ID: "j", Version: 1},
			knowledge.ErrVersionUnapproved,
		},
		{"a replaced item reads as retired", knowledge.Ref{ID: "a", Version: 1}, knowledge.ErrVersionUnapproved},
		{"an item outside the compaction reads as before", knowledge.Ref{ID: "far", Version: 1}, nil},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), now)
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			c, err := l.ProposeCompaction(ctx, "a", seeds.drafts())
			require.NoError(t, err)
			before, err := l.All(ctx)
			require.NoError(t, err)

			preview, err := l.Preview(ctx, c.ID)
			require.NoError(t, err)
			set, err := preview.All(ctx)
			require.NoError(t, err)
			var approved []string
			for _, k := range set.Approved() {
				approved = append(approved, fmt.Sprintf("%s v%d", k.ID, k.Version))
			}
			_, lookupErr := preview.Approved(ctx, tc.args.ID, tc.args.Version)
			after, err := l.All(ctx)
			require.NoError(t, err)

			assert.Equal(t, []string{"far v1", "j v2", "k-1 v1", "p v1"}, approved)
			assert.ErrorIs(t, lookupErr, tc.want)
			assert.Equal(t, before, after)
		})
	}
}

func TestLedgerApproveCompaction(t *testing.T) {
	seeds := newCompactionSeeds()
	now := seeds.at.Add(time.Hour)
	type args struct {
		// Versions retired after the proposal
		retired []knowledge.Ref
		// Candidates appended as approved after the proposal as a cut approval leaves them
		approved []knowledge.Ref
		replay   knowledge.Replay
		approver string
		extra    []knowledge.Knowledge
	}
	type want struct {
		// id version status and compaction of every record the approval appended in order
		appended []string
		// id version status of every item the approval returned
		items  []string
		vetoes bool
		err    error
	}
	passed := knowledge.Replay{Compaction: "c-1", Events: []knowledge.ReplayEvent{
		{EventID: "e1", Expected: evidence.StatusNoAction, Got: evidence.StatusNoAction, TraceID: "r1"},
	}}
	failed := passed
	failed.Events = []knowledge.ReplayEvent{{EventID: "e1", Expected: evidence.StatusNoAction, Got: evidence.StatusHold}}
	missing := passed
	never := knowledge.ReplayEvent{EventID: "e2", Expected: evidence.StatusHold}
	missing.Events = append(slices.Clone(passed.Events), never)
	other := passed
	other.Compaction = "c-other"
	large := seeds.p
	large.ID, large.Content = "large", strings.Repeat("가", knowledge.ReviewChars)
	success := []string{
		"k-1 v1 approved c-1", "j v2 approved c-1", "j v1 superseded c-1", "a v1 retired c-1", "b v1 retired c-1",
	}
	items := []string{"k-1 v1 approved", "j v2 approved"}
	candidate := []knowledge.Ref{{ID: "k-1", Version: 1}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"approves the new items and retires the old ones",
			args{replay: passed, approver: "jed"},
			want{appended: success, items: items, vetoes: true},
		},
		{
			"a second call after a partial append appends only what is missing",
			args{approved: candidate, replay: passed, approver: "jed"},
			want{appended: success[1:], items: items, vetoes: true},
		},
		{"refused without a replay", args{approver: "jed"}, want{err: knowledge.ErrReplayNotPassed}},
		{"refused after a failed replay", args{replay: failed, approver: "jed"}, want{err: knowledge.ErrReplayNotPassed}},
		{
			"refused when an event was never replayed",
			args{replay: missing, approver: "jed"},
			want{err: knowledge.ErrReplayNotPassed},
		},
		{
			"refused with the replay of another compaction",
			args{replay: other, approver: "jed"},
			want{err: knowledge.ErrReplayNotPassed},
		},
		{
			"refused when an old item changed since the proposal",
			args{retired: []knowledge.Ref{{ID: "b", Version: 1}}, replay: passed, approver: "jed"},
			want{err: knowledge.ErrCompactionOutdated},
		},
		{
			"refused when a candidate of the compaction was retired",
			args{retired: candidate, replay: passed, approver: "jed"},
			want{err: knowledge.ErrTransitionInvalid},
		},
		{
			"refused when a new item's folder would outgrow the review",
			args{replay: passed, approver: "jed", extra: []knowledge.Knowledge{large}},
			want{err: knowledge.ErrFolderFull},
		},
		{"refused without an approver", args{replay: passed}, want{err: knowledge.ErrApproverRequired}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, vetoPath := newTestLedger(t, t.TempDir(), now)
			require.NoError(t, testkit.Err(l.Import(ctx, append(seeds.all(), tc.args.extra...))))
			proposed, err := l.ProposeCompaction(ctx, "a", seeds.drafts())
			require.NoError(t, err)
			for _, ref := range tc.args.retired {
				_, err = l.Retire(ctx, ref.ID, ref.Version, "ann")
				require.NoError(t, err)
			}
			for _, ref := range tc.args.approved {
				history, err := l.History(ctx, ref.ID)
				require.NoError(t, err)
				k := history[0]
				k.Status, k.Approver = knowledge.StatusApproved, "jed"
				require.NoError(t, testkit.Err(l.Import(ctx, []knowledge.Knowledge{k})))
			}
			before, err := l.All(ctx)
			require.NoError(t, err)
			require.NoError(t, os.RemoveAll(vetoPath))

			got, err := l.ApproveCompaction(ctx, proposed.ID, tc.args.approver, tc.args.replay)
			all, listErr := l.All(ctx)
			require.NoError(t, listErr)
			var appended, approved []string
			for _, k := range all[:len(all)-len(before)] {
				appended = append([]string{fmt.Sprintf("%s v%d %s %s", k.ID, k.Version, k.Status, k.Compaction)}, appended...)
			}
			for _, k := range got.Items {
				approved = append(approved, fmt.Sprintf("%s v%d %s", k.ID, k.Version, k.Status))
			}
			vetoes, _ := os.ReadFile(vetoPath)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.appended, appended)
			assert.Equal(t, tc.want.items, approved)
			assert.Equal(t, tc.want.vetoes, strings.Contains(string(vetoes), "id: j"))
		})
	}
}

func TestReplayPassed(t *testing.T) {
	ok := knowledge.ReplayEvent{EventID: "e1", Expected: evidence.StatusHold, Got: evidence.StatusHold}
	tcs := []struct {
		name string
		args knowledge.Replay
		want bool
	}{
		{"no event does not pass", knowledge.Replay{}, false},
		{"every event at its expectation passes", knowledge.Replay{Events: []knowledge.ReplayEvent{ok}}, true},
		{
			"a missing review fails",
			knowledge.Replay{Events: []knowledge.ReplayEvent{ok, {EventID: "e2", Expected: evidence.StatusHold}}},
			false,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Passed())
		})
	}
}

// A narrowed version proposed while a compaction of its id waited for the replay
// Approving it after the compaction would drop what the compaction merged so it is refused in either order
func TestLedgerNarrowedDuringCompaction(t *testing.T) {
	seeds := newCompactionSeeds()
	passed := knowledge.Replay{Compaction: "c-1", Events: []knowledge.ReplayEvent{
		{EventID: "e1", Expected: evidence.StatusNoAction, Got: evidence.StatusNoAction, TraceID: "r1"},
	}}
	type want struct {
		narrowErr, compactionErr error
		// id version and status of the approved j in the end
		approved string
	}
	tcs := []struct {
		name string
		// The approvals in the order they run
		args []string
		want want
	}{
		{"the compaction lands first and the narrowed version is refused", []string{"compaction", "narrowed"}, want{knowledge.ErrCandidateOutdated, nil, "j v2"}},
		{"the narrowed version lands first and the compaction is refused", []string{"narrowed", "compaction"}, want{nil, knowledge.ErrTransitionInvalid, "j v3"}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			now := seeds.at
			counts := map[string]int{}
			l := knowledge.NewLedger(store, vetofile.NewApprovedFile(t.TempDir(), "records"), func() time.Time {
				now = now.Add(time.Minute)
				return now
			}, func(prefix string) string {
				counts[prefix]++
				return fmt.Sprintf("%s%d", prefix, counts[prefix])
			})
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			_, err = l.ProposeCompaction(ctx, "a", seeds.drafts())
			require.NoError(t, err)
			narrowed, _, err := l.Narrow(ctx, "j", 1, []evidence.Context{evidence.ContextPlannedChange}, []string{"r9"}, "jed")
			require.NoError(t, err)
			require.Equal(t, 3, narrowed.Version)

			approvals := map[string]func() error{
				"narrowed":   func() error { return testkit.Err(l.Approve(ctx, "j", 3, "jed")) },
				"compaction": func() error { return testkit.Err(l.ApproveCompaction(ctx, "c-1", "jed", passed)) },
			}
			errs := map[string]error{}
			for _, step := range tc.args {
				errs[step] = approvals[step]()
			}

			assert.ErrorIs(t, errs["narrowed"], tc.want.narrowErr)
			assert.ErrorIs(t, errs["compaction"], tc.want.compactionErr)
			version, err := l.ApprovedVersion(ctx, "j")
			require.NoError(t, err)
			assert.Equal(t, tc.want.approved, fmt.Sprintf("j v%d", version))
		})
	}
}

// A candidate built on a version that a compaction retired
// Approving it would bring back what the compaction merged so it is refused however the steps interleave
func TestLedgerCandidateOfCompactedVersion(t *testing.T) {
	seeds := newCompactionSeeds()
	passed := knowledge.Replay{Compaction: "c-1", Events: []knowledge.ReplayEvent{
		{EventID: "e1", Expected: evidence.StatusNoAction, Got: evidence.StatusNoAction, TraceID: "r1"},
	}}
	reword := func(id string) knowledge.Knowledge {
		return knowledge.Knowledge{
			ID: id, Kind: knowledge.KindMeaning, Content: id + " reworded", Scope: seeds.b.Scope,
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t9"}}, Author: "jed",
		}
	}
	stacked := reword("b")
	stacked.Version, stacked.Base, stacked.Status, stacked.Time = 3, 2, knowledge.StatusCandidate, seeds.at.Add(time.Minute)
	stacked.Basis = knowledge.BasisStated
	steps := map[string]func(context.Context, *knowledge.Ledger) error{
		"propose b": func(ctx context.Context, l *knowledge.Ledger) error {
			_, _, err := l.Propose(ctx, reword("b"))
			return err
		},
		"propose k-1": func(ctx context.Context, l *knowledge.Ledger) error {
			_, _, err := l.Propose(ctx, reword("k-1"))
			return err
		},
		"stack b v3 on v2": func(ctx context.Context, l *knowledge.Ledger) error {
			return testkit.Err(l.Import(ctx, []knowledge.Knowledge{stacked}))
		},
		"propose compaction": func(ctx context.Context, l *knowledge.Ledger) error {
			return testkit.Err(l.ProposeCompaction(ctx, "a", seeds.drafts()))
		},
		"approve compaction": func(ctx context.Context, l *knowledge.Ledger) error {
			return testkit.Err(l.ApproveCompaction(ctx, "c-1", "jed", passed))
		},
		"approve b v2": func(ctx context.Context, l *knowledge.Ledger) error {
			return testkit.Err(l.Approve(ctx, "b", 2, "jed"))
		},
		"approve b v3": func(ctx context.Context, l *knowledge.Ledger) error {
			return testkit.Err(l.Approve(ctx, "b", 3, "jed"))
		},
		"approve k-1 v2": func(ctx context.Context, l *knowledge.Ledger) error {
			return testkit.Err(l.Approve(ctx, "k-1", 2, "jed"))
		},
		"retire b v1": func(ctx context.Context, l *knowledge.Ledger) error { return testkit.Err(l.Retire(ctx, "b", 1, "jed")) },
		"retire k-1 v1": func(ctx context.Context, l *knowledge.Ledger) error {
			return testkit.Err(l.Retire(ctx, "k-1", 1, "jed"))
		},
	}
	type want struct {
		// The error of the last step
		err error
		// The approved version of the id the last step names and empty when none
		approved string
	}
	tcs := []struct {
		name string
		// The steps in the order they run
		args []string
		want want
	}{
		{
			"a candidate proposed before the compaction retired its base is refused",
			[]string{"propose b", "propose compaction", "approve compaction", "approve b v2"},
			want{knowledge.ErrCandidateOutdated, ""},
		},
		{
			"a candidate approved before the compaction makes the compaction outdated",
			[]string{"propose b", "propose compaction", "approve b v2", "approve compaction"},
			want{knowledge.ErrCompactionOutdated, "b v2"},
		},
		{
			"a candidate proposed after the compaction starts a new history and is approved",
			[]string{"propose compaction", "approve compaction", "propose b", "approve b v2"},
			want{nil, "b v2"},
		},
		{
			"a candidate stacked on one built before the compaction is refused",
			[]string{"propose b", "propose compaction", "approve compaction", "stack b v3 on v2", "approve b v3"},
			want{knowledge.ErrCandidateOutdated, ""},
		},
		{
			"a candidate proposed on top of a pending one after the compaction is refused",
			[]string{"propose b", "propose compaction", "approve compaction", "propose b", "approve b v3"},
			want{knowledge.ErrCandidateOutdated, ""},
		},
		{
			"a candidate whose base a person retired is approved",
			[]string{"propose b", "retire b v1", "approve b v2"},
			want{nil, "b v2"},
		},
		{
			"a candidate of a compacted item whose base a person retired is approved",
			[]string{"propose compaction", "approve compaction", "propose k-1", "retire k-1 v1", "approve k-1 v2"},
			want{nil, "k-1 v2"},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), seeds.at.Add(time.Hour))
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			last := len(tc.args) - 1
			for _, step := range tc.args[:last] {
				require.NoError(t, steps[step](ctx, l), step)
			}

			err := steps[tc.args[last]](ctx, l)

			assert.ErrorIs(t, err, tc.want.err)
			id := strings.Fields(tc.args[last])[1]
			if id == "compaction" {
				id = "b"
			}
			approved := ""
			if version, verr := l.ApprovedVersion(ctx, id); verr == nil {
				approved = fmt.Sprintf("%s v%d", id, version)
			}
			assert.Equal(t, tc.want.approved, approved)
		})
	}
}

// One unscoped meaning g and one replayable meaning per change context
// Each review carries g and the item of its change context only
func perContextSeeds(at time.Time) []knowledge.Knowledge {
	base := knowledge.Knowledge{
		Version: 1, Kind: knowledge.KindMeaning, Basis: knowledge.BasisStated,
		Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Author: "author", Time: at,
	}
	g := base
	g.ID, g.Content, g.Evidence = "g", "general fact", knowledge.Evidence{FeedbackTraceIDs: []string{"t-g"}}
	out := []knowledge.Knowledge{g}
	for _, c := range evidence.Contexts() {
		k := base
		k.ID, k.Content = string(c), string(c)+" fact"
		k.Scope = knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{c}}}
		k.Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{"t-" + string(c)}}
		out = append(out, k)
	}
	return out
}

// Crowding counts the items one review carries and never the union of every change context the anchor spans
func TestLedgerFolderCrowdedPerChangeContext(t *testing.T) {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	quiet := []evidence.Context{evidence.ContextNoKnownChange}
	planned := []evidence.Context{evidence.ContextPlannedChange}
	item := func(id string, contexts []evidence.Context) knowledge.Knowledge {
		return knowledge.Knowledge{
			ID: id, Version: 1, Kind: knowledge.KindMeaning, Content: id + " fact",
			Scope:    knowledge.Scope{Scope: evidence.Scope{ChangeContexts: contexts}},
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t-" + id}}, Basis: knowledge.BasisStated,
			Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Author: "author", Time: at,
		}
	}
	unscoped := []knowledge.Knowledge{item("g", nil)}
	split := []knowledge.Knowledge{item("g", nil)}
	for i := range 5 {
		unscoped = append(unscoped, item(fmt.Sprintf("u%d", i), nil))
	}
	for i := range 3 {
		split = append(split, item(fmt.Sprintf("q%d", i), quiet), item(fmt.Sprintf("p%d", i), planned))
	}
	type want struct {
		compactable int
		crowded     bool
	}
	tcs := []struct {
		name string
		args []knowledge.Knowledge
		want want
	}{
		{"an unscoped item beside one item per change context is not crowded", perContextSeeds(at), want{2, false}},
		{"six unscoped items are crowded", unscoped, want{6, true}},
		{
			"an unscoped item beside three items in each of two change contexts counts itself in both and is not crowded",
			split, want{4, false},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), at)
			require.NoError(t, testkit.Err(l.Import(ctx, tc.args)))
			all, err := l.All(ctx)
			require.NoError(t, err)
			compactable, err := all.Compactable("g")
			require.NoError(t, err)

			got, err := l.Folder(ctx, "g", 1)

			require.NoError(t, err)
			assert.Equal(t, tc.want, want{got.Compactable, got.Crowded()})
			assert.Equal(t, tc.want.crowded, compactable.Crowded())
			assert.Len(t, compactable.Items, len(tc.args), "a compaction still covers the union")
		})
	}
}

// A compaction of the per change context folder keeps each fact on the events its item reached
func TestLedgerProposeCompactionPerChangeContext(t *testing.T) {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	seeds := perContextSeeds(at)
	general := knowledge.Ref{ID: "g", Version: 1}
	perContext := func(withGeneral bool) []knowledge.Knowledge {
		out := make([]knowledge.Knowledge, 0, len(seeds)-1)
		for i, old := range seeds[1:] {
			d := knowledge.Knowledge{
				ID: old.ID, Kind: knowledge.KindMeaning, Content: old.Content, Scope: old.Scope, Author: "claude",
				Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: old.ID, Version: 1}}},
			}
			if withGeneral {
				d.Content += " and general fact"
				d.Evidence.Knowledge = append(d.Evidence.Knowledge, general)
			}
			if i == 0 && withGeneral {
				d.ID = "g"
			}
			out = append(out, d)
		}
		return out
	}
	allRefs := make([]knowledge.Ref, 0, len(seeds))
	for _, k := range seeds {
		allRefs = append(allRefs, knowledge.Ref{ID: k.ID, Version: 1})
	}
	merged := []knowledge.Knowledge{{
		ID: "g", Kind: knowledge.KindMeaning, Content: "every fact", Author: "claude",
		Evidence: knowledge.Evidence{Knowledge: allRefs},
	}}
	beside := append(perContext(false), knowledge.Knowledge{
		ID: "g", Kind: knowledge.KindMeaning, Content: "general fact", Author: "claude",
		Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{general}},
	})
	type want struct {
		err error
		// A part of the error text
		message string
		items   int
	}
	tcs := []struct {
		name string
		args []knowledge.Knowledge
		want want
	}{
		{
			"a merged unscoped draft would carry each fact to every change context and is refused",
			merged, want{knowledge.ErrCompactionInvalid, "facts of planned_operational_change to events of change contexts no_known_change", 0},
		},
		{"one draft per change context that repeats the general fact passes", perContext(true), want{nil, "", 5}},
		{"a general draft beside the change context drafts overlaps them", beside, want{knowledge.ErrCompactionOverlap, "", 0}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), at)
			require.NoError(t, testkit.Err(l.Import(ctx, seeds)))

			got, err := l.ProposeCompaction(ctx, "g", tc.args)

			assert.ErrorIs(t, err, tc.want.err)
			if tc.want.message != "" {
				assert.ErrorContains(t, err, tc.want.message)
			}
			assert.Len(t, got.Items, tc.want.items)
		})
	}
}

// A folder of one change context whose meanings are scoped to different metrics compacts into one draft per metric
// and the folder then takes approvals again
func TestLedgerProposeCompactionPerMetric(t *testing.T) {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	quiet := []evidence.Context{evidence.ContextNoKnownChange}
	metrics := []string{"click_count", "conversion_count", "impression_count"}
	// Twelve approved meanings of no_known_change that cycle through the metrics
	var seeds []knowledge.Knowledge
	for i := range 12 {
		seeds = append(seeds, knowledge.Knowledge{
			ID: fmt.Sprintf("k%02d", i), Version: 1, Kind: knowledge.KindMeaning, Content: fmt.Sprintf("fact %d", i),
			Scope:    knowledge.Scope{Scope: evidence.Scope{ChangeContexts: quiet, Metrics: []string{metrics[i%len(metrics)]}}},
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{fmt.Sprintf("t%d", i)}}, Basis: knowledge.BasisStated,
			Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Author: "author", Time: at,
		})
	}
	// One draft over the seeds of each listed metric group scoped to the given metrics
	draft := func(groups []string, scoped []string) knowledge.Knowledge {
		d := knowledge.Knowledge{
			Kind: knowledge.KindMeaning, Content: strings.Join(groups, " and ") + " facts", Author: "claude",
			Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: quiet, Metrics: scoped}},
		}
		for _, k := range seeds {
			if slices.Contains(groups, k.Scope.Metrics[0]) {
				d.Evidence.Knowledge = append(d.Evidence.Knowledge, knowledge.Ref{ID: k.ID, Version: 1})
			}
		}
		return d
	}
	perMetric := []knowledge.Knowledge{
		draft(metrics[:1], metrics[:1]), draft(metrics[1:2], metrics[1:2]), draft(metrics[2:], metrics[2:]),
	}
	// The click facts split in two drafts that both name click_count
	clicksTwice := slices.Concat(perMetric, []knowledge.Knowledge{draft(metrics[:1], metrics[:1])})
	type want struct {
		err error
		// Items the compaction proposes
		items int
		// The error of approving one more item of the folder after the compaction was approved
		next error
	}
	tcs := []struct {
		name string
		args []knowledge.Knowledge
		want want
	}{
		{"one draft per metric passes and the folder takes the next approval", perMetric, want{nil, 3, nil}},
		{
			"a merged draft over every metric reaches metrics each old item never reached and is refused",
			[]knowledge.Knowledge{draft(metrics, metrics)},
			want{knowledge.ErrCompactionInvalid, 0, knowledge.ErrFolderFull},
		},
		{
			"a merged draft without metrics reaches every metric and is refused",
			[]knowledge.Knowledge{draft(metrics, nil)},
			want{knowledge.ErrCompactionInvalid, 0, knowledge.ErrFolderFull},
		},
		{
			"two drafts of one metric overlap and are refused",
			clicksTwice,
			want{knowledge.ErrCompactionOverlap, 0, knowledge.ErrFolderFull},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), at)
			require.NoError(t, testkit.Err(l.Import(ctx, seeds)))

			got, err := l.ProposeCompaction(ctx, "k00", tc.args)
			if err == nil {
				require.NoError(t, testkit.Err(l.ApproveCompaction(ctx, got.ID, "jed", knowledge.Replay{
					Compaction: got.ID,
					Events:     []knowledge.ReplayEvent{{EventID: "e1", Expected: evidence.StatusHold, Got: evidence.StatusHold}},
				})))
			}
			next := knowledge.Knowledge{
				ID: "k-next", Kind: knowledge.KindMeaning, Content: "next fact", Author: "author",
				Scope:    knowledge.Scope{Scope: evidence.Scope{ChangeContexts: quiet, Metrics: metrics[:1]}},
				Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t-next"}},
			}
			_, _, proposeErr := l.Propose(ctx, next)
			require.NoError(t, proposeErr)
			_, nextErr := l.Approve(ctx, "k-next", 1, "jed")

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, got.Items, tc.want.items)
			assert.ErrorIs(t, nextErr, tc.want.next)
		})
	}
}
