package knowledge_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestKnowledgeRecord(t *testing.T) {
	at := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	full := knowledge.Knowledge{
		ID: "k1", Version: 2, Kind: knowledge.KindJudgment, Content: "never sed -i",
		Run: &knowledge.RunScope{
			Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}, Except: trace.Labels{"dir": {"tmp"}},
		},
		Evidence: knowledge.Evidence{
			FeedbackTraceIDs: []string{"t1"}, OutcomeTraceIDs: []string{"t2"}, ParagraphIDs: []string{"p"},
			Knowledge: []knowledge.Ref{{ID: "k0", Version: 1}},
		},
		Basis: knowledge.BasisVerified, Status: knowledge.StatusApproved, Approver: "sam", ApprovedAt: at,
		ReviewedAt: at.Add(time.Hour), Supersedes: 1, Base: 1, Time: at.Add(time.Hour), Author: "author", Drafted: true,
		NewLabels: true,
		Veto: &knowledge.Veto{
			Tool:    "Bash",
			When:    []knowledge.VetoCondition{{Field: "command", Match: `sed\s+-i`, Unless: "--dry"}},
			Example: map[string]any{"command": "sed -i s/a/b/ f"},
		},
		Compaction: "c-1", CompactionSize: 2, DeferredIn: "s1",
	}
	tcs := []struct {
		name string
		args knowledge.Knowledge
	}{
		{"every field", full},
		{
			"a legacy record without a run scope",
			knowledge.Knowledge{
				ID: "k2", Version: 1, Kind: knowledge.KindMeaning, Content: "c", Evidence: knowledge.Evidence{ParagraphIDs: []string{"p"}},
				Basis: knowledge.BasisStated, Status: knowledge.StatusCandidate, Time: at, Author: "x",
			},
		},
		{
			"a veto without conditions keeps its when null",
			knowledge.Knowledge{ID: "k3", Version: 1, Kind: knowledge.KindJudgment, Time: at, Veto: &knowledge.Veto{Tool: "Bash"}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want, err := json.Marshal(tc.args)
			require.NoError(t, err)
			got, err := json.Marshal(tc.args.Record())
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got), "the line encodes to the bytes of the version")
			assert.Equal(t, tc.args, knowledge.Restore(tc.args.Record()))
		})
	}
}

func TestSetRecordsKeepNil(t *testing.T) {
	assert.Nil(t, knowledge.RestoreAll(nil))
	assert.Nil(t, knowledge.Set(nil).Records())
}
