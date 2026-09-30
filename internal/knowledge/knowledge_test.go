package knowledge_test

import (
	"context"
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

func TestKnowledgeValidate(t *testing.T) {
	t.Parallel()
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
	baseBelow := valid
	baseBelow.Version, baseBelow.Base = 2, 1
	baseAtVersion := valid
	baseAtVersion.Base = 1
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
	sed := knowledge.Veto{
		Tool:    "Bash",
		When:    []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`}},
		Example: map[string]any{"command": "sed -i s/a/b/ f"},
	}
	judgment := valid
	judgment.Kind, judgment.Content, judgment.Veto = knowledge.KindJudgment, "never edit files with sed -i", &sed
	meaningVeto := valid
	meaningVeto.Veto = &sed
	badPattern := judgment
	badPattern.Veto = &knowledge.Veto{
		Tool: "Bash", When: []knowledge.VetoCondition{{Field: "command", Match: "("}}, Example: sed.Example,
	}
	noTool := judgment
	noTool.Veto = &knowledge.Veto{When: sed.When, Example: sed.Example}
	missedExample := judgment
	missedExample.Veto = &knowledge.Veto{Tool: "Bash", When: sed.When, Example: map[string]any{"command": "ls"}}
	otherTool := judgment
	otherTool.Veto = &knowledge.Veto{Tool: "Edit|Bash", When: sed.When, Example: sed.Example}
	toolVeto := func(tool string) knowledge.Knowledge {
		k := judgment
		k.Veto = &knowledge.Veto{Tool: tool, When: sed.When, Example: sed.Example}
		return k
	}
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
		{"a base below the version is valid", baseBelow, nil},
		{"a base at the version fails", baseAtVersion, knowledge.ErrVersionInvalid},
		{"unknown kind fails", unknownKind, knowledge.ErrKindUnknown},
		{"empty content fails", noContent, knowledge.ErrContentRequired},
		{"empty evidence fails", noEvidence, knowledge.ErrEvidenceRequired},
		{"unknown basis fails", unknownBasis, knowledge.ErrBasisUnknown},
		{"unknown status fails", unknownStatus, knowledge.ErrStatusUnknown},
		{"approved without an approver fails", unapproved, knowledge.ErrApproverRequired},
		{"retired without an approver fails", unretired, knowledge.ErrApproverRequired},
		{"missing author fails", noAuthor, knowledge.ErrAuthorRequired},
		{"judgment with a veto that blocks its example is valid", judgment, nil},
		{"veto on any tool of a list blocks its example", otherTool, nil},
		{"veto on a meaning fails", meaningVeto, knowledge.ErrVetoKind},
		{"veto with a broken pattern fails", badPattern, knowledge.ErrVetoInvalid},
		{"veto without a tool fails", noTool, knowledge.ErrVetoInvalid},
		{"veto that lets its example through fails", missedExample, knowledge.ErrVetoExample},
		{"veto on an mcp tool is valid", toolVeto("mcp__srv__do"), nil},
		{"veto on a lower case tool fails", toolVeto("bash"), veto.ErrToolUnknown},
		{"veto on a permission rule fails", toolVeto("Bash(sed -i:*)"), veto.ErrToolUnknown},
		{"veto with one unknown tool in its list fails", toolVeto("Edit|write"), knowledge.ErrVetoInvalid},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := file.New(t.TempDir())
			require.NoError(t, err)
			l := knowledge.NewLedger(
				store, vetofile.NewApprovedFile(t.TempDir(), "records"),
				func() time.Time { return at }, func(prefix string) string { return prefix + "new" },
			)
			assert.ErrorIs(t, testkit.Err(l.Import(ctx, []knowledge.Knowledge{tc.args})), tc.want)
		})
	}
}

func TestKnowledgeFilled(t *testing.T) {
	filled := knowledge.Knowledge{
		Scope: knowledge.Scope{Scope: evidence.Scope{
			ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}, Metrics: []string{"click_count"},
		}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}},
		Basis:    knowledge.BasisStated,
	}
	tcs := []struct {
		name string
		args knowledge.Knowledge
		want knowledge.Knowledge
	}{
		{"an empty draft takes every filled field", knowledge.Knowledge{Content: "c"}, func() knowledge.Knowledge {
			k := filled
			k.Content = "c"
			return k
		}()},
		{
			"a set axis replaces the filled one and evidence adds up without repeats",
			knowledge.Knowledge{
				Scope: knowledge.Scope{
					Scope: evidence.Scope{Metrics: []string{"conversion_count"}}, Dims: map[string]string{"platform": "ios"},
				},
				Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1", "t2"}, ParagraphIDs: []string{"p#1"}},
				Basis:    knowledge.BasisVerified,
			},
			knowledge.Knowledge{
				Scope: knowledge.Scope{
					Scope: evidence.Scope{
						ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}, Metrics: []string{"conversion_count"},
					},
					Dims: map[string]string{"platform": "ios"},
				},
				Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1", "t2"}, ParagraphIDs: []string{"p#1"}},
				Basis:    knowledge.BasisVerified,
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Filled(filled.Scope, filled.Evidence, filled.Basis))
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
