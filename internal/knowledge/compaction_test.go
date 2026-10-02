package knowledge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// One folder of commit and push runs in the nodloop repo
// 1. a and b are meanings and j is a judgment with a veto
// 2. j acts through the guard so only a compaction anchored at j covers it
// 3. far sits in another folder because it names another repo
type compactionSeeds struct {
	a, b, j, far knowledge.Knowledge
	at           time.Time
}

func newCompactionSeeds() compactionSeeds {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	base := knowledge.Knowledge{
		Version: 1, Kind: knowledge.KindMeaning, Basis: knowledge.BasisStated,
		Run:    &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}, "task": {"commit", "push"}}},
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
	far := base
	far.ID, far.Content, far.Evidence = "far", "clicks", knowledge.Evidence{FeedbackTraceIDs: []string{"t4"}}
	far.Run = &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"other"}}}
	return compactionSeeds{a: a, b: b, j: j, far: far, at: at}
}

func (s compactionSeeds) all() []knowledge.Knowledge {
	return []knowledge.Knowledge{s.a, s.b, s.j, s.far}
}

// A meaning that replaces a and b and a judgment that keeps the veto of j under its id
func (s compactionSeeds) drafts() []knowledge.Knowledge {
	meaning := knowledge.Knowledge{
		Kind: knowledge.KindMeaning, Content: "lag and basis", Run: s.a.Run, Author: "claude",
		Evidence: knowledge.Evidence{
			Knowledge: []knowledge.Ref{{ID: "a", Version: 1}, {ID: "b", Version: 1}}, ParagraphIDs: []string{"p#3"},
			FeedbackTraceIDs: []string{"ignored"},
		},
	}
	judgment := knowledge.Knowledge{
		ID: "j", Kind: knowledge.KindJudgment, Content: "never edit in place", Run: s.a.Run, Author: "claude",
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

// A coverage of the compaction that states every old item by every new item and loses nothing
func coverage(c knowledge.Compaction) knowledge.Coverage {
	by := make([]knowledge.Ref, 0, len(c.Items))
	for _, k := range c.Items {
		by = append(by, knowledge.Ref{ID: k.ID, Version: k.Version})
	}
	cv := knowledge.Coverage{Compaction: c.ID}
	for _, old := range c.Replaced {
		cv.Items = append(cv.Items, knowledge.CoverageItem{Old: knowledge.Ref{ID: old.ID, Version: old.Version}, CoveredBy: by})
	}
	return cv
}

// Approves the compaction of id with a coverage that passes
func approveCompaction(ctx context.Context, l *knowledge.Ledger, id, approver string) (knowledge.Compaction, error) {
	c, err := l.Compaction(ctx, id)
	if err != nil {
		return knowledge.Compaction{}, err
	}
	return l.ApproveCompaction(ctx, id, approver, coverage(c))
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
		ID: "k-1", Version: 1, Kind: knowledge.KindMeaning, Content: "lag and basis", Run: seeds.a.Run,
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
	replaced := knowledge.Set{seeds.j, seeds.a, seeds.b}

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

	task := func(value string) *knowledge.RunScope {
		return &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}, "task": {value}}}
	}
	overlapping := seeds.drafts()
	second := overlapping[0]
	second.Evidence.Knowledge = []knowledge.Ref{{ID: "b", Version: 1}}
	overlapping[0].Evidence.Knowledge = []knowledge.Ref{{ID: "a", Version: 1}}
	overlapping = append(overlapping, second)
	partitioned := seeds.drafts()
	second.Run = task("push")
	partitioned[0].Evidence.Knowledge = []knowledge.Ref{{ID: "a", Version: 1}}
	partitioned[0].Run = task("commit")
	partitioned = append(partitioned, second)
	byCommit := meaning
	byCommit.Run = task("commit")
	byCommit.Evidence = knowledge.Evidence{
		FeedbackTraceIDs: []string{"t1"}, ParagraphIDs: []string{"p#3"}, Knowledge: []knowledge.Ref{{ID: "a", Version: 1}},
	}
	byPush := meaning
	byPush.ID, byPush.Basis, byPush.Run = "k-2", knowledge.BasisVerified, task("push")
	byPush.Evidence = knowledge.Evidence{
		FeedbackTraceIDs: []string{"t2", "t1"}, OutcomeTraceIDs: []string{"o1"}, ParagraphIDs: []string{"p#1", "p#3"},
		Knowledge: []knowledge.Ref{{ID: "b", Version: 1}},
	}
	threeWay := judgment
	byCommit.CompactionSize, threeWay.CompactionSize, byPush.CompactionSize = 3, 3, 3

	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"candidates carry the compaction id and evidence built from the old items they name",
			args{"j", seeds.drafts()},
			want{compaction: knowledge.Compaction{ID: "c-1", Items: knowledge.Set{meaning, judgment}, Replaced: replaced}},
		},
		{"an anchor that is not approved is not found", args{"none", seeds.drafts()}, want{err: knowledge.ErrNotFound}},
		{
			"a meaning anchor leaves the vetoed judgment out of its folder so a name of it is refused",
			args{"a", seeds.drafts()},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{"a folder of one item is refused", args{"far", seeds.drafts()}, want{err: knowledge.ErrCompactionInvalid}},
		{"no draft is refused", args{"j", nil}, want{err: knowledge.ErrCompactionInvalid}},
		{"an old item no draft names is refused", args{"j", oneDraft}, want{err: knowledge.ErrCompactionInvalid}},
		{"a draft that names nothing is refused", args{"j", namesNothing}, want{err: knowledge.ErrCompactionInvalid}},
		{"a name outside the folder is refused", args{"j", outsideFolder}, want{err: knowledge.ErrCompactionInvalid}},
		{"a name of an older version is refused", args{"j", olderVersion}, want{err: knowledge.ErrCompactionInvalid}},
		{
			"a draft that takes the id of an item outside the compaction is refused",
			args{"j", outsideID},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{"two drafts of one id are refused", args{"j", repeatedID}, want{err: knowledge.ErrCompactionInvalid}},
		{"an invalid draft is refused with its own error", args{"j", noContent}, want{err: knowledge.ErrContentRequired}},
		{"two meanings one run may carry overlap", args{"j", overlapping}, want{err: knowledge.ErrCompactionOverlap}},
		{
			"two meanings split by a label value with none in common pass",
			args{"j", partitioned},
			want{compaction: knowledge.Compaction{
				ID: "c-1", Items: knowledge.Set{byCommit, threeWay, byPush}, Replaced: replaced,
			}},
		},
		{"dropping the veto of an old judgment is refused", args{"j", noVeto}, want{err: knowledge.ErrCompactionVeto}},
		{
			"a new veto that lets the old example through is refused",
			args{"j", weakVeto},
			want{err: knowledge.ErrCompactionVeto},
		},
		{
			"a new veto that blocks only the old example is refused",
			args{"j", exampleVeto},
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

// A general meaning g and two meanings split by the value of one label
// A run carries g and at most one of the two so each fact must stay on the runs its item reached
// All three except docs work
func TestLedgerProposeCompactionScope(t *testing.T) {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	scope := func(labels trace.Labels) *knowledge.RunScope {
		return &knowledge.RunScope{Producer: "session", Labels: labels, Except: trace.Labels{"task": {"docs"}}}
	}
	base := knowledge.Knowledge{
		Version: 1, Kind: knowledge.KindMeaning, Basis: knowledge.BasisStated,
		Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Author: "author", Time: at,
	}
	g, shop, sports := base, base, base
	g.ID, g.Content, g.Evidence = "g", "general fact", knowledge.Evidence{FeedbackTraceIDs: []string{"t0"}}
	g.Run = scope(trace.Labels{"repo": {"nodloop"}})
	shop.ID, shop.Content, shop.Evidence = "shop", "shop fact", knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}
	shop.Run = scope(trace.Labels{"repo": {"nodloop"}, "topic": {"shopping"}})
	sports.ID, sports.Content, sports.Evidence = "sports", "sports fact", knowledge.Evidence{FeedbackTraceIDs: []string{"t2"}}
	sports.Run = scope(trace.Labels{"repo": {"nodloop"}, "topic": {"sports"}})
	// A draft of the old item that repeats the general fact under the scope of the old item
	draftOf := func(old knowledge.Knowledge, edit func(*knowledge.Knowledge)) knowledge.Knowledge {
		d := knowledge.Knowledge{
			ID: old.ID, Kind: old.Kind, Content: old.Content + " and general fact", Author: "claude",
			Run:      &knowledge.RunScope{Producer: old.Run.Producer, Labels: maps.Clone(old.Run.Labels), Except: maps.Clone(old.Run.Except)},
			Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: old.ID, Version: 1}, {ID: "g", Version: 1}}},
		}
		edit(&d)
		return d
	}
	keep := func(*knowledge.Knowledge) {}
	own := func(d *knowledge.Knowledge) { d.Evidence.Knowledge = d.Evidence.Knowledge[:1] }
	label := func(key string, values ...string) func(*knowledge.Knowledge) {
		return func(d *knowledge.Knowledge) { d.Run.Labels[key] = values }
	}
	merged := func(edit func(*knowledge.Knowledge)) []knowledge.Knowledge {
		return []knowledge.Knowledge{draftOf(shop, func(d *knowledge.Knowledge) {
			d.ID = ""
			d.Evidence.Knowledge = append(d.Evidence.Knowledge, knowledge.Ref{ID: "sports", Version: 1})
			edit(d)
		})}
	}
	unnamed := func(edit func(*knowledge.Knowledge)) func(*knowledge.Knowledge) {
		return func(d *knowledge.Knowledge) {
			d.ID = ""
			edit(d)
		}
	}
	type want struct {
		// Labels of every proposed item
		labels []trace.Labels
		err    error
		// A part of the error text
		message string
	}
	shopping := trace.Labels{"repo": {"nodloop"}, "topic": {"shopping"}}
	sporting := trace.Labels{"repo": {"nodloop"}, "topic": {"sports"}}
	tcs := []struct {
		name string
		args []knowledge.Knowledge
		want want
	}{
		{
			"one draft per label value that repeats the general fact passes",
			[]knowledge.Knowledge{draftOf(shop, keep), draftOf(sports, keep)},
			want{labels: []trace.Labels{shopping, sporting}},
		},
		{
			"a merged draft that drops the label would carry each fact to every topic and is refused",
			merged(func(d *knowledge.Knowledge) { delete(d.Run.Labels, "topic") }),
			want{err: knowledge.ErrCompactionInvalid, message: "carries the facts of shop to runs that shop never reached"},
		},
		{
			"a merged draft that keeps one value would carry the sports fact to shopping runs and is refused",
			merged(keep),
			want{err: knowledge.ErrCompactionInvalid, message: "carries the facts of sports to runs that sports never reached"},
		},
		{
			"a draft that drops the exceptions its items share is refused",
			[]knowledge.Knowledge{draftOf(shop, func(d *knowledge.Knowledge) { d.Run.Except = nil }), draftOf(sports, keep)},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"a draft that adds a label value is refused",
			[]knowledge.Knowledge{draftOf(shop, label("repo", "nodloop", "other")), draftOf(sports, keep)},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"a draft that drops a label key reaches every value and is refused",
			[]knowledge.Knowledge{draftOf(shop, func(d *knowledge.Knowledge) { delete(d.Run.Labels, "repo") }), draftOf(sports, keep)},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"two meanings split by a label value with none in common pass",
			[]knowledge.Knowledge{
				draftOf(shop, label("task", "commit")),
				draftOf(shop, unnamed(label("task", "push"))),
				draftOf(sports, keep),
			},
			want{labels: []trace.Labels{
				{"repo": {"nodloop"}, "task": {"commit"}, "topic": {"shopping"}},
				{"repo": {"nodloop"}, "task": {"push"}, "topic": {"shopping"}},
				sporting,
			}},
		},
		{
			"two meanings whose values share one overlap",
			[]knowledge.Knowledge{draftOf(shop, label("task", "commit")), draftOf(shop, unnamed(keep)), draftOf(sports, keep)},
			want{err: knowledge.ErrCompactionOverlap},
		},
		{
			"two meanings whose labels differ in keys but not in values overlap",
			[]knowledge.Knowledge{draftOf(shop, keep), draftOf(shop, unnamed(label("platform", "ios"))), draftOf(sports, keep)},
			want{err: knowledge.ErrCompactionOverlap},
		},
		{
			"a general draft beside the drafts of each value overlaps them",
			[]knowledge.Knowledge{draftOf(shop, own), draftOf(sports, own), draftOf(g, own)},
			want{err: knowledge.ErrCompactionOverlap},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), at)
			require.NoError(t, testkit.Err(l.Import(ctx, []knowledge.Knowledge{g, shop, sports})))

			got, err := l.ProposeCompaction(ctx, "g", tc.args)
			var labels []trace.Labels
			for _, k := range got.Items {
				labels = append(labels, k.Run.Labels)
			}

			assert.ErrorIs(t, err, tc.want.err)
			if tc.want.message != "" {
				assert.ErrorContains(t, err, tc.want.message)
			}
			assert.Equal(t, tc.want.labels, labels)
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

// A second proposal over items of the first waits until the first is abandoned or can never be approved
// The first is anchored at j and the second at a with the meaning draft alone
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
		// Approving the second proposal with a passing coverage
		approveErr error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"refused while the first is pending", args{}, want{knowledge.ErrCompactionPending, 6, knowledge.ErrNotFound}},
		{
			"accepted once a candidate of the first is retired",
			args{retired: []knowledge.Ref{{ID: "k-1", Version: 1}}},
			want{nil, 8, nil},
		},
		{"a proposal cut between two appends is replaced by the retry", args{cut: 1}, want{nil, 6, nil}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seeds := newCompactionSeeds()
			dir := t.TempDir()
			l, _ := newTestLedger(t, dir, seeds.at.Add(time.Hour))
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			_, err := l.ProposeCompaction(ctx, "j", seeds.drafts())
			require.NoError(t, err)
			for _, ref := range tc.args.retired {
				_, err = l.Retire(ctx, ref.ID, ref.Version, "ann")
				require.NoError(t, err)
			}
			dropNewest(t, dir, tc.args.cut)

			second, err := l.ProposeCompaction(ctx, "a", seeds.drafts()[:1])
			all, listErr := l.All(ctx)
			require.NoError(t, listErr)
			_, approveErr := approveCompaction(ctx, l, second.ID, "jed")

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

			got, err := l.ProposeCompaction(ctx, "j", drafts)

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

			got, err := l.ProposeCompaction(ctx, "j", drafts)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, got.Items, tc.want.items)
			all, err := l.All(ctx)
			require.NoError(t, err)
			assert.Len(t, all, len(seeds.all())+tc.want.items)
		})
	}
}

// Beside the judgment that keeps the veto of j a second judgment names j with a perl veto of its own
// plain is that second judgment without its veto
func TestLedgerProposeCompactionVetoesShareRun(t *testing.T) {
	seeds := newCompactionSeeds()
	perl := knowledge.Knowledge{
		ID: "k", Kind: knowledge.KindJudgment, Content: "never perl -i", Run: seeds.j.Run, Author: "claude",
		Evidence: knowledge.Evidence{Knowledge: []knowledge.Ref{{ID: "j", Version: 1}}},
		Veto: &knowledge.Veto{
			Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: `perl\s+-i`}},
			Example: map[string]any{"command": "perl -i -pe s/a/b/ f"},
		},
	}
	plain := perl
	plain.Veto = nil
	tcs := []struct {
		name string
		args []knowledge.Knowledge
		want error
	}{
		{"two judgments that each carry a veto may share a run", append(seeds.drafts(), perl), nil},
		{"a judgment without a veto may not share a run with a veto judgment", append(seeds.drafts(), plain), knowledge.ErrCompactionOverlap},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, _ := newTestLedger(t, t.TempDir(), seeds.at.Add(time.Hour))
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))

			_, err := l.ProposeCompaction(ctx, "j", tc.args)

			assert.ErrorIs(t, err, tc.want)
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
			proposed, err := ledger.ProposeCompaction(ctx, "j", drafts)
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
			proposed, err := l.ProposeCompaction(ctx, "j", drafts)
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
			_, individualErr := l.Approve(ctx, proposed.Items[0].ID, proposed.Items[0].Version, "jed")
			cv := coverage(proposed)
			_, approveErr := l.ApproveCompaction(ctx, id, "jed", cv)
			after, err := l.All(ctx)
			require.NoError(t, err)
			_, retryErr := l.ApproveCompaction(ctx, id, "jed", cv)
			retried, err := l.All(ctx)
			require.NoError(t, err)
			vetoes, err := os.ReadFile(vetoPath)
			require.NoError(t, err)

			assert.ErrorIs(t, lookupErr, tc.want.err)
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

func TestCoveragePasses(t *testing.T) {
	ref := func(id string, version int) knowledge.Ref { return knowledge.Ref{ID: id, Version: version} }
	c := knowledge.Compaction{
		ID:       "c-1",
		Items:    knowledge.Set{{ID: "k-1", Version: 1}},
		Replaced: knowledge.Set{{ID: "a", Version: 1}, {ID: "b", Version: 1}},
	}
	covered := func(old knowledge.Ref, by ...knowledge.Ref) knowledge.CoverageItem {
		return knowledge.CoverageItem{Old: old, CoveredBy: by}
	}
	tcs := []struct {
		name string
		args knowledge.Coverage
		// A part of the error text and empty when the coverage passes
		want string
	}{
		{
			"every old item stated by a new item passes",
			knowledge.Coverage{Compaction: "c-1", Items: []knowledge.CoverageItem{covered(ref("a", 1), ref("k-1", 1)), covered(ref("b", 1), ref("k-1", 1))}},
			"",
		},
		{
			"the coverage of another compaction fails",
			knowledge.Coverage{Compaction: "c-2", Items: []knowledge.CoverageItem{covered(ref("a", 1), ref("k-1", 1)), covered(ref("b", 1), ref("k-1", 1))}},
			`the coverage is of "c-2" and not of c-1`,
		},
		{
			"an old item left out fails",
			knowledge.Coverage{Compaction: "c-1", Items: []knowledge.CoverageItem{covered(ref("a", 1), ref("k-1", 1))}},
			"no new item states b v1",
		},
		{
			"an old item covered by nothing fails",
			knowledge.Coverage{Compaction: "c-1", Items: []knowledge.CoverageItem{covered(ref("a", 1)), covered(ref("b", 1), ref("k-1", 1))}},
			"no new item states a v1",
		},
		{
			"a lost fact fails",
			knowledge.Coverage{Compaction: "c-1", Items: []knowledge.CoverageItem{
				{Old: ref("a", 1), CoveredBy: []knowledge.Ref{ref("k-1", 1)}, Lost: []string{"the four hour lag"}},
				covered(ref("b", 1), ref("k-1", 1)),
			}},
			"a v1 loses the four hour lag",
		},
		{
			"a cover that is no new item of the compaction fails",
			knowledge.Coverage{Compaction: "c-1", Items: []knowledge.CoverageItem{covered(ref("a", 1), ref("k-2", 1)), covered(ref("b", 1), ref("k-1", 1))}},
			"k-2 v1 is not a new item of c-1",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.args.Passes(c)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, knowledge.ErrCoverageNotPassed)
			assert.ErrorContains(t, err, tc.want)
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
		// The coverage handed to the approval built from the proposal
		coverage func(knowledge.Compaction) knowledge.Coverage
		approver string
		// Approved items of the folder that the meaning draft names beside a and b
		extra []knowledge.Knowledge
		// The drafts of seeds when nil
		drafts []knowledge.Knowledge
	}
	type want struct {
		// id version status and compaction of every record the approval appended in order
		appended []string
		// id version status of every item the approval returned
		items  []string
		vetoes bool
		err    error
	}
	passed := coverage
	edited := func(edit func(*knowledge.Coverage)) func(knowledge.Compaction) knowledge.Coverage {
		return func(c knowledge.Compaction) knowledge.Coverage {
			cv := coverage(c)
			edit(&cv)
			return cv
		}
	}
	none := func(knowledge.Compaction) knowledge.Coverage { return knowledge.Coverage{} }
	lost := edited(func(cv *knowledge.Coverage) { cv.Items[1].Lost = []string{"the lag"} })
	uncovered := edited(func(cv *knowledge.Coverage) { cv.Items = cv.Items[:2] })
	other := edited(func(cv *knowledge.Coverage) { cv.Compaction = "c-other" })
	large := seeds.a
	large.ID, large.Content = "large", strings.Repeat("가", knowledge.ReviewChars)
	large.Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{"t-large"}}
	longer := seeds.drafts()
	longer[0].Content = strings.Repeat("가", knowledge.ReviewChars)
	// Nine items push the review of the folder past the item cap before the compaction
	var crowd []knowledge.Knowledge
	for i := range 9 {
		k := seeds.a
		k.ID, k.Content = fmt.Sprintf("crowd-%d", i), fmt.Sprintf("fact %d", i)
		k.Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{k.ID}}
		crowd = append(crowd, k)
	}
	success := []string{
		"k-1 v1 approved c-1", "j v2 approved c-1", "j v1 superseded c-1", "a v1 retired c-1", "b v1 retired c-1",
	}
	retired := func(extra []knowledge.Knowledge) []string {
		out := slices.Clone(success)
		for _, k := range extra {
			out = append(out, k.ID+" v1 retired c-1")
		}
		return out
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
			args{coverage: passed, approver: "jed"},
			want{appended: success, items: items, vetoes: true},
		},
		{
			"a second call after a partial append appends only what is missing",
			args{approved: candidate, coverage: passed, approver: "jed"},
			want{appended: success[1:], items: items, vetoes: true},
		},
		{"refused without a coverage", args{coverage: none, approver: "jed"}, want{err: knowledge.ErrCoverageNotPassed}},
		{"refused when a fact is lost", args{coverage: lost, approver: "jed"}, want{err: knowledge.ErrCoverageNotPassed}},
		{"refused when an old item is not covered", args{coverage: uncovered, approver: "jed"}, want{err: knowledge.ErrCoverageNotPassed}},
		{"refused with the coverage of another compaction", args{coverage: other, approver: "jed"}, want{err: knowledge.ErrCoverageNotPassed}},
		{
			"refused when an old item changed since the proposal",
			args{retired: []knowledge.Ref{{ID: "b", Version: 1}}, coverage: passed, approver: "jed"},
			want{err: knowledge.ErrCompactionOutdated},
		},
		{
			"refused when a candidate of the compaction was retired",
			args{retired: candidate, coverage: passed, approver: "jed"},
			want{err: knowledge.ErrTransitionInvalid},
		},
		{
			"refused when a new item pushes a review past the char cap",
			args{coverage: passed, approver: "jed", drafts: longer},
			want{err: knowledge.ErrFolderFull},
		},
		{
			"a compaction that shrinks a review already past the char cap passes",
			args{coverage: passed, approver: "jed", extra: []knowledge.Knowledge{large}},
			want{appended: retired([]knowledge.Knowledge{large}), items: items, vetoes: true},
		},
		{
			"a compaction that shrinks a review already past the item cap passes",
			args{coverage: passed, approver: "jed", extra: crowd},
			want{appended: retired(crowd), items: items, vetoes: true},
		},
		{"refused without an approver", args{coverage: passed}, want{err: knowledge.ErrApproverRequired}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, vetoPath := newTestLedger(t, t.TempDir(), now)
			require.NoError(t, testkit.Err(l.Import(ctx, append(seeds.all(), tc.args.extra...))))
			drafts := slices.Clone(tc.args.drafts)
			if drafts == nil {
				drafts = seeds.drafts()
			}
			for _, k := range tc.args.extra {
				drafts[0].Evidence.Knowledge = append(slices.Clone(drafts[0].Evidence.Knowledge), knowledge.Ref{ID: k.ID, Version: k.Version})
			}
			proposed, err := l.ProposeCompaction(ctx, "j", drafts)
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

			got, err := l.ApproveCompaction(ctx, proposed.ID, tc.args.approver, tc.args.coverage(proposed))
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

// A narrowed version proposed while a compaction of its id waited for approval
// Approving it after the compaction would drop what the compaction merged so it is refused in either order
func TestLedgerNarrowedDuringCompaction(t *testing.T) {
	seeds := newCompactionSeeds()
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
			},
			)
			require.NoError(t, testkit.Err(l.Import(ctx, seeds.all())))
			_, err = l.ProposeCompaction(ctx, "j", seeds.drafts())
			require.NoError(t, err)
			narrowed, _, err := l.Narrow(ctx, "j", 1, "task", []string{"push"}, []string{"r9"}, "jed")
			require.NoError(t, err)
			require.Equal(t, 3, narrowed.Version)

			approvals := map[string]func() error{
				"narrowed":   func() error { return testkit.Err(l.Approve(ctx, "j", 3, "jed")) },
				"compaction": func() error { return testkit.Err(approveCompaction(ctx, l, "c-1", "jed")) },
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
	reword := func(id string) knowledge.Knowledge {
		return knowledge.Knowledge{
			ID: id, Kind: knowledge.KindMeaning, Content: id + " reworded", Run: seeds.b.Run,
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
			return testkit.Err(l.ProposeCompaction(ctx, "j", seeds.drafts()))
		},
		"approve compaction": func(ctx context.Context, l *knowledge.Ledger) error {
			return testkit.Err(approveCompaction(ctx, l, "c-1", "jed"))
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

// A folder past the item cap compacts into drafts that one run never carries two of a kind
// and the folder then takes approvals again
func TestLedgerProposeCompactionFreesFolder(t *testing.T) {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	repo := trace.Labels{"repo": {"nodloop"}}
	// Twelve approved meanings of the repo that a ledger approved before the item cap left
	var seeds []knowledge.Knowledge
	for i := range 12 {
		seeds = append(seeds, knowledge.Knowledge{
			ID: fmt.Sprintf("k%02d", i), Version: 1, Kind: knowledge.KindMeaning, Content: fmt.Sprintf("fact %d", i),
			Run:      &knowledge.RunScope{Producer: "session", Labels: repo},
			Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{fmt.Sprintf("t%d", i)}}, Basis: knowledge.BasisStated,
			Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Author: "author", Time: at,
		})
	}
	// One draft over the seeds from first up to last scoped to the labels
	draft := func(first, last int, labels trace.Labels) knowledge.Knowledge {
		d := knowledge.Knowledge{
			Kind: knowledge.KindMeaning, Content: fmt.Sprintf("facts %d to %d", first, last), Author: "claude",
			Run: &knowledge.RunScope{Producer: "session", Labels: labels},
		}
		for _, k := range seeds[first:last] {
			d.Evidence.Knowledge = append(d.Evidence.Knowledge, knowledge.Ref{ID: k.ID, Version: 1})
		}
		return d
	}
	byTask := func(value string) trace.Labels { return trace.Labels{"repo": {"nodloop"}, "task": {value}} }
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
		{"one merged draft passes and the folder takes the next approval", []knowledge.Knowledge{draft(0, 12, repo)}, want{nil, 1, nil}},
		{
			"drafts split by a label value with none in common pass and the folder takes the next approval",
			[]knowledge.Knowledge{draft(0, 6, byTask("commit")), draft(6, 12, byTask("push"))},
			want{nil, 2, nil},
		},
		{
			"a merged draft without labels reaches runs each old item never reached and is refused",
			[]knowledge.Knowledge{draft(0, 12, nil)},
			want{knowledge.ErrCompactionInvalid, 0, knowledge.ErrFolderFull},
		},
		{
			"two drafts of the same labels overlap and are refused",
			[]knowledge.Knowledge{draft(0, 6, repo), draft(6, 12, repo)},
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
				require.NoError(t, testkit.Err(l.ApproveCompaction(ctx, got.ID, "jed", coverage(got))))
			}
			next := knowledge.Knowledge{
				ID: "k-next", Kind: knowledge.KindMeaning, Content: "next fact", Author: "author",
				Run:      &knowledge.RunScope{Producer: "session", Labels: repo},
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
