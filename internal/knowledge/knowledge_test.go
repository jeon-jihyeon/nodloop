package knowledge_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
)

func TestKnowledgeValidate(t *testing.T) {
	at := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	valid := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "clicks and conversions use different time bases",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := valid
	approved.Status, approved.Approver = knowledge.StatusApproved, "jed"
	outcomeOnly := valid
	outcomeOnly.Evidence = knowledge.Evidence{OutcomeTraceIDs: []string{"t1"}}
	noID := valid
	noID.ID = ""
	zeroVersion := valid
	zeroVersion.Version = 0
	unknownKind := valid
	unknownKind.Kind = "rule"
	noContent := valid
	noContent.Content = ""
	noEvidence := valid
	noEvidence.Evidence = knowledge.Evidence{}
	unknownBasis := valid
	unknownBasis.Basis = "guess"
	unknownStatus := valid
	unknownStatus.Status = "live"
	unapproved := valid
	unapproved.Status = knowledge.StatusApproved
	unretired := valid
	unretired.Status = knowledge.StatusRetired
	noAuthor := valid
	noAuthor.Author = ""
	tcs := []struct {
		name string
		args knowledge.Knowledge
		want error
	}{
		{"complete candidate is valid", valid, nil},
		{"approved with an approver is valid", approved, nil},
		{"outcome evidence alone is enough", outcomeOnly, nil},
		{"missing id fails", noID, knowledge.ErrIDRequired},
		{"zero version fails", zeroVersion, knowledge.ErrVersionInvalid},
		{"unknown kind fails", unknownKind, knowledge.ErrKindUnknown},
		{"empty content fails", noContent, knowledge.ErrContentRequired},
		{"empty evidence fails", noEvidence, knowledge.ErrEvidenceRequired},
		{"unknown basis fails", unknownBasis, knowledge.ErrBasisUnknown},
		{"unknown status fails", unknownStatus, knowledge.ErrStatusUnknown},
		{"approved without an approver fails", unapproved, knowledge.ErrApproverRequired},
		{"retired without an approver fails", unretired, knowledge.ErrApproverRequired},
		{"missing author fails", noAuthor, knowledge.ErrAuthorRequired},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(store, func() time.Time { return at }, func() string { return "k-new" })
			assert.ErrorIs(t, l.Import(ctx, []knowledge.Knowledge{tc.args}), tc.want)
		})
	}
}

func TestEvidenceTraceIDs(t *testing.T) {
	tcs := []struct {
		name string
		args knowledge.Evidence
		want []string
	}{
		{"no trace ids give nothing", knowledge.Evidence{ParagraphIDs: []string{"p#1"}}, nil},
		{
			"feedback ids come before outcome ids",
			knowledge.Evidence{FeedbackTraceIDs: []string{"f1", "f2"}, OutcomeTraceIDs: []string{"o1"}},
			[]string{"f1", "f2", "o1"},
		},
		{
			"paragraph ids are not trace ids",
			knowledge.Evidence{OutcomeTraceIDs: []string{"o1"}, ParagraphIDs: []string{"p#1"}},
			[]string{"o1"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.TraceIDs())
		})
	}
}
