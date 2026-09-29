package knowledge_test

import (
	"context"
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
		store, vetofile.NewApprovedFile(t.TempDir(), "records"),
		func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
	)
	require.NoError(t, l.Import(ctx, []knowledge.Knowledge{candidate, approved, other}))
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
		store, vetofile.NewApprovedFile(t.TempDir(), "records"),
		func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
	)
	require.NoError(t, l.Import(ctx, []knowledge.Knowledge{first, second, candidate, other}))
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
		store, vetofile.NewApprovedFile(t.TempDir(), "records"),
		func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
	)
	require.NoError(t, l.Import(ctx, []knowledge.Knowledge{candidate, other, approved}))
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
		store, vetofile.NewApprovedFile(t.TempDir(), "records"),
		func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
	)
	require.NoError(t, l.Import(ctx, []knowledge.Knowledge{k1, k2, retired}))
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := l.Overlaps(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.overlaps, got)
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
	filled.Version, filled.Status = 7, knowledge.StatusApproved
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
	proposedV4.Version = 4
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
				store, vetofile.NewApprovedFile(t.TempDir(), "records"),
				func() time.Time { return now }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, l.Import(ctx, tc.args.seeds))
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
				store, vetofile.NewApprovedFile(home, "records"),
				func() time.Time { return now }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, l.Import(ctx, tc.args.seeds))
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
				store, vetofile.NewApprovedFile(home, "records"),
				func() time.Time { return now }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, l.Import(ctx, tc.args.seeds))
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
	type want struct {
		history knowledge.Set
		// The generated veto file and empty when there is none
		vetoes string
		err    error
		msg    string
	}
	tcs := []struct {
		name string
		args []knowledge.Knowledge
		want want
	}{
		{
			"valid records are appended as they are",
			[]knowledge.Knowledge{candidate, approved},
			want{knowledge.Set{approved, candidate}, "", nil, "<nil>"},
		},
		{
			"an approved judgment with a veto is exported for guard",
			[]knowledge.Knowledge{judgment},
			want{knowledge.Set{judgment}, "# Generated by nodloop from the approved judgment knowledge of records\n" +
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
			"invalid record stops the import after the valid ones and is named by position",
			[]knowledge.Knowledge{candidate, empty, approved},
			want{knowledge.Set{candidate}, "", knowledge.ErrContentRequired, "record 2: knowledge: content is required"},
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
				store, vetofile.NewApprovedFile(home, "records"),
				func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
			)
			err = l.Import(ctx, tc.args)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.msg, fmt.Sprint(err))
			history, err := l.History(ctx, "k1")
			require.NoError(t, err)
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
		unreadableStore, vetofile.NewApprovedFile(t.TempDir(), "records"), now, newID,
	)
	readOnlyDir := t.TempDir()
	readOnlyStore, err := file.New(readOnlyDir)
	require.NoError(t, err)
	readOnly := knowledge.NewLedger(
		readOnlyStore, vetofile.NewApprovedFile(t.TempDir(), "records"), now, newID,
	)
	require.NoError(t, readOnly.Import(ctx, []knowledge.Knowledge{candidate}))
	require.NoError(t, os.Chmod(filepath.Join(readOnlyDir, "knowledge.jsonl"), 0o400))
	// A file where the veto directory belongs so every export fails
	// The records go in through a ledger whose export works
	blockedHome := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(blockedHome, ".claude"), nil, 0o600))
	blockedStore, err := file.New(t.TempDir())
	require.NoError(t, err)
	approved := candidate
	approved.ID, approved.Status, approved.Approver = "k2", knowledge.StatusApproved, "ann"
	seeder := knowledge.NewLedger(
		blockedStore, vetofile.NewApprovedFile(t.TempDir(), "records"), now, newID,
	)
	require.NoError(t, seeder.Import(ctx, []knowledge.Knowledge{candidate, approved}))
	blocked := knowledge.NewLedger(
		blockedStore, vetofile.NewApprovedFile(blockedHome, "records"), now, newID,
	)
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
			"propose fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				k, overlaps, err := l.Propose(ctx, candidate)
				return []any{k, overlaps}, err
			}},
			want{[]any{knowledge.Knowledge{}, knowledge.Set(nil)}, file.ErrRead},
		},
		{
			"approve fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Approve(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrRead},
		},
		{
			"retire fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Retire(ctx, "k1", 1, "jed")
			}},
			want{knowledge.Knowledge{}, file.ErrRead},
		},
		{
			"import fails to append",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return nil, l.Import(ctx, []knowledge.Knowledge{candidate})
			}},
			want{nil, file.ErrAppend},
		},
		{
			"propose compaction fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.ProposeCompaction(ctx, "k1", nil)
			}},
			want{knowledge.Compaction{}, file.ErrRead},
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
			"approve compaction fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				replay := knowledge.Replay{Compaction: "c-x", Events: []knowledge.ReplayEvent{{Expected: "hold", Got: "hold"}}}
				return l.ApproveCompaction(ctx, "c-x", "jed", replay)
			}},
			want{knowledge.Compaction{}, file.ErrRead},
		},
		{
			"folder fails to read",
			args{unreadable, func(ctx context.Context, l *knowledge.Ledger) (any, error) { return l.Folder(ctx, "k1", 1) }},
			want{knowledge.Folder{}, file.ErrRead},
		},
		{
			"import fails to export the vetoes after appending",
			args{blocked, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return nil, l.Import(ctx, []knowledge.Knowledge{third})
			}},
			want{nil, knowledge.ErrVetoExport},
		},
		{
			"approve fails to export the vetoes and still returns the approved record",
			args{blocked, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Approve(ctx, "k1", 1, "jed")
			}},
			want{approvedNow, knowledge.ErrVetoExport},
		},
		{
			"retire fails to export the vetoes and still returns the retired record",
			args{blocked, func(ctx context.Context, l *knowledge.Ledger) (any, error) {
				return l.Retire(ctx, "k2", 1, "jed")
			}},
			want{retiredNow, knowledge.ErrVetoExport},
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

// Every lookup by id fails the same way when no record carries the id
func TestLedgerUnknownID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := file.New(t.TempDir())
	require.NoError(t, err)
	now := func() time.Time { return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) }
	l := knowledge.NewLedger(
		store, vetofile.NewApprovedFile(t.TempDir(), "records"), now, func(prefix string) string { return prefix + "new" },
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
			want{knowledge.Folder{Chars: 143 + 148, Items: knowledge.Set{neighbour}}, false, nil},
		},
		{
			"an item of other metrics sits in another folder",
			args{[]knowledge.Knowledge{clicks, candidate}, 1},
			want{knowledge.Folder{Chars: 143, Items: knowledge.Set{}}, false, nil},
		},
		{
			"an item of other change contexts sits in another folder",
			args{[]knowledge.Knowledge{planned, candidate}, 1},
			want{knowledge.Folder{Chars: 143, Items: knowledge.Set{}}, false, nil},
		},
		{
			"an item that excepts every change context of the item never joins it",
			args{[]knowledge.Knowledge{excepting, candidate}, 1},
			want{knowledge.Folder{Chars: 143, Items: knowledge.Set{}}, false, nil},
		},
		{
			"an item with an empty scope sits in every folder",
			args{[]knowledge.Knowledge{everywhere, candidate}, 1},
			want{knowledge.Folder{Chars: 143 + 98, Items: knowledge.Set{everywhere}}, false, nil},
		},
		{
			"a new version is measured by its own text and replaces the approved one",
			args{[]knowledge.Knowledge{oldVersion, longer}, 2},
			want{knowledge.Folder{Chars: 173, Items: knowledge.Set{}}, false, nil},
		},
		{
			"dims never split a folder",
			args{[]knowledge.Knowledge{neighbour, onIOS}, 1},
			want{knowledge.Folder{Chars: 157 + 148, Items: knowledge.Set{neighbour}}, false, nil},
		},
		{
			"korean content counts one char per rune",
			args{[]knowledge.Knowledge{korean}, 1},
			want{knowledge.Folder{Chars: 143, Items: knowledge.Set{}}, false, nil},
		},
		{
			"emoji content counts one char per rune",
			args{[]knowledge.Knowledge{emoji}, 1},
			want{knowledge.Folder{Chars: 143, Items: knowledge.Set{}}, false, nil},
		},
		{
			"five replayable items with an approved replayable item are not crowded",
			args{append(slices.Clone(replayed[:4]), anchor), 1},
			want{knowledge.Folder{Chars: 143 + 4*144, Items: knowledge.Set(replayed[:4]), Compactable: 5}, false, nil},
		},
		{
			"six replayable items with an approved replayable item are crowded",
			args{append(slices.Clone(replayed), anchor), 1},
			want{knowledge.Folder{Chars: 143 + 5*144, Items: knowledge.Set(replayed), Compactable: 6}, true, nil},
		},
		{
			"items that cite only paragraphs never count toward a compaction",
			args{append(slices.Clone(crowd), anchor), 1},
			want{knowledge.Folder{Chars: 143 + 5*144, Items: knowledge.Set(crowd), Compactable: 1}, false, nil},
		},
		{
			"a candidate is never crowded because only an approved item anchors a compaction",
			args{append(slices.Clone(replayed), candidate), 1},
			want{knowledge.Folder{Chars: 143 + 5*144, Items: knowledge.Set(replayed)}, false, nil},
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
				store, vetofile.NewApprovedFile(t.TempDir(), "records"),
				func() time.Time { return now }, func(prefix string) string { return prefix + "new" },
			)
			require.NoError(t, l.Import(ctx, tc.args.seeds))

			got, err := l.Folder(ctx, "k1", tc.args.version)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.folder, got)
			assert.Equal(t, tc.want.crowded, got.Crowded())
		})
	}
}
