package knowledge_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
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
		Compaction: "c-1", CompactionSize: 2, Decision: "d1", Extract: "e1",
	}
	retired := full
	retired.Status, retired.Reason = knowledge.StatusRetired, knowledge.ReasonWords
	tcs := []struct {
		name string
		args knowledge.Knowledge
	}{
		{"every field", full},
		{"a retire with its reason", retired},
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
			model, err := json.Marshal(tc.args)
			require.NoError(t, err)
			want := fmt.Sprintf(`%s,"schema_version":%d}`, model[:len(model)-1], knowledge.SchemaVersion)
			got, err := json.Marshal(tc.args.Record())
			require.NoError(t, err)
			assert.Equal(t, want, string(got), "the line encodes to the bytes of the version and the version of its schema")
			assert.Equal(t, tc.args, knowledge.Restore(tc.args.Record()))
		})
	}
}

func TestSetRecordsKeepNil(t *testing.T) {
	assert.Nil(t, knowledge.RestoreAll(nil))
	assert.Nil(t, knowledge.Set(nil).Records())
}

func TestKnowledgeRecordTimed(t *testing.T) {
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	type args struct{ from, until time.Time }
	tcs := []struct {
		name string
		args args
	}{
		{"start only", args{at, time.Time{}}},
		{"end only", args{time.Time{}, at.Add(time.Hour)}},
		{"both bounds", args{at, at.Add(time.Hour)}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := runItem("acme", &knowledge.RunScope{
				Producer: "session", ValidFrom: tc.args.from, ValidUntil: tc.args.until,
			})
			r := k.Record()
			assert.Nil(t, r.Run)
			require.NotNil(t, r.ConditionalRun)
			assert.Equal(t, tc.args.from, r.ConditionalRun.ValidFrom)
			assert.Equal(t, tc.args.until, r.ConditionalRun.ValidUntil)
			assert.Equal(t, k, knowledge.Restore(r))
			encoded, err := json.Marshal(r)
			require.NoError(t, err)
			var legacy struct {
				Run *record.RunScope `json:"run"`
			}
			require.NoError(t, json.Unmarshal(encoded, &legacy))
			assert.Nil(t, legacy.Run)
		})
	}
}

func TestKnowledgeRestoreConditional(t *testing.T) {
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	plain := &record.RunScope{Producer: "acme"}
	timed := &record.RunScope{Producer: "acme", ValidFrom: at, ValidUntil: at.Add(time.Hour)}
	type args struct{ run, conditional *record.RunScope }
	tcs := []struct {
		name string
		args args
		want int
	}{
		{"legacy timeless", args{plain, nil}, 1},
		{"legacy timed input", args{timed, nil}, 1},
		{"conditional timed", args{nil, timed}, 1},
		{"ambiguous scopes", args{plain, timed}, 0},
		{"unbounded conditional", args{nil, plain}, 0},
		{"missing scope", args{nil, nil}, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := knowledge.Restore(record.Knowledge{ID: "acme", Version: 1, Status: "approved",
				Time: at, ApprovedAt: at, Run: tc.args.run, ConditionalRun: tc.args.conditional})
			assert.Len(t, (knowledge.Set{k}).ForAt("acme", nil, at), tc.want)
		})
	}
}
