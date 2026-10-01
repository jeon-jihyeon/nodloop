package knowledge_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestSetRules(t *testing.T) {
	approved := func(id string, kind knowledge.Kind, content string) knowledge.Knowledge {
		return knowledge.Knowledge{ID: id, Version: 2, Kind: kind, Content: content, Status: knowledge.StatusApproved, Approver: "ann"}
	}
	lag := approved("k-lag", knowledge.KindMeaning, "Conversions arrive up to 4 hours late.\nThe newest hours read low.")
	lag.Scope = knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}}
	lag.Exceptions = []evidence.Context{evidence.ContextMeasurementChanged}
	hold := approved("k-hold", knowledge.KindJudgment, "Hold while the tracking change is unconfirmed")
	sed := approved("k-sed", knowledge.KindJudgment, "Never edit files with sed -i")
	sed.Veto = &knowledge.Veto{Tool: "Bash"}
	candidate := approved("k-draft", knowledge.KindMeaning, "not approved yet")
	candidate.Status, candidate.Approver = knowledge.StatusCandidate, ""
	tcs := []struct {
		name string
		args knowledge.Set
		want string
	}{
		{"no approved item renders nothing", knowledge.Set{candidate}, ""},
		{
			"meaning leads judgment and each item is one line in id order",
			knowledge.Set{sed, lag, hold, candidate},
			"\n## Meaning\n\n" +
				"- k-lag v2: Conversions arrive up to 4 hours late. The newest hours read low. " +
				"Scope: metrics conversion_count. except measurement_context_changed. Approved by ann\n" +
				"\n## Judgment\n\n" +
				"- k-hold v2: Hold while the tracking change is unconfirmed. Scope: any event. Approved by ann\n" +
				"- k-sed v2: Never edit files with sed -i. Scope: any event. Approved by ann. The guard blocks the call it forbids\n",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Rules())
		})
	}
}

// Every change of the approved set or its approvers rewrites the rules file a CLAUDE.md imports
func TestLedgerExportRules(t *testing.T) {
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	draft := knowledge.Knowledge{
		ID: "k-lag", Kind: knowledge.KindMeaning, Content: "conversions lag clicks", Author: "author",
		Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}},
	}
	type step func(ctx context.Context, l *knowledge.Ledger) error
	approve := func(ctx context.Context, l *knowledge.Ledger) error {
		return testkit.Err(l.Approve(ctx, "k-lag", 1, "ann"))
	}
	retire := func(ctx context.Context, l *knowledge.Ledger) error {
		return testkit.Err(l.Retire(ctx, "k-lag", 1, "bo"))
	}
	reaffirm := func(ctx context.Context, l *knowledge.Ledger) error {
		return testkit.Err(l.Reaffirm(ctx, "k-lag", 1, "cy"))
	}
	line := "- k-lag v1: conversions lag clicks. Scope: any event. Approved by "
	tcs := []struct {
		name string
		args []step
		// The rules file after the steps without its header and none when empty
		want string
	}{
		{"a proposal alone writes no file", nil, ""},
		{"an approval lists the item", []step{approve}, "\n## Meaning\n\n" + line + "ann\n"},
		{"a reaffirm names the new approver", []step{approve, reaffirm}, "\n## Meaning\n\n" + line + "cy\n"},
		{"a retire leaves the header alone", []step{approve, retire}, "\n"},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			l, _ := newTestLedger(t, dir, at)
			_, _, err := l.Propose(ctx, draft)
			require.NoError(t, err)
			for _, s := range tc.args {
				require.NoError(t, s(ctx, l))
			}

			got, err := os.ReadFile(file.RulesPath(dir))

			if tc.want == "" {
				assert.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			require.NoError(t, err)
			_, body, _ := strings.Cut(string(got), "It never overrides an observation\n")
			assert.Equal(t, tc.want, "\n"+strings.TrimPrefix(body, "\n"))
		})
	}
}

// A rules file that cannot be written fails the approval with ErrExport after the approval landed
func TestLedgerExportRulesFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	l, _ := newTestLedger(t, dir, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	_, _, err := l.Propose(ctx, knowledge.Knowledge{
		ID: "k-lag", Kind: knowledge.KindMeaning, Content: "lag", Author: "author", Evidence: knowledge.Evidence{ParagraphIDs: []string{"p#1"}},
	})
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(file.RulesPath(dir), 0o700))

	k, err := l.Approve(ctx, "k-lag", 1, "ann")

	assert.ErrorIs(t, err, knowledge.ErrExport)
	assert.ErrorIs(t, err, file.ErrWrite)
	assert.Equal(t, knowledge.StatusApproved, k.Status)
}
