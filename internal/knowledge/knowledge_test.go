package knowledge_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	"github.com/jeon-jihyeon/nodloop/internal/veto"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestKnowledgeValidate(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	valid := knowledge.Knowledge{
		ID: "k1", Version: 1, Kind: knowledge.KindMeaning, Content: "commits in this repo are signed",
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}, Basis: knowledge.BasisStated,
		Status: knowledge.StatusCandidate, Author: "author", Time: at,
	}
	approved := valid
	approved.Status, approved.Approver = knowledge.StatusApproved, "sam"
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
	cued := func(kind knowledge.Kind, cue knowledge.Cue) knowledge.Knowledge {
		k := valid
		k.Kind, k.Content, k.Cue = kind, "keep a commit title to one line", &cue
		return k
	}
	commit := knowledge.Cue{Tool: "Bash", Command: "git commit"}
	cueAndVeto := judgment
	cueAndVeto.Cue = &commit
	timedCue := cued(knowledge.KindJudgment, commit)
	timedCue.Run = &knowledge.RunScope{
		Producer:   "session",
		Labels:     trace.Labels{"repo": {"nodloop"}},
		ValidUntil: at.Add(time.Hour),
	}
	scoped := func(run *knowledge.RunScope) knowledge.Knowledge {
		k := valid
		k.Run = run
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
		{"veto on the lower case tool of another agent passes", toolVeto("bash"), nil},
		{"veto on a permission rule fails", toolVeto("Bash(sed -i:*)"), veto.ErrToolMalformed},
		{"veto with one malformed tool in its list fails", toolVeto("Edit|Write(x)"), knowledge.ErrVetoInvalid},
		{"judgment with a cue is valid", cued(knowledge.KindJudgment, commit), nil},
		{"cue with a tool alone is valid", cued(knowledge.KindJudgment, knowledge.Cue{Tool: "mcp__acme__send"}), nil},
		{"cue with a file shape is valid", cued(knowledge.KindJudgment, knowledge.Cue{Tool: "Edit", File: "*_test.go"}), nil},
		{"cue on a meaning fails", cued(knowledge.KindMeaning, commit), knowledge.ErrCueKind},
		{"cue beside a veto fails", cueAndVeto, knowledge.ErrCueVeto},
		{"cue with an effective time fails", timedCue, knowledge.ErrCueVeto},
		{"cue without a tool fails", cued(knowledge.KindJudgment, knowledge.Cue{Command: "git commit"}),
			knowledge.ErrCueInvalid},
		{"cue with four words fails", cued(knowledge.KindJudgment, knowledge.Cue{Tool: "Bash", Command: "gh pr create now"}),
			knowledge.ErrCueInvalid},
		{"cue with a value fails", cued(knowledge.KindJudgment, knowledge.Cue{Tool: "Bash", Command: "git checkout ann-1"}),
			knowledge.ErrCueInvalid},
		{"cue with a file name fails", cued(knowledge.KindJudgment, knowledge.Cue{Tool: "Edit", File: "CHANGELOG.md"}),
			knowledge.ErrCueInvalid},
		{
			"run scope with labels and exceptions is valid",
			scoped(&knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}, Except: trace.Labels{"task": {"docs"}}}),
			nil,
		},
		{"missing run scope fails", scoped(nil), knowledge.ErrScopeRequired},
		{"run scope without a producer fails", scoped(&knowledge.RunScope{Labels: trace.Labels{"repo": {"nodloop"}}}), knowledge.ErrScopeInvalid},
		{"empty label value fails", scoped(&knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {""}}}), knowledge.ErrScopeInvalid},
		{"exception without a value fails", scoped(&knowledge.RunScope{Producer: "session", Except: trace.Labels{"task": nil}}), knowledge.ErrScopeInvalid},
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

func TestKnowledgeDecided(t *testing.T) {
	at := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		args knowledge.Knowledge
		want bool
	}{
		{"an approval by a person", knowledge.Knowledge{Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Time: at}, true},
		{
			"an approval nodloop carried out",
			knowledge.Knowledge{Status: knowledge.StatusApproved, Approver: knowledge.ApproverNodloop, ApprovedAt: at, Time: at},
			true,
		},
		{
			"a reaffirm keeps the time of the approval",
			knowledge.Knowledge{Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Time: at.Add(time.Hour)},
			false,
		},
		{"a retire", knowledge.Knowledge{Status: knowledge.StatusRetired, Approver: "ann", ApprovedAt: at, Time: at.Add(time.Hour)}, true},
		{"a candidate", knowledge.Knowledge{Status: knowledge.StatusCandidate, Time: at}, false},
		{"a superseded version", knowledge.Knowledge{Status: knowledge.StatusSuperseded, Approver: "ann", ApprovedAt: at, Time: at}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Decided())
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
