package knowledge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestLedgerApproved(t *testing.T) {
	at := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "jed", at
	other := candidate
	other.ID = "k2"
	type args struct {
		id      string
		version int
	}
	type want struct {
		knowledge knowledge.Knowledge
		err       error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"approved version is returned", args{"k1", 1}, want{knowledge: approved}},
		{"candidate is not approved", args{"k2", 1}, want{err: knowledge.ErrVersionUnapproved}},
		{"unknown version is not found", args{"k1", 2}, want{err: knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	l := knowledge.NewLedger(
		store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
		func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
	)
	require.NoError(t, testkit.Err(l.Import(ctx, []knowledge.Knowledge{candidate, approved, other})))
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := l.Approved(ctx, tc.args.id, tc.args.version)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.knowledge, got)
		})
	}
}

func TestLedgerApprovedVersion(t *testing.T) {
	at := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	first := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusApproved, Approver: "jed", ApprovedAt: at, Author: "author", Time: at,
	}
	second := first
	second.Version = 2
	candidate := first
	candidate.Version, candidate.Status = 3, knowledge.StatusCandidate
	other := candidate
	other.ID, other.Version = "k2", 1
	type want struct {
		version int
		err     error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"the approved version wins over older approvals and a newer candidate", "k1", want{version: 2}},
		{"an id without an approved version fails", "k2", want{err: knowledge.ErrVersionUnapproved}},
		{"an unknown id is not found", "nope", want{err: knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	l := knowledge.NewLedger(
		store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
		func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
	)
	require.NoError(t, testkit.Err(l.Import(ctx, []knowledge.Knowledge{first, second, candidate, other})))
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := l.ApprovedVersion(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.version, got)
		})
	}
}

func TestLedgerHistory(t *testing.T) {
	at := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "jed", at
	other := candidate
	other.ID = "k2"
	type want struct {
		history knowledge.Set
		err     error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"records of the id come newest first", "k1", want{history: knowledge.Set{approved, candidate}}},
		{"id without a record is not found", "k3", want{err: knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	l := knowledge.NewLedger(
		store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
		func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
	)
	require.NoError(t, testkit.Err(l.Import(ctx, []knowledge.Knowledge{candidate, other, approved})))
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := l.History(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.history, got)
		})
	}
}

func TestLedgerOverlaps(t *testing.T) {
	at := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	k1 := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	k2 := k1
	k2.ID = "k2"
	retired := k1
	retired.ID, retired.Status, retired.Approver = "k3", knowledge.StatusRetired, "jed"
	type want struct {
		overlaps knowledge.Set
		err      error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"current items of the same kind and scope are listed", "k1", want{overlaps: knowledge.Set{k2}}},
		{"id without a current record is not found", "k3", want{err: knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	l := knowledge.NewLedger(
		store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
		func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
	)
	require.NoError(t, testkit.Err(l.Import(ctx, []knowledge.Knowledge{k1, k2, retired})))
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := l.Overlaps(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.overlaps, got)
		})
	}
}

// A data set that declares its own change contexts scopes and measures knowledge by them
func TestLedgerDeclaredContexts(t *testing.T) {
	at := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	declared := evidence.Contexts{{Name: "deploy", BreaksBaseline: true}, {Name: "campaign_start"}, {Name: evidence.ContextUnknown}}
	scoped := func(contexts, exceptions []evidence.Context) knowledge.Knowledge {
		return knowledge.Knowledge{
			ID: "k1", Kind: knowledge.KindMeaning, Content: "a deploy resets the counters",
			Scope:      knowledge.Scope{Scope: evidence.Scope{ChangeContexts: contexts}},
			Exceptions: exceptions, Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Author: "author",
		}
	}
	type want struct {
		err  error
		text string
		// The change context of the folder the candidate would join
		folder evidence.Context
	}
	tcs := []struct {
		name string
		args knowledge.Knowledge
		want want
	}{
		{"a declared context scopes the item", scoped([]evidence.Context{"campaign_start"}, nil), want{folder: "campaign_start"}},
		{"an unscoped item is measured in the first declared context", scoped(nil, nil), want{folder: "deploy"}},
		{
			"a default context the data set does not declare is refused naming the declared ones",
			scoped([]evidence.Context{evidence.ContextNoKnownChange}, nil),
			want{err: knowledge.ErrScopeInvalid, text: `"no_known_change" is not one of [deploy campaign_start unknown]`},
		},
		{
			"exceptions of every declared context leave an unscoped item nothing",
			scoped(nil, []evidence.Context{"deploy", "campaign_start", evidence.ContextUnknown}),
			want{err: knowledge.ErrScopeInvalid, text: "cover every change context"},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(
				store, vetofile.NewApprovedFile(t.TempDir(), "records"), declared,
				func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
			)

			k, _, err := l.Propose(ctx, tc.args)

			if tc.want.err != nil {
				assert.ErrorIs(t, err, tc.want.err)
				assert.ErrorContains(t, err, tc.want.text)
				return
			}
			require.NoError(t, err)
			f, err := l.Folder(ctx, k.ID, k.Version)
			require.NoError(t, err)
			assert.Equal(t, tc.want.folder, f.Context)
		})
	}
}

func TestLedgerPropose(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	draft := knowledge.Knowledge{
		ID: "k1", Kind: knowledge.KindMeaning, Content: "clicks and conversions use different time bases",
		Scope:    knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}},
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Author: "author",
	}
	proposed := draft
	proposed.Version, proposed.Status, proposed.Basis = 1, knowledge.StatusCandidate, knowledge.BasisStated
	proposed.Time = now
	filled := draft
	filled.Version, filled.Status, filled.Base = 7, knowledge.StatusApproved, 6
	signed := draft
	signed.Approver, signed.ApprovedAt, signed.Supersedes = "jed", at, 2
	verified := draft
	verified.Basis = knowledge.BasisVerified
	proposedVerified := proposed
	proposedVerified.Basis = knowledge.BasisVerified
	unnamed := draft
	unnamed.ID = ""
	generated := proposed
	generated.ID = "k-new"
	v1 := proposed
	v1.Time = at
	v3 := v1
	v3.Version = 3
	proposedV4 := proposed
	proposedV4.Version, proposedV4.Base = 4, 3
	// v1 is approved and v2 a candidate so the next version is built from v1
	v1Approved := v1
	v1Approved.Status, v1Approved.Approver, v1Approved.ApprovedAt = knowledge.StatusApproved, "ann", at
	v2 := v1
	v2.Version = 2
	proposedV3 := proposed
	proposedV3.Version, proposedV3.Base = 3, 1
	neighbour := v1
	neighbour.ID = "k2"
	empty := draft
	empty.Content = ""
	drafted := draft
	drafted.Drafted = true
	proposedDrafted := proposed
	proposedDrafted.Drafted = true
	none, just := knowledge.Set{}, knowledge.Set{proposed}
	type args struct {
		seeds []knowledge.Knowledge
		draft knowledge.Knowledge
	}
	type want struct {
		knowledge knowledge.Knowledge
		overlaps  knowledge.Set
		all       knowledge.Set
		err       error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"new id becomes the first stated candidate", args{draft: draft}, want{proposed, none, just, nil}},
		{
			"draft without an id takes a generated one",
			args{draft: unnamed},
			want{generated, none, knowledge.Set{generated}, nil},
		},
		{"version and status of the draft are replaced", args{draft: filled}, want{proposed, none, just, nil}},
		{"approval fields of the draft are dropped", args{draft: signed}, want{proposed, none, just, nil}},
		{
			"verified basis is kept",
			args{draft: verified},
			want{proposedVerified, none, knowledge.Set{proposedVerified}, nil},
		},
		{
			"next version follows the highest version of the id",
			args{[]knowledge.Knowledge{v1, v3}, draft},
			want{proposedV4, none, knowledge.Set{proposedV4, v3, v1}, nil},
		},
		{
			"the base is the approved version even beside a newer candidate",
			args{[]knowledge.Knowledge{v1, v1Approved, v2}, draft},
			want{proposedV3, none, knowledge.Set{proposedV3, v2, v1Approved, v1}, nil},
		},
		{
			"current item with an intersecting scope is listed",
			args{[]knowledge.Knowledge{neighbour}, draft},
			want{proposed, knowledge.Set{neighbour}, knowledge.Set{proposed, neighbour}, nil},
		},
		{"invalid draft is not appended", args{draft: empty}, want{err: knowledge.ErrContentRequired}},
		{
			"content a model drafted stays marked",
			args{draft: drafted},
			want{proposedDrafted, none, knowledge.Set{proposedDrafted}, nil},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(
				store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
				func() time.Time { return now }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, testkit.Err(l.Import(ctx, tc.args.seeds)))
			got, overlaps, err := l.Propose(ctx, tc.args.draft)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.knowledge, got)
			assert.Equal(t, tc.want.overlaps, overlaps)
			all, err := l.All(ctx)
			require.NoError(t, err)
			assert.Equal(t, tc.want.all, all)
		})
	}
}

func TestLedgerApprove(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "ann", at
	retired := candidate
	retired.Status, retired.Approver = knowledge.StatusRetired, "ann"
	superseded := approved
	superseded.Status = knowledge.StatusSuperseded
	second := candidate
	second.Version = 2
	approvedNow := candidate
	approvedNow.Status, approvedNow.Approver, approvedNow.ApprovedAt = knowledge.StatusApproved, "jed", now
	approvedNow.Time = now
	secondApproved := second
	secondApproved.Status, secondApproved.Approver, secondApproved.ApprovedAt = knowledge.StatusApproved, "jed", now
	secondApproved.Time, secondApproved.Supersedes = now, 1
	supersededNow := approved
	supersededNow.Status, supersededNow.Approver, supersededNow.Time = knowledge.StatusSuperseded, "jed", now
	secondApprovedEarlier := second
	secondApprovedEarlier.Status, secondApprovedEarlier.Approver = knowledge.StatusApproved, "ann"
	secondApprovedEarlier.ApprovedAt = at
	judgment := candidate
	judgment.Kind, judgment.Content = knowledge.KindJudgment, "never edit files with sed -i"
	judgment.Veto = &knowledge.Veto{
		Tool:    "Bash",
		When:    []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`}},
		Example: map[string]any{"command": "sed -i s/a/b/ f"},
	}
	judgmentApproved := judgment
	judgmentApproved.Status, judgmentApproved.Approver, judgmentApproved.ApprovedAt = knowledge.StatusApproved, "jed", now
	judgmentApproved.Time = now
	drafted := candidate
	drafted.Drafted = true
	draftedApproved := approvedNow
	draftedApproved.Drafted = true
	compacted := candidate
	compacted.Compaction, compacted.CompactionSize = "c-1", 1
	compactedApproved := compacted
	compactedApproved.Status, compactedApproved.Approver, compactedApproved.ApprovedAt = knowledge.StatusApproved, "ann", at
	// Its text alone nearly fills ReviewChars in runes so any other item of its folder overflows it
	large := approved
	large.ID, large.Content = "k-large", strings.Repeat("가", knowledge.ReviewChars-50)
	largeClicks := large
	largeClicks.Scope = knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"click_count"}}}
	conversions := candidate
	conversions.Scope = knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}}
	// v2 and v3 were proposed from v1 and v2 was approved after them
	proposedAt, v2ApprovedAt := at.Add(time.Minute), at.Add(2*time.Minute)
	v2 := candidate
	v2.Version, v2.Base, v2.Time = 2, 1, proposedAt
	v3 := v2
	v3.Version = 3
	v2Approved := v2
	v2Approved.Status, v2Approved.Approver, v2Approved.ApprovedAt, v2Approved.Time = knowledge.StatusApproved, "ann",
		v2ApprovedAt, v2ApprovedAt
	v2Approved.Supersedes = 1
	v1Superseded := approved
	v1Superseded.Status, v1Superseded.Time = knowledge.StatusSuperseded, v2ApprovedAt
	// v1 was reaffirmed after v2 was proposed and a reaffirm is no new approval
	reaffirmed := approved
	reaffirmed.ReviewedAt, reaffirmed.Time = v2ApprovedAt, v2ApprovedAt
	v2Now := v2
	v2Now.Status, v2Now.Approver, v2Now.ApprovedAt, v2Now.Time, v2Now.Supersedes = knowledge.StatusApproved, "jed", now, now, 1
	reaffirmedSuperseded := reaffirmed
	reaffirmedSuperseded.Status, reaffirmedSuperseded.Approver, reaffirmedSuperseded.Time = knowledge.StatusSuperseded, "jed", now
	// v2 was proposed from the candidate v1 before v1 was approved
	v1ApprovedLater := approved
	v1ApprovedLater.ApprovedAt, v1ApprovedLater.Time = v2ApprovedAt, v2ApprovedAt
	stacked := candidate
	stacked.Version, stacked.Base = 2, 1
	stackedNow := stacked
	stackedNow.Status, stackedNow.Approver, stackedNow.ApprovedAt, stackedNow.Time = knowledge.StatusApproved, "jed", now, now
	stackedNow.Supersedes = 1
	v1LaterSuperseded := v1ApprovedLater
	v1LaterSuperseded.Status, v1LaterSuperseded.Approver, v1LaterSuperseded.Time = knowledge.StatusSuperseded, "jed", now
	vetoApproved := judgment
	vetoApproved.Status, vetoApproved.Approver, vetoApproved.ApprovedAt = knowledge.StatusApproved, "ann", at
	noVeto := judgment
	noVeto.Version, noVeto.Veto, noVeto.Time = 2, nil, proposedAt
	weaker := noVeto
	weaker.Veto = &knowledge.Veto{
		Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i\s+--`}},
		Example: map[string]any{"command": "sed -i -- s/a/b/ f"},
	}
	vetoRetired := vetoApproved
	vetoRetired.Status, vetoRetired.Time = knowledge.StatusRetired, proposedAt
	annVeto := "# Generated by nodloop from the approved judgment knowledge of records\n" +
		"# Edits here are overwritten. Retire the knowledge to remove a veto\n" +
		"vetoes:\n    - id: k1\n      tool: Bash\n      when:\n        - field: command\n" +
		"          match: sed\\s+-i\n      reason: never edit files with sed -i\n" +
		"      source: nodloop knowledge k1 v1 approved by ann\n"
	noVetoNow := noVeto
	noVetoNow.Status, noVetoNow.Approver, noVetoNow.ApprovedAt, noVetoNow.Time = knowledge.StatusApproved, "jed", now, now
	type args struct {
		seeds    []knowledge.Knowledge
		version  int
		approver string
	}
	type want struct {
		knowledge knowledge.Knowledge
		history   knowledge.Set
		// The generated veto file and empty when there is none
		vetoes string
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"candidate is approved under the approver's name",
			args{[]knowledge.Knowledge{candidate}, 1, "jed"},
			want{approvedNow, knowledge.Set{approvedNow, candidate}, "", nil},
		},
		{
			"an approved draft keeps its mark",
			args{[]knowledge.Knowledge{drafted}, 1, "jed"},
			want{draftedApproved, knowledge.Set{draftedApproved, drafted}, "", nil},
		},
		{
			"approving a judgment with a veto exports it for guard",
			args{[]knowledge.Knowledge{judgment}, 1, "jed"},
			want{judgmentApproved, knowledge.Set{judgmentApproved, judgment},
				"# Generated by nodloop from the approved judgment knowledge of records\n" +
					"# Edits here are overwritten. Retire the knowledge to remove a veto\n" +
					"vetoes:\n" +
					"    - id: k1\n" +
					"      tool: Bash\n" +
					"      when:\n" +
					"        - field: command\n" +
					"          match: sed\\s+-i\n" +
					"      reason: never edit files with sed -i\n" +
					"      source: nodloop knowledge k1 v1 approved by jed\n", nil},
		},
		{
			"an item whose folder may outgrow the review is refused and nothing is appended",
			args{[]knowledge.Knowledge{large, candidate}, 1, "jed"},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrFolderFull},
		},
		{
			"an item of another metric still fills the folder because one event may move both",
			args{[]knowledge.Knowledge{largeClicks, conversions}, 1, "jed"},
			want{history: knowledge.Set{conversions}, err: knowledge.ErrFolderFull},
		},
		{
			"a candidate built from a version the approved one replaced is refused",
			args{[]knowledge.Knowledge{candidate, approved, v2, v3, v2Approved, v1Superseded}, 3, "jed"},
			want{
				history: knowledge.Set{v1Superseded, v2Approved, v3, v2, approved, candidate},
				err:     knowledge.ErrCandidateOutdated,
			},
		},
		{
			"a candidate built on a candidate approved after it replaces that version",
			args{[]knowledge.Knowledge{candidate, stacked, v1ApprovedLater}, 2, "jed"},
			want{stackedNow, knowledge.Set{v1LaterSuperseded, stackedNow, v1ApprovedLater, stacked, candidate}, "", nil},
		},
		{
			"a reaffirm after the candidate was proposed never makes it outdated",
			args{[]knowledge.Knowledge{candidate, approved, v2, reaffirmed}, 2, "jed"},
			want{v2Now, knowledge.Set{reaffirmedSuperseded, v2Now, reaffirmed, v2, approved, candidate}, "", nil},
		},
		{
			"a new version that drops the veto is refused",
			args{[]knowledge.Knowledge{judgment, vetoApproved, noVeto}, 2, "jed"},
			want{history: knowledge.Set{noVeto, vetoApproved, judgment}, vetoes: annVeto, err: knowledge.ErrVetoLifted},
		},
		{
			"a new version whose veto lets the old example through is refused",
			args{[]knowledge.Knowledge{judgment, vetoApproved, weaker}, 2, "jed"},
			want{history: knowledge.Set{weaker, vetoApproved, judgment}, vetoes: annVeto, err: knowledge.ErrVetoLifted},
		},
		{
			"a new version without the veto approves once a person retired the vetoed one",
			args{[]knowledge.Knowledge{judgment, vetoApproved, noVeto, vetoRetired}, 2, "jed"},
			want{noVetoNow, knowledge.Set{noVetoNow, vetoRetired, noVeto, vetoApproved, judgment}, "", nil},
		},
		{
			"approving a second version appends it before superseding the first",
			args{[]knowledge.Knowledge{candidate, approved, second}, 2, "jed"},
			want{secondApproved, knowledge.Set{supersededNow, secondApproved, second, approved, candidate}, "", nil},
		},
		{
			"older candidate cannot replace a newer approved version",
			args{[]knowledge.Knowledge{candidate, second, secondApprovedEarlier}, 1, "jed"},
			want{history: knowledge.Set{secondApprovedEarlier, second, candidate}, err: knowledge.ErrTransitionInvalid},
		},
		{
			"missing approver fails",
			args{[]knowledge.Knowledge{candidate}, 1, ""},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrApproverRequired},
		},
		{
			"unknown version is not found",
			args{[]knowledge.Knowledge{candidate}, 9, "jed"},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrNotFound},
		},
		{
			"unknown version without an approver is not found",
			args{[]knowledge.Knowledge{candidate}, 9, ""},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrNotFound},
		},
		{
			"approved version cannot be approved again",
			args{[]knowledge.Knowledge{candidate, approved}, 1, "jed"},
			want{history: knowledge.Set{approved, candidate}, err: knowledge.ErrTransitionInvalid},
		},
		{
			"retired version cannot be approved",
			args{[]knowledge.Knowledge{candidate, retired}, 1, "jed"},
			want{history: knowledge.Set{retired, candidate}, err: knowledge.ErrTransitionInvalid},
		},
		{
			"superseded version cannot be approved",
			args{[]knowledge.Knowledge{candidate, approved, superseded}, 1, "jed"},
			want{history: knowledge.Set{superseded, approved, candidate}, err: knowledge.ErrTransitionInvalid},
		},
		{
			"an approved item of a finished compaction cannot be approved again",
			args{[]knowledge.Knowledge{compacted, compactedApproved}, 1, "jed"},
			want{history: knowledge.Set{compactedApproved, compacted}, err: knowledge.ErrTransitionInvalid},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			home := t.TempDir()
			l := knowledge.NewLedger(
				store, vetofile.NewApprovedFile(home, "records"), evidence.DefaultContexts(),
				func() time.Time { return now }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, testkit.Err(l.Import(ctx, tc.args.seeds)))
			got, err := l.Approve(ctx, "k1", tc.args.version, tc.args.approver)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.knowledge, got)
			history, err := l.History(ctx, "k1")
			require.NoError(t, err)
			assert.Equal(t, tc.want.history, history)
			// A missing file reads as empty
			vetoes, _ := os.ReadFile(vetofile.NewApprovedFile(home, "records").Path())
			assert.Equal(t, tc.want.vetoes, string(vetoes))
		})
	}
}

// Proposals and approvals of one id in the order a person runs them with a clock that moves on every call
func TestLedgerApproveInProposalOrder(t *testing.T) {
	draft := knowledge.Knowledge{
		ID: "k1", Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Author: "author",
	}
	type want struct {
		// The error of the last step
		err error
		// The approved version in the end
		approved int
	}
	tcs := []struct {
		name string
		// propose or the version to approve
		args []string
		want want
	}{
		{"a version proposed on a candidate approves after that candidate", []string{"propose", "propose", "1", "2"}, want{nil, 2}},
		{"a version proposed after the approval replaces it", []string{"propose", "1", "propose", "2"}, want{nil, 2}},
		{"a version proposed on a candidate of a candidate reaches the approved one", []string{"propose", "propose", "propose", "1", "3"}, want{nil, 3}},
		{"two versions proposed from one approved version never both land", []string{"propose", "1", "propose", "propose", "2", "3"}, want{knowledge.ErrCandidateOutdated, 2}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
			l := knowledge.NewLedger(store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(), func() time.Time {
				now = now.Add(time.Minute)
				return now
			}, func(prefix string) string { return prefix + "new" },
			)
			var last error
			for i, step := range tc.args {
				require.NoError(t, last, "step %d", i)
				if step == "propose" {
					_, _, last = l.Propose(ctx, draft)
					continue
				}
				version, err := strconv.Atoi(step)
				require.NoError(t, err)
				_, last = l.Approve(ctx, "k1", version, "jed")
			}

			assert.ErrorIs(t, last, tc.want.err)
			approved, err := l.ApprovedVersion(ctx, "k1")
			require.NoError(t, err)
			assert.Equal(t, tc.want.approved, approved)
		})
	}
}

// A new version of an approved item proposed after that approval and approved with or without a retire between
func TestLedgerApproveScopeWidened(t *testing.T) {
	quiet, planned := evidence.ContextNoKnownChange, evidence.ContextPlannedChange
	type reach struct {
		contexts   []evidence.Context
		metrics    []string
		dims       map[string]string
		exceptions []evidence.Context
	}
	clicksAndConversions := reach{contexts: []evidence.Context{quiet}, metrics: []string{"click_count", "conversion_count"}}
	onA := map[string]string{"source": "source-a"}
	type args struct {
		v1, v2 reach
		// v1 is retired by a person before v2 is approved
		retired bool
	}
	type want struct {
		err error
		// The approved version in the end
		approved int
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a reword that drops the whole scope is refused and v1 stays approved",
			args{v1: clicksAndConversions, v2: reach{}},
			want{knowledge.ErrScopeWidened, 1},
		},
		{
			"a new version that drops the metrics only is refused",
			args{v1: clicksAndConversions, v2: reach{contexts: []evidence.Context{quiet}}},
			want{knowledge.ErrScopeWidened, 1},
		},
		{
			"a new version that moves to another change context is refused",
			args{v1: clicksAndConversions, v2: reach{contexts: []evidence.Context{planned}, metrics: clicksAndConversions.metrics}},
			want{knowledge.ErrScopeWidened, 1},
		},
		{
			"a new version that drops an exception is refused",
			args{v1: reach{exceptions: []evidence.Context{planned}}, v2: reach{}},
			want{knowledge.ErrScopeWidened, 1},
		},
		{
			"a new version that changes a dim value is refused",
			args{v1: reach{dims: onA}, v2: reach{dims: map[string]string{"source": "source-b"}}},
			want{knowledge.ErrScopeWidened, 1},
		},
		{
			"a new version that restates the scope is approved",
			args{v1: clicksAndConversions, v2: clicksAndConversions},
			want{nil, 2},
		},
		{
			"a new version that narrows its metrics and adds a dim is approved",
			args{v1: clicksAndConversions, v2: reach{contexts: []evidence.Context{quiet}, metrics: []string{"click_count"}, dims: onA}},
			want{nil, 2},
		},
		{
			"a new version that narrows its change contexts is approved",
			args{v1: reach{contexts: []evidence.Context{quiet, planned}}, v2: reach{contexts: []evidence.Context{planned}}},
			want{nil, 2},
		},
		{
			"a new version without a scope is approved once a person retired v1",
			args{v1: clicksAndConversions, v2: reach{}, retired: true},
			want{nil, 2},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
			l := knowledge.NewLedger(store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(), func() time.Time {
				now = now.Add(time.Minute)
				return now
			}, func(prefix string) string { return prefix + "new" },
			)
			draft := func(r reach) knowledge.Knowledge {
				return knowledge.Knowledge{
					ID: "k1", Kind: knowledge.KindMeaning, Content: "one",
					Scope:      knowledge.Scope{Scope: evidence.Scope{ChangeContexts: r.contexts, Metrics: r.metrics}, Dims: r.dims},
					Exceptions: r.exceptions, Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Author: "author",
				}
			}
			_, _, err = l.Propose(ctx, draft(tc.args.v1))
			require.NoError(t, err)
			require.NoError(t, testkit.Err(l.Approve(ctx, "k1", 1, "ann")))
			_, _, err = l.Propose(ctx, draft(tc.args.v2))
			require.NoError(t, err)
			if tc.args.retired {
				require.NoError(t, testkit.Err(l.Retire(ctx, "k1", 1, "ann")))
			}

			_, err = l.Approve(ctx, "k1", 2, "jed")

			assert.ErrorIs(t, err, tc.want.err)
			approved, err := l.ApprovedVersion(ctx, "k1")
			require.NoError(t, err)
			assert.Equal(t, tc.want.approved, approved)
		})
	}
}

// Items that cite only a paragraph never make a compaction due so the item cap is the only bound on their folder
// An event carries one change context so the cap counts the items of one change context and never the union
func TestLedgerApproveItemCap(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	item := func(id string, status knowledge.Status, contexts []evidence.Context, vetoed bool) knowledge.Knowledge {
		k := knowledge.Knowledge{
			ID: id, Version: 1, Kind: knowledge.KindMeaning, Content: "one",
			Scope:    knowledge.Scope{Scope: evidence.Scope{ChangeContexts: contexts}},
			Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
			Status: status, Author: "author", Time: now,
		}
		if vetoed {
			k.Kind, k.Veto = knowledge.KindJudgment, &knowledge.Veto{
				Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: "^" + id + `\b`}},
				Example: map[string]any{"command": id + " now"},
			}
		}
		if status == knowledge.StatusApproved {
			k.Approver, k.ApprovedAt = "ann", now
		}
		return k
	}
	// Approved items already in the ledger
	type group struct {
		prefix   string
		count    int
		contexts []evidence.Context
		vetoed   bool
	}
	type args struct {
		approved []group
		// The scope and exceptions of the candidate
		contexts   []evidence.Context
		exceptions []evidence.Context
		vetoed     bool
	}
	type want struct {
		status knowledge.Status
		// The error text naming both caps and every carried item and empty on success
		err string
	}
	quiet := []evidence.Context{evidence.ContextNoKnownChange}
	planned := []evidence.Context{evidence.ContextPlannedChange}
	measured := []evidence.Context{evidence.ContextMeasurementChanged}
	split := []group{{"k-q", 5, quiet, false}, {"k-p", 5, planned, false}}
	vetoes := []group{{"k-v", 10, nil, true}}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the item that fills the review list is approved", args{approved: []group{{"k-", 9, nil, false}}}, want{status: knowledge.StatusApproved}},
		{
			"the item one past the review list is refused naming the items",
			args{approved: []group{{"k-", 10, nil, false}}},
			want{
				status: knowledge.StatusCandidate,
				err: "knowledge: folder may outgrow the review: 431 of 70000 chars 11 of 10 items in no_known_change with " +
					"k-0 v1 39 chars, k-1 v1 39 chars, k-2 v1 39 chars, k-3 v1 39 chars, k-4 v1 39 chars, " +
					"k-5 v1 39 chars, k-6 v1 39 chars, k-7 v1 39 chars, k-8 v1 39 chars, k-9 v1 39 chars",
			},
		},
		{
			"an unscoped item beside five items in each of two change contexts is approved",
			args{approved: split},
			want{status: knowledge.StatusApproved},
		},
		{
			"an item scoped to two change contexts of five items each is approved",
			args{approved: split, contexts: append(slices.Clone(quiet), planned...)},
			want{status: knowledge.StatusApproved},
		},
		{
			"an item of a change context that already carries ten items is refused naming only those",
			args{approved: []group{{"k-q", 10, quiet, false}, {"k-p", 5, planned, false}}},
			want{
				status: knowledge.StatusCandidate,
				err: "knowledge: folder may outgrow the review: 661 of 70000 chars 11 of 10 items in no_known_change with " +
					"k-q0 v1 62 chars, k-q1 v1 62 chars, k-q2 v1 62 chars, k-q3 v1 62 chars, k-q4 v1 62 chars, " +
					"k-q5 v1 62 chars, k-q6 v1 62 chars, k-q7 v1 62 chars, k-q8 v1 62 chars, k-q9 v1 62 chars",
			},
		},
		{
			"a change context the item excepts is never counted",
			args{approved: append(slices.Clone(split[:1]), group{"k-m", 5, measured, false}, group{"k-p", 10, planned, false}), exceptions: planned},
			want{status: knowledge.StatusApproved},
		},
		{"an item beside ten unscoped vetoes is approved", args{approved: vetoes, contexts: quiet}, want{status: knowledge.StatusApproved}},
		{"an eleventh veto is approved", args{approved: vetoes, vetoed: true}, want{status: knowledge.StatusApproved}},
		{
			"a veto beside a change context that already carries ten items is approved",
			args{approved: []group{{"k-q", 10, quiet, false}}, vetoed: true},
			want{status: knowledge.StatusApproved},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(
				store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
				func() time.Time { return now }, func(prefix string) string { return prefix + "new" },
			)
			var records []knowledge.Knowledge
			for _, g := range tc.args.approved {
				for i := range g.count {
					records = append(records, item(fmt.Sprintf("%s%d", g.prefix, i), knowledge.StatusApproved, g.contexts, g.vetoed))
				}
			}
			candidate := item("k-new", knowledge.StatusCandidate, tc.args.contexts, tc.args.vetoed)
			candidate.Exceptions = tc.args.exceptions
			require.NoError(t, testkit.Err(l.Import(ctx, append(records, candidate))))

			_, err = l.Approve(ctx, "k-new", 1, "jed")
			got := want{}
			if err != nil {
				got.err = err.Error()
			}
			history, herr := l.History(ctx, "k-new")
			require.NoError(t, herr)
			got.status = history[0].Status

			assert.Equal(t, tc.want, got)
		})
	}
}

// A review already past a cap takes a new version that replaces an item while the review does not grow
func TestLedgerApproveInOverfullFolder(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	quiet := []evidence.Context{evidence.ContextNoKnownChange}
	planned := []evidence.Context{evidence.ContextPlannedChange}
	item := func(id string, version int, status knowledge.Status, content string, contexts []evidence.Context) knowledge.Knowledge {
		k := knowledge.Knowledge{
			ID: id, Version: version, Kind: knowledge.KindMeaning, Content: content,
			Scope:    knowledge.Scope{Scope: evidence.Scope{ChangeContexts: contexts}},
			Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
			Status: status, Author: "author", Time: now,
		}
		if status != knowledge.StatusCandidate {
			k.Approver, k.ApprovedAt = "ann", now
		}
		return k
	}
	group := func(prefix string, count int, contexts []evidence.Context) []knowledge.Knowledge {
		out := make([]knowledge.Knowledge, 0, count)
		for i := range count {
			out = append(out, item(fmt.Sprintf("%s%d", prefix, i), 1, knowledge.StatusApproved, "one", contexts))
		}
		return out
	}
	// Eleven items in one change context as a ledger approved before the item cap leaves them
	overfull := group("k-", 11, quiet)
	version := func(content string, contexts []evidence.Context) knowledge.Knowledge {
		k := item("k-0", 2, knowledge.StatusCandidate, content, contexts)
		k.Base = 1
		return k
	}
	large := item("large", 1, knowledge.StatusApproved, strings.Repeat("가", knowledge.ReviewChars), quiet)
	retired := item("k-0", 1, knowledge.StatusRetired, "one", quiet)
	reapproved := version("one", quiet)
	reapproved.Base = 0
	type args struct {
		seeds []knowledge.Knowledge
		id    string
	}
	type want struct {
		status knowledge.Status
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a shorter new version in a review past the item cap is approved",
			args{append(slices.Clone(overfull), version("o", quiet)), "k-0"},
			want{knowledge.StatusApproved, nil},
		},
		{
			"a longer new version in a review past the item cap and under the char cap is approved",
			args{append(slices.Clone(overfull), version("one and more", quiet)), "k-0"},
			want{knowledge.StatusApproved, nil},
		},
		{
			"a longer new version in a review past the char cap is refused",
			args{[]knowledge.Knowledge{large, overfull[0], version("one and more", quiet)}, "k-0"},
			want{knowledge.StatusCandidate, knowledge.ErrFolderFull},
		},
		{
			"a new id in a review past the item cap is refused",
			args{append(slices.Clone(overfull), item("k-new", 1, knowledge.StatusCandidate, "o", quiet)), "k-new"},
			want{knowledge.StatusCandidate, knowledge.ErrFolderFull},
		},
		{
			"a new version that widens into another change context is refused before any cap is counted",
			args{slices.Concat(overfull, group("k-p", 10, planned), []knowledge.Knowledge{version("o", slices.Concat(quiet, planned))}), "k-0"},
			want{knowledge.StatusCandidate, knowledge.ErrScopeWidened},
		},
		{
			"a new version after its approved version was retired counts as an added item",
			args{append(slices.Clone(overfull), retired, reapproved), "k-0"},
			want{knowledge.StatusCandidate, knowledge.ErrFolderFull},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(
				store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
				func() time.Time { return now.Add(time.Hour) }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, testkit.Err(l.Import(ctx, tc.args.seeds)))
			latest, err := l.History(ctx, tc.args.id)
			require.NoError(t, err)

			v := latest[0].Version
			_, err = l.Approve(ctx, tc.args.id, v, "jed")
			history, herr := l.History(ctx, tc.args.id)
			require.NoError(t, herr)
			got := history[slices.IndexFunc(history, func(k knowledge.Knowledge) bool { return k.Version == v })]

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.status, got.Status)
		})
	}
}

func TestLedgerRetire(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "ann", at
	retired := candidate
	retired.Status, retired.Approver = knowledge.StatusRetired, "ann"
	superseded := approved
	superseded.Status = knowledge.StatusSuperseded
	candidateRetired := candidate
	candidateRetired.Status, candidateRetired.Approver, candidateRetired.Time = knowledge.StatusRetired, "jed", now
	approvedRetired := approved
	approvedRetired.Status, approvedRetired.Approver, approvedRetired.Time = knowledge.StatusRetired, "jed", now
	judgment := candidate
	judgment.Kind, judgment.Content = knowledge.KindJudgment, "never edit files with sed -i"
	judgment.Veto = &knowledge.Veto{
		Tool:    "Bash",
		When:    []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`}},
		Example: map[string]any{"command": "sed -i s/a/b/ f"},
	}
	judgmentApproved := judgment
	judgmentApproved.Status, judgmentApproved.Approver, judgmentApproved.ApprovedAt = knowledge.StatusApproved, "ann", at
	judgmentRetired := judgmentApproved
	judgmentRetired.Status, judgmentRetired.Approver, judgmentRetired.Time = knowledge.StatusRetired, "jed", now
	type args struct {
		seeds    []knowledge.Knowledge
		version  int
		approver string
	}
	type want struct {
		knowledge knowledge.Knowledge
		history   knowledge.Set
		// The generated veto file and empty when there is none
		vetoes string
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"candidate is retired under the approver's name",
			args{[]knowledge.Knowledge{candidate}, 1, "jed"},
			want{candidateRetired, knowledge.Set{candidateRetired, candidate}, "", nil},
		},
		{
			"retiring the only approved veto removes the generated file",
			args{[]knowledge.Knowledge{judgment, judgmentApproved}, 1, "jed"},
			want{judgmentRetired, knowledge.Set{judgmentRetired, judgmentApproved, judgment}, "", nil},
		},
		{
			"approved version is retired",
			args{[]knowledge.Knowledge{candidate, approved}, 1, "jed"},
			want{approvedRetired, knowledge.Set{approvedRetired, approved, candidate}, "", nil},
		},
		{
			"missing approver fails",
			args{[]knowledge.Knowledge{candidate}, 1, ""},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrApproverRequired},
		},
		{
			"unknown version is not found",
			args{[]knowledge.Knowledge{candidate}, 9, "jed"},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrNotFound},
		},
		{
			"unknown version without an approver is not found",
			args{[]knowledge.Knowledge{candidate}, 9, ""},
			want{history: knowledge.Set{candidate}, err: knowledge.ErrNotFound},
		},
		{
			"retired version cannot be retired again",
			args{[]knowledge.Knowledge{candidate, retired}, 1, "jed"},
			want{history: knowledge.Set{retired, candidate}, err: knowledge.ErrTransitionInvalid},
		},
		{
			"superseded version cannot be retired",
			args{[]knowledge.Knowledge{candidate, approved, superseded}, 1, "jed"},
			want{history: knowledge.Set{superseded, approved, candidate}, err: knowledge.ErrTransitionInvalid},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			home := t.TempDir()
			l := knowledge.NewLedger(
				store, vetofile.NewApprovedFile(home, "records"), evidence.DefaultContexts(),
				func() time.Time { return now }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, testkit.Err(l.Import(ctx, tc.args.seeds)))
			got, err := l.Retire(ctx, "k1", tc.args.version, tc.args.approver)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.knowledge, got)
			history, err := l.History(ctx, "k1")
			require.NoError(t, err)
			assert.Equal(t, tc.want.history, history)
			// A missing file reads as empty
			vetoes, _ := os.ReadFile(vetofile.NewApprovedFile(home, "records").Path())
			assert.Equal(t, tc.want.vetoes, string(vetoes))
		})
	}
}

func TestLedgerImport(t *testing.T) {
	at := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver, approved.ApprovedAt = knowledge.StatusApproved, "jed", at
	empty := candidate
	empty.Content = ""
	judgment := approved
	judgment.Kind, judgment.Content = knowledge.KindJudgment, "never edit files with sed -i"
	judgment.Veto = &knowledge.Veto{
		Tool:    "Bash",
		When:    []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`}},
		Example: map[string]any{"command": "sed -i s/a/b/ f"},
	}
	retired := approved
	retired.Status, retired.Time = knowledge.StatusRetired, at.Add(time.Hour)
	edited := approved
	edited.Content = "one edited"
	type args struct {
		// Imported before the records under test
		seeds   []knowledge.Knowledge
		records []knowledge.Knowledge
	}
	type want struct {
		imported knowledge.Set
		history  knowledge.Set
		// The generated veto file and empty when there is none
		vetoes string
		err    error
		msg    string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"valid records are appended as they are",
			args{nil, []knowledge.Knowledge{candidate, approved}},
			want{knowledge.Set{candidate, approved}, knowledge.Set{approved, candidate}, "", nil, "<nil>"},
		},
		{
			"an approved judgment with a veto is exported for guard",
			args{nil, []knowledge.Knowledge{judgment}},
			want{knowledge.Set{judgment}, knowledge.Set{judgment}, "# Generated by nodloop from the approved judgment knowledge of records\n" +
				"# Edits here are overwritten. Retire the knowledge to remove a veto\n" +
				"vetoes:\n" +
				"    - id: k1\n" +
				"      tool: Bash\n" +
				"      when:\n" +
				"        - field: command\n" +
				"          match: sed\\s+-i\n" +
				"      reason: never edit files with sed -i\n" +
				"      source: nodloop knowledge k1 v1 approved by jed\n", nil, "<nil>"},
		},
		{
			"invalid record stops the import before any record lands and is named by position",
			args{[]knowledge.Knowledge{candidate}, []knowledge.Knowledge{approved, empty}},
			want{nil, knowledge.Set{candidate}, "", knowledge.ErrContentRequired, "record 2: knowledge: content is required"},
		},
		{
			"an approved veto before an invalid record is neither recorded nor exported",
			args{[]knowledge.Knowledge{candidate}, []knowledge.Knowledge{judgment, empty}},
			want{nil, knowledge.Set{candidate}, "", knowledge.ErrContentRequired, "record 2: knowledge: content is required"},
		},
		{
			"importing the same file again after a retire keeps the retire",
			args{[]knowledge.Knowledge{candidate, approved, retired}, []knowledge.Knowledge{candidate, approved}},
			want{knowledge.Set{}, knowledge.Set{retired, approved, candidate}, "", nil, "<nil>"},
		},
		{
			"a record older than the recorded history of its version fails and lands nothing",
			args{[]knowledge.Knowledge{candidate, retired}, []knowledge.Knowledge{edited}},
			want{nil, knowledge.Set{retired, candidate}, "", knowledge.ErrImportStale,
				"record 1: knowledge: import record is older than the recorded history of its version: " +
					"k1 v1 approved at 2026-09-23T00:00:00Z and the recorded retired at 2026-09-23T01:00:00Z"},
		},
		{
			"a record as new as the recorded history lands",
			args{[]knowledge.Knowledge{candidate}, []knowledge.Knowledge{approved}},
			want{knowledge.Set{approved}, knowledge.Set{approved, candidate}, "", nil, "<nil>"},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			home := t.TempDir()
			l := knowledge.NewLedger(
				store, vetofile.NewApprovedFile(home, "records"), evidence.DefaultContexts(),
				func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, testkit.Err(l.Import(ctx, tc.args.seeds)))
			imported, err := l.Import(ctx, tc.args.records)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.msg, fmt.Sprint(err))
			assert.Equal(t, tc.want.imported, imported)
			all, err := l.All(ctx)
			require.NoError(t, err)
			history := all.Matching(knowledge.Filter{})
			assert.Equal(t, tc.want.history, history)
			// A missing file reads as empty
			vetoes, _ := os.ReadFile(vetofile.NewApprovedFile(home, "records").Path())
			assert.Equal(t, tc.want.vetoes, string(vetoes))
		})
	}
}

func TestLedgerFailsOnBrokenStore(t *testing.T) {
	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) }
	newID := func(prefix string) string { return prefix + "new" }
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "one",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author",
	}
	unreadableDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(unreadableDir, "knowledge.jsonl"), 0o700))
	unreadableStore, err := file.New(unreadableDir)
	require.NoError(t, err)
	unreadable := knowledge.NewLedger(
		unreadableStore, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
		now, newID,
	)
	readOnlyDir := t.TempDir()
	readOnlyStore, err := file.New(readOnlyDir)
	require.NoError(t, err)
	readOnly := knowledge.NewLedger(
		readOnlyStore, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
		now, newID,
	)
	require.NoError(t, testkit.Err(readOnly.Import(ctx, []knowledge.Knowledge{candidate})))
	require.NoError(t, os.Chmod(filepath.Join(readOnlyDir, "knowledge.jsonl"), 0o400))
	// A file where the veto directory belongs so every export fails
	// The records go in through a ledger whose export works
	blockedHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(blockedHome, ".claude"), nil, 0o600))
	approved := candidate
	approved.ID, approved.Status, approved.Approver = "k2", knowledge.StatusApproved, "ann"
	// One store per case so each case writes on its own records
	blocked := func() *knowledge.Ledger {
		store, err := file.New(t.TempDir())
		require.NoError(t, err)
		seeder := knowledge.NewLedger(store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(), now, newID)
		require.NoError(t, testkit.Err(seeder.Import(ctx, []knowledge.Knowledge{candidate, approved})))
		return knowledge.NewLedger(store, vetofile.NewApprovedFile(blockedHome, "records"), evidence.DefaultContexts(), now, newID)
	}
	approvedNow := candidate
	approvedNow.Status, approvedNow.Approver = knowledge.StatusApproved, "jed"
	approvedNow.ApprovedAt, approvedNow.Time = now(), now()
	retiredNow := approved
	retiredNow.Status, retiredNow.Approver, retiredNow.Time = knowledge.StatusRetired, "jed", now()
	third := candidate
	third.ID = "k3"
	type args struct {
		ledger *knowledge.Ledger
		call   func(context.Context, *knowledge.Ledger) (any, error)
	}
	type want struct {
		value any
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"all fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.All(ctx) }},
			want{knowledge.Set(nil), file.ErrRead},
		},
		{
			"history fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.History(ctx, "k1")
			}},
			want{knowledge.Set(nil), file.ErrRead},
		},
		{
			"overlaps fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Overlaps(ctx, "k1")
			}},
			want{knowledge.Set(nil), file.ErrRead},
		},
		{
			"approved fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Approved(ctx, "k1", 1)
			}},
			want{knowledge.Knowledge{}, file.ErrRead},
		},
		{
			"propose fails to read the records it decides on as a failed append",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				k, overlaps, err := l.Propose(ctx, candidate)
				return []any{k, overlaps}, err
			}},
			want{[]any{knowledge.Knowledge{}, knowledge.Set(nil)}, file.ErrAppend},
		},
		{
			"approve fails to read the records it decides on as a failed append",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Approve(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrAppend},
		},
		{
			"retire fails to read the records it decides on as a failed append",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Retire(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrAppend},
		},
		{
			"import fails to read the records it decides on as a failed append",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Import(ctx, []knowledge.Knowledge{candidate})
			}},
			want{knowledge.Set(nil), file.ErrAppend},
		},
		{
			"propose compaction fails to read the records it decides on as a failed append",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.ProposeCompaction(ctx, "k1", nil)
			}},
			want{knowledge.Compaction{}, file.ErrAppend},
		},
		{
			"compaction fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Compaction(ctx, "c-x")
			}},
			want{knowledge.Compaction{}, file.ErrRead},
		},
		{
			"preview fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Preview(ctx, "c-x")
			}},
			want{(*knowledge.Preview)(nil), file.ErrRead},
		},
		{
			"approve compaction fails to read the records it decides on as a failed append",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				replay := knowledge.Replay{Compaction: "c-x", Events: []knowledge.ReplayEvent{{Expected: "hold", Got: "hold"}}}
				return l.ApproveCompaction(ctx, "c-x", "jed", replay)
			}},
			want{knowledge.Compaction{}, file.ErrAppend},
		},
		{
			"folder fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.Folder(ctx, "k1", 1) }},
			want{knowledge.Folder{}, file.ErrRead},
		},
		{
			"import fails to export the vetoes after appending",
			args{blocked(), func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Import(ctx, []knowledge.Knowledge{third})
			}},
			want{knowledge.Set{third}, knowledge.ErrExport},
		},
		{
			"approve fails to export the vetoes and still returns the approved record",
			args{blocked(), func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Approve(ctx, "k1", 1, "jed")
			}},
			want{approvedNow, knowledge.ErrExport},
		},
		{
			"retire fails to export the vetoes and still returns the retired record",
			args{blocked(), func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Retire(ctx, "k2", 1, "jed")
			}},
			want{retiredNow, knowledge.ErrExport},
		},
		{
			"propose fails to append",
			args{readOnly, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				k, overlaps, err := l.Propose(ctx, candidate)
				return []any{k, overlaps}, err
			}},
			want{[]any{knowledge.Knowledge{}, knowledge.Set(nil)}, file.ErrAppend},
		},
		{
			"approve fails to append",
			args{readOnly, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Approve(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrAppend},
		},
		{
			"retire fails to append",
			args{readOnly, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Retire(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrAppend},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.args.call(ctx, tc.args.ledger)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.value, got)
		})
	}
}

// A call killed between its append and its hand off leaves the vetoes behind the way a failed hand off does
// Its retry is refused and still brings the vetoes in step
func TestLedgerRefusedRetryExportsVetoes(t *testing.T) {
	ctx := context.Background()
	now := func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	newID := func(prefix string) string { return prefix + "new" }
	judgment := knowledge.Knowledge{
		ID: "v", Version: 1, Kind: knowledge.KindJudgment, Content: "never run cmd",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: now(),
		Veto: &knowledge.Veto{
			Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: `^cmd\b`}},
			Example: map[string]any{"command": "cmd now"},
		},
	}
	type call func(context.Context, *knowledge.Ledger) error
	approve := func(ctx context.Context, l *knowledge.Ledger) error {
		_, err := l.Approve(ctx, "v", 1, "jed")
		return err
	}
	retire := func(ctx context.Context, l *knowledge.Ledger) error {
		_, err := l.Retire(ctx, "v", 1, "jed")
		return err
	}
	type args struct {
		// Runs with a working hand off before the lost one
		before call
		// Loses its hand off and then runs again with a working one
		lost call
	}
	tcs := []struct {
		name string
		args args
		// Veto ids in the approved file after the retry
		want []string
	}{
		{"a refused approve exports the veto its lost hand off left out", args{nil, approve}, []string{"v"}},
		{"a refused retire removes the veto its lost hand off left in", args{approve, retire}, []string{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, home, blockedHome := t.TempDir(), t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(blockedHome, ".claude"), nil, 0o600))
			store, err := file.New(dir)
			require.NoError(t, err)
			working := knowledge.NewLedger(store, vetofile.NewApprovedFile(home, dir), evidence.DefaultContexts(), now, newID)
			require.NoError(t, testkit.Err(working.Import(ctx, []knowledge.Knowledge{judgment})))
			if tc.args.before != nil {
				require.NoError(t, tc.args.before(ctx, working))
			}
			blocked := knowledge.NewLedger(store, vetofile.NewApprovedFile(blockedHome, dir), evidence.DefaultContexts(), now, newID)
			require.ErrorIs(t, tc.args.lost(ctx, blocked), knowledge.ErrExport)

			err = tc.args.lost(ctx, working)

			assert.ErrorIs(t, err, knowledge.ErrTransitionInvalid)
			b, _ := os.ReadFile(vetofile.NewApprovedFile(home, dir).Path())
			vetoes, err := veto.Parse(b)
			require.NoError(t, err)
			ids := []string{}
			for _, v := range vetoes {
				ids = append(ids, v.ID())
			}
			assert.Equal(t, tc.want, ids)
		})
	}
}

// Every lookup by id fails the same way when no record carries the id
func TestLedgerUnknownID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	now := func() time.Time { return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) }
	l := knowledge.NewLedger(
		store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
		now, func(prefix string) string { return prefix + "new" },
	)
	type want struct {
		value any
		err   error
	}
	tcs := []struct {
		name string
		args func(context.Context, *knowledge.Ledger) (any, error)
		want want
	}{
		{
			"history of an unknown id is not found",
			func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.History(ctx, "nope") },
			want{knowledge.Set(nil), knowledge.ErrNotFound},
		},
		{
			"folder of an unknown id is not found",
			func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.Folder(ctx, "nope", 1) },
			want{knowledge.Folder{}, knowledge.ErrNotFound},
		},
		{
			"approve of an unknown id is not found",
			func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.Approve(ctx, "nope", 1, "jed") },
			want{knowledge.Knowledge{}, knowledge.ErrNotFound},
		},
		{
			"retire of an unknown id is not found",
			func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.Retire(ctx, "nope", 1, "jed") },
			want{knowledge.Knowledge{}, knowledge.ErrNotFound},
		},
		{
			"compaction of an unknown id is not found",
			func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.Compaction(ctx, "c-none") },
			want{knowledge.Compaction{}, knowledge.ErrNotFound},
		},
		{
			"preview of an unknown compaction is not found",
			func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.Preview(ctx, "c-none") },
			want{(*knowledge.Preview)(nil), knowledge.ErrNotFound},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.args(ctx, l)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.value, got)
		})
	}
}

func TestLedgerFolder(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	lag := knowledge.Scope{Scope: evidence.Scope{
		ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}, Metrics: []string{"conversion_count"},
	}}
	candidate := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: strings.Repeat("x", 60), Scope: lag,
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: now,
	}
	neighbour := candidate
	neighbour.ID, neighbour.Status, neighbour.Approver = "k-other", knowledge.StatusApproved, "ann"
	clicks := neighbour
	clicks.ID, clicks.Scope = "k-clicks", knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"click_count"}}}
	planned := neighbour
	planned.ID = "k-planned"
	planned.Scope.ChangeContexts = []evidence.Context{evidence.ContextPlannedChange}
	excepting := neighbour
	excepting.ID, excepting.Scope = "k-except", knowledge.Scope{}
	excepting.Exceptions = []evidence.Context{evidence.ContextNoKnownChange}
	everywhere := neighbour
	everywhere.ID, everywhere.Scope = "k-any", knowledge.Scope{}
	oldVersion := candidate
	oldVersion.Status, oldVersion.Approver = knowledge.StatusApproved, "ann"
	longer := candidate
	longer.Version, longer.Content = 2, strings.Repeat("x", 90)
	onIOS := candidate
	onIOS.Scope.Dims = map[string]string{"platform": "ios"}
	korean := candidate
	korean.Content = strings.Repeat("가", 60)
	emoji := candidate
	emoji.Content = strings.Repeat("🙂", 60)
	crowd := make([]knowledge.Knowledge, 0, 5)
	replayed := make([]knowledge.Knowledge, 0, 5)
	for _, id := range []string{"k-a", "k-b", "k-c", "k-d", "k-e"} {
		k := neighbour
		k.ID = id
		crowd = append(crowd, k)
		k.Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{"t-" + id}}
		replayed = append(replayed, k)
	}
	anchor := oldVersion
	wide := candidate
	wide.Scope = knowledge.Scope{}
	quiet := evidence.ContextNoKnownChange
	anchor.Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{"t-k1"}}
	type args struct {
		seeds   []knowledge.Knowledge
		version int
	}
	type want struct {
		folder  knowledge.Folder
		crowded bool
		err     error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"an item of the same contexts and metrics shares the folder",
			args{[]knowledge.Knowledge{neighbour, candidate}, 1},
			want{knowledge.Folder{Chars: 143 + 148, Carried: knowledge.Set{neighbour}, Context: quiet}, false, nil},
		},
		{
			"an item of other metrics shares the folder because one event may move both metrics",
			args{[]knowledge.Knowledge{clicks, candidate}, 1},
			want{knowledge.Folder{Chars: 143 + 111, Carried: knowledge.Set{clicks}, Context: quiet}, false, nil},
		},
		{
			"an item of other change contexts sits in another folder",
			args{[]knowledge.Knowledge{planned, candidate}, 1},
			want{knowledge.Folder{Chars: 143, Carried: knowledge.Set{}, Context: quiet}, false, nil},
		},
		{
			"an item that excepts every change context of the item never joins it",
			args{[]knowledge.Knowledge{excepting, candidate}, 1},
			want{knowledge.Folder{Chars: 143, Carried: knowledge.Set{}, Context: quiet}, false, nil},
		},
		{
			"an item with an empty scope sits in every folder",
			args{[]knowledge.Knowledge{everywhere, candidate}, 1},
			want{knowledge.Folder{Chars: 143 + 98, Carried: knowledge.Set{everywhere}, Context: quiet}, false, nil},
		},
		{
			"a new version is measured by its own text and replaces the approved one",
			args{[]knowledge.Knowledge{oldVersion, longer}, 2},
			want{knowledge.Folder{Chars: 173, Carried: knowledge.Set{}, Context: quiet}, false, nil},
		},
		{
			"dims never split a folder",
			args{[]knowledge.Knowledge{neighbour, onIOS}, 1},
			want{knowledge.Folder{Chars: 157 + 148, Carried: knowledge.Set{neighbour}, Context: quiet}, false, nil},
		},
		{
			"korean content counts one char per rune",
			args{[]knowledge.Knowledge{korean}, 1},
			want{knowledge.Folder{Chars: 143, Carried: knowledge.Set{}, Context: quiet}, false, nil},
		},
		{
			"emoji content counts one char per rune",
			args{[]knowledge.Knowledge{emoji}, 1},
			want{knowledge.Folder{Chars: 143, Carried: knowledge.Set{}, Context: quiet}, false, nil},
		},
		{
			"five replayable items with an approved replayable item are not crowded",
			args{append(slices.Clone(replayed[:4]), anchor), 1},
			want{knowledge.Folder{Chars: 143 + 4*144, Carried: knowledge.Set(replayed[:4]), Context: quiet, Compactable: 5}, false, nil},
		},
		{
			"six replayable items with an approved replayable item are crowded",
			args{append(slices.Clone(replayed), anchor), 1},
			want{knowledge.Folder{Chars: 143 + 5*144, Carried: knowledge.Set(replayed), Context: quiet, Compactable: 6}, true, nil},
		},
		{
			"items that cite only paragraphs never count toward a compaction",
			args{append(slices.Clone(crowd), anchor), 1},
			want{knowledge.Folder{Chars: 143 + 5*144, Carried: knowledge.Set(crowd), Context: quiet, Compactable: 1}, false, nil},
		},
		{
			"a candidate is never crowded because only an approved item anchors a compaction",
			args{append(slices.Clone(replayed), candidate), 1},
			want{knowledge.Folder{Chars: 143 + 5*144, Carried: knowledge.Set(replayed), Context: quiet}, false, nil},
		},
		{
			"an item spanning two change contexts carries only the heavier one",
			args{[]knowledge.Knowledge{neighbour, planned, wide}, 1},
			want{
				knowledge.Folder{
					Chars: 97 + 159, Carried: knowledge.Set{planned}, Context: evidence.ContextPlannedChange,
				},
				false, nil,
			},
		},
		{
			"an unknown version is not found",
			args{[]knowledge.Knowledge{candidate}, 9},
			want{knowledge.Folder{}, false, knowledge.ErrNotFound},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(
				store, vetofile.NewApprovedFile(t.TempDir(), "records"), evidence.DefaultContexts(),
				func() time.Time { return now }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, testkit.Err(l.Import(ctx, tc.args.seeds)))

			got, err := l.Folder(ctx, "k1", tc.args.version)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.folder, got)
			assert.Equal(t, tc.want.crowded, got.Crowded())
		})
	}
}

// Separate ledgers on one record dir write at the same moment as two sessions would
// 1. every write decides on the records the others left so a check only one may pass passes once
// 2. writes that no check ties together all land
func TestLedgerConcurrentWrite(t *testing.T) {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	candidate := knowledge.Knowledge{
		ID: "item", Version: 1, Kind: knowledge.KindMeaning, Content: "lag is four hours",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := candidate
	approved.Status, approved.Approver = knowledge.StatusApproved, "ann"
	// Nine approved items fill a review but one
	full := make([]knowledge.Knowledge, 0, 11)
	for i := range 9 {
		k := approved
		k.ID = fmt.Sprintf("full-%d", i)
		full = append(full, k)
	}
	candidates := make([]knowledge.Knowledge, 0, 3)
	for i := range 3 {
		k := candidate
		k.ID = fmt.Sprintf("par-%d", i)
		candidates = append(candidates, k)
	}
	proposal := candidate
	proposal.Status, proposal.Version = "", 0
	anonymous := proposal
	anonymous.ID = ""
	type args struct {
		seeds []knowledge.Knowledge
		calls []func(ctx context.Context, l *knowledge.Ledger) error
	}
	type want struct {
		// Records newest first as id version status after the calls in any order
		versions []string
		failed   int
		err      error
	}
	approve := func(id string) func(ctx context.Context, l *knowledge.Ledger) error {
		return func(ctx context.Context, l *knowledge.Ledger) error {
			_, err := l.Approve(ctx, id, 1, "jed")
			return err
		}
	}
	propose := func(draft knowledge.Knowledge) func(ctx context.Context, l *knowledge.Ledger) error {
		return func(ctx context.Context, l *knowledge.Ledger) error {
			_, _, err := l.Propose(ctx, draft)
			return err
		}
	}
	retire := func(ctx context.Context, l *knowledge.Ledger) error {
		_, err := l.Retire(ctx, "item", 1, "jed")
		return err
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"two proposals of one id become two versions and never a second v1",
			args{nil, []func(context.Context, *knowledge.Ledger) error{propose(proposal), propose(proposal)}},
			want{[]string{"item 1 candidate", "item 2 candidate"}, 0, nil},
		},
		{
			"two approvals of one version approve it once",
			args{[]knowledge.Knowledge{candidate}, []func(context.Context, *knowledge.Ledger) error{approve("item"), approve("item")}},
			want{[]string{"item 1 approved"}, 1, knowledge.ErrTransitionInvalid},
		},
		{
			"two retires of one version retire it once",
			args{[]knowledge.Knowledge{candidate}, []func(context.Context, *knowledge.Ledger) error{retire, retire}},
			want{[]string{"item 1 retired"}, 1, knowledge.ErrTransitionInvalid},
		},
		{
			"two approvals into the last seat of a review approve one",
			args{
				append(slices.Clone(full), candidates[:2]...),
				[]func(context.Context, *knowledge.Ledger) error{approve("par-0"), approve("par-1")},
			},
			want{nil, 1, knowledge.ErrFolderFull},
		},
		{
			"approvals of three ids at once all land",
			args{candidates, []func(context.Context, *knowledge.Ledger) error{approve("par-0"), approve("par-1"), approve("par-2")}},
			want{[]string{"par-0 1 approved", "par-1 1 approved", "par-2 1 approved"}, 0, nil},
		},
		{
			"proposals with generated ids at once all land",
			args{nil, []func(context.Context, *knowledge.Ledger) error{propose(anonymous), propose(anonymous), propose(anonymous)}},
			want{[]string{"k-0 1 candidate", "k-1 1 candidate", "k-2 1 candidate"}, 0, nil},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			sink := vetofile.NewApprovedFile(t.TempDir(), dir)
			open := func(n int) *knowledge.Ledger {
				store, err := file.New(dir)
				require.NoError(t, err)
				return knowledge.NewLedger(store, sink, evidence.DefaultContexts(), func() time.Time { return at }, func(p string) string { return fmt.Sprintf("%s%d", p, n) })
			}
			require.NoError(t, testkit.Err(open(0).Import(ctx, tc.args.seeds)))
			start, results := make(chan struct{}), make(chan error, len(tc.args.calls))
			for i, call := range tc.args.calls {
				l := open(i)
				go func() {
					<-start
					results <- call(ctx, l)
				}()
			}
			close(start)
			failed := 0
			for range tc.args.calls {
				if err := <-results; err != nil {
					failed++
					assert.ErrorIs(t, err, tc.want.err)
				}
			}

			all, err := open(0).All(ctx)
			require.NoError(t, err)
			var versions []string
			for _, k := range all.Versions() {
				if !strings.HasPrefix(k.ID, "full-") && (tc.want.versions != nil || k.Status == knowledge.StatusApproved) {
					versions = append(versions, fmt.Sprintf("%s %d %s", k.ID, k.Version, k.Status))
				}
			}
			assert.Equal(t, tc.want.failed, failed)
			if tc.want.versions != nil {
				assert.ElementsMatch(t, tc.want.versions, versions)
			} else {
				assert.Len(t, versions, 1, "one of the two approvals lands")
			}
		})
	}
}

// Approvals of six vetoed judgments from separate ledgers at once
// Every approved veto reaches the file whatever order the exports finish in
func TestLedgerConcurrentApprovalsKeepEveryVeto(t *testing.T) {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	dir, home := t.TempDir(), t.TempDir()
	const judgments = 6
	var seeds []knowledge.Knowledge
	for i := range judgments {
		seeds = append(seeds, knowledge.Knowledge{
			ID: fmt.Sprintf("v%d", i), Version: 1, Kind: knowledge.KindJudgment, Content: fmt.Sprintf("never run cmd%d", i),
			Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
			Status: knowledge.StatusCandidate, Author: "author", Time: at,
			Veto: &knowledge.Veto{
				Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: fmt.Sprintf(`cmd%d\b`, i)}},
				Example: map[string]any{"command": fmt.Sprintf("cmd%d", i)},
			},
		})
	}
	open := func() *knowledge.Ledger {
		store, err := file.New(dir)
		require.NoError(t, err)
		return knowledge.NewLedger(
			store, vetofile.NewApprovedFile(home, dir), evidence.DefaultContexts(),
			func() time.Time { return at }, func(p string) string { return p },
		)
	}
	require.NoError(t, testkit.Err(open().Import(ctx, seeds)))
	ledgers := make([]*knowledge.Ledger, judgments)
	for i := range ledgers {
		ledgers[i] = open()
	}
	var wg sync.WaitGroup
	for i, l := range ledgers {
		wg.Go(func() {
			_, err := l.Approve(ctx, fmt.Sprintf("v%d", i), 1, "jed")
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	all, err := open().All(ctx)
	require.NoError(t, err)
	assert.Len(t, all.Approved(), judgments)
	b, err := os.ReadFile(vetofile.NewApprovedFile(home, dir).Path())
	require.NoError(t, err)
	vetoes, err := veto.Parse(b)
	require.NoError(t, err)
	assert.Len(t, vetoes, judgments)
}
