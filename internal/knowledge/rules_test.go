package knowledge_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestSetRules(t *testing.T) {
	approved := func(id string, kind knowledge.Kind, content string) knowledge.Knowledge {
		return knowledge.Knowledge{
			ID: id, Version: 2, Kind: kind, Content: content, Status: knowledge.StatusApproved, Approver: "ann",
			Run: &knowledge.RunScope{Producer: "session"},
		}
	}
	sign := approved("k-sign", knowledge.KindMeaning, "Commits here are signed.\nAn unsigned commit is rejected.")
	sign.Run = &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}, Except: trace.Labels{"task": {"docs", "draft"}}}
	hold := approved("k-hold", knowledge.KindJudgment, "Ask before a force push")
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
			knowledge.Set{sed, sign, hold, candidate},
			"\n## Meaning\n\n" +
				"- k-sign v2: Commits here are signed. An unsigned commit is rejected. " +
				"Scope: runs of session. repo=nodloop. except task=docs|draft. Approved by ann\n" +
				"\n## Judgment\n\n" +
				"- k-hold v2: Ask before a force push. Scope: runs of session. Approved by ann\n" +
				"- k-sed v2: Never edit files with sed -i. Scope: runs of session. Approved by ann. The guard blocks the call it forbids\n",
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
		ID: "k-lag", Kind: knowledge.KindMeaning, Content: "commits here are signed", Author: "author",
		Run:      &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}},
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
	line := "- k-lag v1: commits here are signed. Scope: runs of session. repo=nodloop. Approved by "
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
			_, body, _ := strings.Cut(string(got), "It never overrides what the user asks now\n")
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
		ID: "k-lag", Kind: knowledge.KindMeaning, Content: "lag", Author: "author", Evidence: knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}},
		Run: &knowledge.RunScope{Producer: "session"},
	})
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(file.RulesPath(dir), 0o700))

	k, err := l.Approve(ctx, "k-lag", 1, "ann")

	assert.ErrorIs(t, err, knowledge.ErrExport)
	assert.ErrorIs(t, err, file.ErrWrite)
	assert.Equal(t, knowledge.StatusApproved, k.Status)
}
