package knowledge_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestKnowledgeStale(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	approved := knowledge.Knowledge{Status: knowledge.StatusApproved, ApprovedAt: at}
	reaffirmed := approved
	reaffirmed.ReviewedAt = at.AddDate(0, 0, 80)
	type args struct {
		item    knowledge.Knowledge
		elapsed int
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a day before the deadline", args{approved, knowledge.ReviewDays - 1}, false},
		{"on the deadline", args{approved, knowledge.ReviewDays}, true},
		{"a day after the deadline", args{approved, knowledge.ReviewDays + 1}, true},
		{"a reaffirm moves the deadline", args{reaffirmed, knowledge.ReviewDays}, false},
		{
			"the record time without an approval time",
			args{knowledge.Knowledge{Status: knowledge.StatusApproved, Time: at}, knowledge.ReviewDays},
			true,
		},
		{"a candidate is never stale", args{knowledge.Knowledge{Status: knowledge.StatusCandidate, Time: at}, 1000}, false},
		{
			"a retired version is never stale",
			args{knowledge.Knowledge{Status: knowledge.StatusRetired, Time: at}, 1000},
			false,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.item.Stale(at.AddDate(0, 0, tc.args.elapsed)))
		})
	}
}

func TestKnowledgeLastReviewed(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		args knowledge.Knowledge
		want time.Time
	}{
		{
			"reaffirm time first",
			knowledge.Knowledge{ReviewedAt: at.Add(2 * time.Hour), ApprovedAt: at.Add(time.Hour), Time: at},
			at.Add(2 * time.Hour),
		},
		{"approval time next", knowledge.Knowledge{ApprovedAt: at.Add(time.Hour), Time: at}, at.Add(time.Hour)},
		{"record time last", knowledge.Knowledge{Time: at}, at},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.LastReviewed())
		})
	}
}

func TestLedgerReaffirm(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := old.AddDate(0, 0, 100)
	item := knowledge.Knowledge{
		ID: "item", Version: 1, Kind: knowledge.KindMeaning, Content: "units",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusApproved, Author: "author", Approver: "first", ApprovedAt: old, Time: old,
	}
	next := item
	next.Version = 2
	superseded := item
	superseded.Status = knowledge.StatusSuperseded
	retired := item
	retired.Status = knowledge.StatusRetired
	candidate := item
	candidate.Status, candidate.Approver, candidate.ApprovedAt = knowledge.StatusCandidate, "", time.Time{}
	type args struct {
		records  []knowledge.Knowledge
		id       string
		version  int
		approver string
	}
	type want struct {
		records  int
		stale    bool
		approver string
		err      error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the approved version is reaffirmed",
			args{[]knowledge.Knowledge{item}, "item", 1, "second"},
			want{2, false, "second", nil},
		},
		{
			"an approver is required",
			args{[]knowledge.Knowledge{item}, "item", 1, ""},
			want{1, true, "first", knowledge.ErrApproverRequired},
		},
		{
			"a candidate is refused",
			args{[]knowledge.Knowledge{candidate}, "item", 1, "second"},
			want{1, false, "", knowledge.ErrVersionUnapproved},
		},
		{
			"a retired version is refused",
			args{[]knowledge.Knowledge{item, retired}, "item", 1, "second"},
			want{2, false, "first", knowledge.ErrVersionUnapproved},
		},
		{
			"a superseded version is refused",
			args{[]knowledge.Knowledge{item, next, superseded}, "item", 1, "second"},
			want{3, false, "first", knowledge.ErrVersionUnapproved},
		},
		{
			"an unknown id is not found",
			args{[]knowledge.Knowledge{item}, "other", 1, "second"},
			want{1, true, "first", knowledge.ErrNotFound},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ledger, _ := newTestLedger(t, t.TempDir(), now)
			require.NoError(t, testkit.Err(ledger.Import(ctx, tc.args.records)))
			_, err := ledger.Reaffirm(ctx, tc.args.id, tc.args.version, tc.args.approver)
			assert.ErrorIs(t, err, tc.want.err)
			history, err := ledger.History(ctx, "item")
			require.NoError(t, err)
			require.Len(t, history, tc.want.records)
			newest, imported := history[0], tc.args.records[len(tc.args.records)-1]
			assert.Equal(t, tc.want.stale, newest.Stale(now))
			assert.Equal(t, tc.want.approver, newest.Approver)
			assert.Equal(t, imported.Content, newest.Content)
			assert.Equal(t, imported.ApprovedAt, newest.ApprovedAt)
		})
	}
}

// Another write lands between the read of the reaffirm and its append
func TestLedgerReaffirmConcurrentWrite(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	item := knowledge.Knowledge{
		ID: "item", Version: 1, Kind: knowledge.KindMeaning, Content: "units",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusApproved, Author: "author", Approver: "first", Time: now,
	}
	retired := item
	retired.Status, retired.Approver = knowledge.StatusRetired, "other"
	reaffirmed := item
	reaffirmed.Approver, reaffirmed.ReviewedAt = "other", now
	type want struct {
		approved int
		status   knowledge.Status
		approver string
	}
	tcs := []struct {
		name string
		// The record the other write appends
		args knowledge.Knowledge
		want want
	}{
		{"a concurrent retire keeps the version closed", retired, want{0, knowledge.StatusRetired, "other"}},
		{"a concurrent reaffirm by another person stands", reaffirmed, want{1, knowledge.StatusApproved, "other"}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			store, err := knowledgefile.New(dir)
			require.NoError(t, err)
			other, err := knowledgefile.New(dir)
			require.NoError(t, err)
			sink := vetofile.NewApprovedFile(t.TempDir(), dir)
			id := func(prefix string) string { return prefix + "item" }
			first := knowledge.NewLedger(store, sink, func() time.Time { return now }, id)
			require.NoError(t, testkit.Err(first.Import(ctx, []knowledge.Knowledge{item})))
			read, landed := make(chan struct{}), make(chan struct{})
			second := knowledge.NewLedger(other, sink, func() time.Time {
				close(read)
				<-landed
				return now.Add(time.Second)
			}, id)
			result := make(chan error, 1)
			go func() {
				_, err := second.Reaffirm(ctx, "item", 1, "approver")
				result <- err
			}()
			<-read
			err = testkit.Err(first.Import(ctx, []knowledge.Knowledge{tc.args}))
			close(landed)
			require.NoError(t, err)

			reaffirmErr := <-result
			all, err := first.All(ctx)
			require.NoError(t, err)

			assert.ErrorIs(t, reaffirmErr, knowledge.ErrRecordsChanged)
			assert.Len(t, all.Approved(), tc.want.approved)
			assert.Len(t, all, 2)
			assert.Equal(t, tc.want.status, all[0].Status)
			assert.Equal(t, tc.want.approver, all[0].Approver)
		})
	}
}

func TestLedgerNarrow(t *testing.T) {
	at := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	base := knowledge.Knowledge{
		ID: "item", Version: 1, Kind: knowledge.KindMeaning, Content: "lag",
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"f1"}}, Basis: knowledge.BasisVerified,
		Status: knowledge.StatusApproved, Author: "author", Approver: "ann", ApprovedAt: at, Time: at,
	}
	scoped := base
	scoped.Scope = knowledge.Scope{Scope: evidence.Scope{
		ChangeContexts: []evidence.Context{evidence.ContextPlannedChange, evidence.ContextUnknown},
	}}
	unscoped := base
	unscoped.Exceptions = []evidence.Context{evidence.ContextMeasurementChanged}
	nearlyAll := base
	nearlyAll.Exceptions = []evidence.Context{
		evidence.ContextNoKnownChange, evidence.ContextMeasurementChanged, evidence.ContextDataAvailability,
		evidence.ContextUnknown,
	}
	candidate := base
	candidate.Status, candidate.Approver, candidate.ApprovedAt = knowledge.StatusCandidate, "", time.Time{}
	compacted := scoped
	compacted.Compaction, compacted.CompactionSize = "c-1", 1
	compacted.ReviewedAt = at
	planned := []evidence.Context{evidence.ContextPlannedChange}
	type args struct {
		item     knowledge.Knowledge
		version  int
		contexts []evidence.Context
	}
	// The fields a narrowing sets or clears on the candidate
	type candidateView struct {
		version    int
		status     knowledge.Status
		author     string
		contexts   []evidence.Context
		exceptions []evidence.Context
		outcomes   []string
		feedback   []string
		compaction string
		reviewedAt time.Time
	}
	type want struct {
		candidate candidateView
		// Records of the id after the narrowing
		records int
		// The version an approval of version 2 supersedes
		supersedes int
		err        error
	}
	narrowed := func(contexts, exceptions []evidence.Context) candidateView {
		return candidateView{
			version: 2, status: knowledge.StatusCandidate, author: "jed", contexts: contexts, exceptions: exceptions,
			outcomes: []string{"r2", "r1"}, feedback: []string{"f1"},
		}
	}
	unknown := []evidence.Context{evidence.ContextUnknown}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a scoped item drops the refuted context", args{scoped, 1, planned}, want{narrowed(unknown, nil), 2, 1, nil}},
		{
			"an unscoped item takes the refuted context as an exception",
			args{unscoped, 1, planned},
			want{narrowed(nil, []evidence.Context{evidence.ContextMeasurementChanged, planned[0]}), 2, 1, nil},
		},
		{
			"an exception already held is not repeated",
			args{unscoped, 1, []evidence.Context{evidence.ContextMeasurementChanged}},
			want{narrowed(nil, []evidence.Context{evidence.ContextMeasurementChanged}), 2, 1, nil},
		},
		{
			"a compacted and reaffirmed version narrows as a plain proposal",
			args{compacted, 1, planned},
			want{narrowed(unknown, nil), 2, 1, nil},
		},
		{"no refuted context is refused", args{scoped, 1, nil}, want{records: 1, err: knowledge.ErrNarrowInvalid}},
		{
			"a scope left empty is refused",
			args{scoped, 1, []evidence.Context{evidence.ContextPlannedChange, evidence.ContextUnknown}},
			want{records: 1, err: knowledge.ErrNarrowExhausted},
		},
		{
			"exceptions over every context are refused",
			args{nearlyAll, 1, planned},
			want{records: 1, err: knowledge.ErrNarrowExhausted},
		},
		{
			"a version that is not approved is refused",
			args{candidate, 1, planned},
			want{records: 1, err: knowledge.ErrVersionUnapproved},
		},
		{
			"a version that is not current is refused",
			args{scoped, 2, planned},
			want{records: 1, err: knowledge.ErrVersionUnapproved},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ledger, _ := newTestLedger(t, t.TempDir(), at.Add(time.Hour))
			require.NoError(t, testkit.Err(ledger.Import(ctx, []knowledge.Knowledge{tc.args.item})))

			got, _, err := ledger.Narrow(ctx, "item", tc.args.version, tc.args.contexts, []string{"r2", "r1"}, "jed")
			history, historyErr := ledger.History(ctx, "item")
			require.NoError(t, historyErr)
			approved, _ := ledger.Approve(ctx, "item", 2, "ann")

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.candidate, candidateView{
				version: got.Version, status: got.Status, author: got.Author, contexts: got.Scope.ChangeContexts,
				exceptions: got.Exceptions, outcomes: got.Evidence.OutcomeTraceIDs, feedback: got.Evidence.FeedbackTraceIDs,
				compaction: got.Compaction, reviewedAt: got.ReviewedAt,
			})
			assert.Len(t, history, tc.want.records)
			assert.Equal(t, tc.want.supersedes, approved.Supersedes)
		})
	}
}
