package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Two approved items a and b of one conversion folder rest on rejected reviews of tq-001 and tq-017
// p of the same folder cites only a paragraph
// The demo labels expect no_action for tq-001 and hold for tq-017
func compactionStores(t *testing.T) testkit.Stores {
	t.Helper()
	ctx := context.Background()
	st := testkit.Open(t)
	at := st.Clock.Now()
	for _, tr := range []trace.Trace{{ID: "t1", Subject: "tq-001"}, {ID: "t2", Subject: "tq-017"}} {
		tr.Name, tr.Time, tr.Output = trace.NameDiagnose, at, json.RawMessage(`{"status":"hold"}`)
		require.NoError(t, st.Traces.Append(ctx, tr))
		fb, err := feedback.New(tr.ID, feedback.VerdictReject, "wrong", nil, "", at)
		require.NoError(t, err)
		require.NoError(t, st.Feedback.Append(ctx, fb))
	}
	base := knowledge.Knowledge{
		Version: 1, Kind: knowledge.KindMeaning, Basis: knowledge.BasisStated, Status: knowledge.StatusApproved,
		Approver: "ann", Author: "author", Time: at,
		Scope: knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}},
	}
	a, b, p := base, base, base
	a.ID, a.Content, a.Evidence = "a", "lag", knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}
	b.ID, b.Content, b.Evidence = "b", "basis", knowledge.Evidence{FeedbackTraceIDs: []string{"t2"}}
	p.ID, p.Content, p.Evidence = "p", "paragraph only", knowledge.Evidence{ParagraphIDs: []string{"p#1"}}
	require.NoError(t, testkit.Err(st.Ledger.Import(ctx, []knowledge.Knowledge{a, b, p})))
	return st
}

type call struct {
	tool  string
	input map[string]any
}

// The proposal that merges a and b into one item under the anchor
func mergeInto(anchor string) call {
	merged := map[string]any{"kind": "meaning", "content": "lag and basis", "metrics": []string{"conversion_count"}, "from": []string{"a", "b"}}
	return call{"propose_compaction", map[string]any{"anchor": anchor, "items": []any{merged}}}
}

func TestServerCompaction(t *testing.T) {
	type want struct {
		Anchor   string                `json:"anchor"`
		Items    []knowledge.Knowledge `json:"items"`
		Excluded []string              `json:"excluded"`
		Replay   []compact.Expectation `json:"replay"`
		Pending  string                `json:"pending"`
	}
	type args struct {
		// Calls that must succeed before the read
		before []call
		id     string
	}
	scope := knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversion_count"}}}
	a := knowledge.Knowledge{ID: "a", Version: 1, Kind: knowledge.KindMeaning, Content: "lag", Scope: scope}
	b := knowledge.Knowledge{ID: "b", Version: 1, Kind: knowledge.KindMeaning, Content: "basis", Scope: scope}
	lag := compact.Expectation{EventID: "tq-001", Expected: evidence.StatusNoAction, Origin: compact.OriginLabel, TraceID: "t1"}
	basis := compact.Expectation{EventID: "tq-017", Expected: evidence.StatusHold, Origin: compact.OriginLabel, TraceID: "t2"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the folder lists the anchor first with its replay and leaves out the paragraph only item",
			args{id: "a"},
			want{Anchor: "a", Items: []knowledge.Knowledge{a, b}, Excluded: []string{"p"}, Replay: []compact.Expectation{lag, basis}},
		},
		{
			"the folder of an item under a pending compaction names it",
			args{before: []call{mergeInto("a")}, id: "b"},
			want{
				Anchor: "b", Items: []knowledge.Knowledge{b, a}, Excluded: []string{"p"}, Replay: []compact.Expectation{basis, lag},
				Pending: "c-generated",
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := connect(t, compactionStores(t), "nodloop", "")
			for _, b := range tc.args.before {
				require.NoError(t, c.Run(t, b.tool, b.input), b.tool)
			}
			var got struct {
				want
				Rules string `json:"rules"`
			}

			require.NoError(t, c.Call(t, "compaction", map[string]any{"id": tc.args.id}, &got))

			assert.Equal(t, tc.want, got.want)
			assert.Equal(t, compact.Rules, got.Rules)
		})
	}
}

// Tool errors cross the transport as text so each case names the sentinel whose message the text carries
func TestServerCompactionRefusals(t *testing.T) {
	type args struct {
		// Calls that must succeed before the call under test
		before []call
		call   call
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{
			"a folder read from a paragraph only item is refused",
			args{call: call{"compaction", map[string]any{"id": "p"}}},
			knowledge.ErrParagraphOnly.Error(),
		},
		{
			"a draft that replaces a paragraph only item is refused",
			args{call: call{"propose_compaction", map[string]any{"anchor": "a", "items": []any{
				map[string]any{"kind": "meaning", "content": "all", "from": []string{"a", "b", "p"}},
			}}}},
			knowledge.ErrParagraphOnly.Error(),
		},
		{
			"a draft with two items of one kind in the folder is refused",
			args{call: call{"propose_compaction", map[string]any{"anchor": "a", "items": []any{
				map[string]any{"id": "a", "kind": "meaning", "content": "lag", "metrics": []string{"conversion_count"}, "from": []string{"a"}},
				map[string]any{"id": "b", "kind": "meaning", "content": "basis", "metrics": []string{"conversion_count"}, "from": []string{"b"}},
			}}}},
			knowledge.ErrCompactionOverlap.Error(),
		},
		{
			"a draft that drops the metrics of the items it names is refused",
			args{call: call{"propose_compaction", map[string]any{"anchor": "a", "items": []any{
				map[string]any{"kind": "meaning", "content": "lag and basis", "from": []string{"a", "b"}},
			}}}},
			"it carries the facts of a to events of change contexts no_known_change that a never reached",
		},
		{
			"a second proposal while one is pending is refused",
			args{before: []call{mergeInto("a")}, call: mergeInto("b")},
			knowledge.ErrCompactionPending.Error(),
		},
		{
			"a plain approval of a compaction candidate is refused",
			args{
				before: []call{mergeInto("a")},
				call:   call{"approve", map[string]any{"id": "k-generated", "version": 1, "approver": "jed"}},
			},
			knowledge.ErrCompactionInvalid.Error(),
		},
		{
			"an approval before the replay names the events without a replay",
			args{
				before: []call{mergeInto("a")},
				call:   call{"approve_compaction", map[string]any{"compaction": "c-generated", "approver": "jed"}},
			},
			"tq-001 expects no_action and has no replay, tq-017 expects hold and has no replay",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := connect(t, compactionStores(t), "nodloop", "")
			for _, b := range tc.args.before {
				require.NoError(t, c.Run(t, b.tool, b.input), b.tool)
			}

			err := c.Run(t, tc.args.call.tool, tc.args.call.input)

			assert.ErrorIs(t, err, testkit.ErrTool)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// The replay command names the binary and the directories the server was started with so the user can paste it
func TestServerProposeCompaction(t *testing.T) {
	type args struct {
		exe      string
		dataArgs string
	}
	type want struct {
		Compaction    string   `json:"compaction"`
		Replaced      []string `json:"replaced"`
		ReplayEvents  int      `json:"replay_events"`
		ReplayCommand string   `json:"replay_command"`
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"the command names the path of the binary",
			args{"/home/u/.nodloop/bin/nodloop", ""},
			want{"c-generated", []string{"a v1", "b v1"}, 2, "/home/u/.nodloop/bin/nodloop knowledge replay c-generated"},
		},
		{
			"the command names the fallback when the OS cannot tell the path",
			args{"nodloop", ""},
			want{"c-generated", []string{"a v1", "b v1"}, 2, "nodloop knowledge replay c-generated"},
		},
		{
			"the command ends with the directories of the server after the id",
			args{"nodloop", "--data-dir /d --record-dir '/r s'"},
			want{"c-generated", []string{"a v1", "b v1"}, 2, "nodloop knowledge replay c-generated --data-dir /d --record-dir '/r s'"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := connect(t, compactionStores(t), tc.args.exe, tc.args.dataArgs)
			merge := mergeInto("a")
			var got want

			require.NoError(t, c.Call(t, merge.tool, merge.input, &got))

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestServerApproveCompaction(t *testing.T) {
	type want struct {
		Compaction string   `json:"compaction"`
		Approver   string   `json:"approver"`
		Records    []string `json:"records"`
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			"a passing replay approves the new item and retires the old ones",
			"jed",
			want{"c-generated", "jed", []string{"k-generated v1 approved", "a v1 retired", "b v1 retired"}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := compactionStores(t)
			c := connect(t, st, "nodloop", "")
			merge := mergeInto("a")
			require.NoError(t, c.Run(t, merge.tool, merge.input))
			for _, tr := range []trace.Trace{
				{ID: "r1", Subject: "tq-001", Output: json.RawMessage(`{"status":"no_action"}`)},
				{ID: "r2", Subject: "tq-017", Output: json.RawMessage(`{"status":"hold"}`)},
			} {
				tr.Name, tr.SessionID, tr.Tags, tr.Time = trace.NameDiagnose, "c-generated", []string{compact.TagReplay}, st.Clock.Now()
				require.NoError(t, st.Replays.Append(ctx, tr))
			}
			var got want

			require.NoError(t, c.Call(t, "approve_compaction", map[string]any{"compaction": "c-generated", "approver": tc.args}, &got))

			assert.Equal(t, tc.want, got)
		})
	}
}
