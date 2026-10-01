package compact_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Reviews and verdicts and approved items of one conversion folder
// Traces t1 to t5 review events e1 and e2 and tq-001 and e4 and only tq-001 has a label in the demo data
// 1. e1 has t1 rejected and the newer t2 edited to no_action
// 2. e2 has t3 approved as hold
// 3. tq-001 has t4 rejected and the label no_action
// 4. e4 has t5 with an outcome and no verdict
// Items a and b cite them while p cites a paragraph only and far cites t1 alone in the folder of another change context
func seed(t *testing.T, s testkit.Stores) {
	t.Helper()
	ctx := context.Background()
	at := s.Clock.Now()
	for i, tr := range []trace.Trace{
		{ID: "t1", Subject: "e1", Output: json.RawMessage(`{"status":"ready_for_review"}`)},
		{ID: "t2", Subject: "e1", Output: json.RawMessage(`{"status":"hold"}`)},
		{ID: "t3", Subject: "e2", Output: json.RawMessage(`{"status":"hold"}`)},
		{ID: "t4", Subject: "tq-001", Output: json.RawMessage(`{"status":"hold"}`)},
		{ID: "t5", Subject: "e4", Output: json.RawMessage(`{"status":"hold"}`)},
	} {
		tr.Name, tr.Time = trace.NameDiagnose, at.Add(time.Duration(i)*time.Minute)
		require.NoError(t, s.Traces.Append(ctx, tr))
	}
	for _, v := range []struct {
		trace   string
		verdict feedback.Verdict
		edited  json.RawMessage
	}{
		{"t1", feedback.VerdictReject, nil}, {"t2", feedback.VerdictEdit, json.RawMessage(`{"status":"no_action"}`)},
		{"t3", feedback.VerdictApprove, nil}, {"t4", feedback.VerdictReject, nil},
	} {
		fb, err := feedback.New(v.trace, v.verdict, "", "reason of "+v.trace, v.edited, "", s.Clock.Now())
		require.NoError(t, err)
		require.NoError(t, s.Feedback.Append(ctx, fb))
	}
	require.NoError(t, testkit.Err(s.Ledger.Import(ctx, seedItems(at))))
}

func seedItems(at time.Time) []knowledge.Knowledge {
	conversions := knowledge.Scope{Scope: evidence.Scope{
		ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange}, Metrics: []string{"conversion_count"},
	}}
	base := knowledge.Knowledge{
		Version: 1, Kind: knowledge.KindMeaning, Scope: conversions, Basis: knowledge.BasisStated,
		Status: knowledge.StatusApproved, Approver: "ann", ApprovedAt: at, Author: "author", Time: at,
	}
	a := base
	a.ID, a.Content, a.Evidence = "a", "lag", knowledge.Evidence{FeedbackTraceIDs: []string{"t1", "t2"}}
	b := base
	b.ID, b.Content = "b", "basis"
	b.Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{"t3", "t4"}, OutcomeTraceIDs: []string{"t5"}}
	p := base
	p.ID, p.Content, p.Evidence = "p", "paragraph only", knowledge.Evidence{ParagraphIDs: []string{"p#1"}}
	far := base
	far.ID, far.Content, far.Evidence = "far", "clicks", knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}
	far.Scope = knowledge.Scope{Scope: evidence.Scope{
		ChangeContexts: []evidence.Context{evidence.ContextMeasurementChanged}, Metrics: []string{"click_count"},
	}}
	return []knowledge.Knowledge{a, b, p, far}
}

var merged = compact.Draft{Items: []compact.Item{{
	Kind: knowledge.KindMeaning, Content: "lag and basis", ChangeContexts: []evidence.Context{evidence.ContextNoKnownChange},
	Metrics: []string{"conversion_count"}, From: []string{"a", "b"},
}}}

func TestFolder(t *testing.T) {
	type want struct {
		items, excluded []string
		corrections     []compact.Correction
		replay          []compact.Expectation
		unverifiable    []string
		// A line the drafter reads
		line string
		err  error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			"expectations come from a label, an edit and an approval and the rest is unverifiable",
			"a",
			want{
				items: []string{"a", "b"}, excluded: []string{"p"},
				corrections: []compact.Correction{
					{TraceID: "t1", EventID: "e1", Verdict: feedback.VerdictReject, Reason: "reason of t1"},
					{TraceID: "t2", EventID: "e1", Verdict: feedback.VerdictEdit, Reason: "reason of t2"},
					{TraceID: "t3", EventID: "e2", Verdict: feedback.VerdictApprove, Reason: "reason of t3"},
					{TraceID: "t4", EventID: "tq-001", Verdict: feedback.VerdictReject, Reason: "reason of t4"},
				},
				replay: []compact.Expectation{
					{EventID: "e1", Expected: evidence.StatusNoAction, Origin: compact.OriginEdit, TraceID: "t2"},
					{EventID: "e2", Expected: evidence.StatusHold, Origin: compact.OriginApproval, TraceID: "t3"},
					{EventID: "tq-001", Expected: evidence.StatusNoAction, Origin: compact.OriginLabel, TraceID: "t4"},
				},
				unverifiable: []string{"e4"},
				line:         "- e1 expects no_action from the edit",
			},
		},
		{"an anchor that cites only paragraphs is refused", "p", want{err: knowledge.ErrParagraphOnly}},
		{"an anchor that is not approved is not found", "none", want{err: knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			seed(t, s)

			got, err := compact.New(s.Source, s.Ledger, s.Traces, s.Feedback, s.Outcomes, s.Replays).Folder(ctx, tc.args)
			view := want{
				corrections: got.Corrections, replay: got.Replay, unverifiable: got.Unverifiable,
				line: tc.want.line, err: tc.want.err,
			}
			for _, k := range got.Items {
				view.items = append(view.items, k.ID)
			}
			for _, k := range got.Excluded {
				view.excluded = append(view.excluded, k.ID)
			}

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want, view)
			assert.Contains(t, got.String(), tc.want.line)
		})
	}
}

func TestPropose(t *testing.T) {
	type args struct {
		// Proposed on the anchor a before the draft
		first  []compact.Draft
		anchor string
		draft  compact.Draft
	}
	type want struct {
		// id and author of every proposed item
		items  []string
		events []string
		err    error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a merged item is proposed with the replay events",
			args{nil, "a", merged},
			want{[]string{"k-generated claude"}, []string{"e1", "e2", "tq-001"}, nil},
		},
		{
			"an item that names an excluded item is refused",
			args{nil, "a", compact.Draft{Items: []compact.Item{
				{Kind: knowledge.KindMeaning, Content: "all", From: []string{"a", "b", "p"}},
			}}},
			want{err: knowledge.ErrParagraphOnly},
		},
		{
			"the code checks of the ledger surface with their sentinels",
			args{nil, "a", compact.Draft{Items: []compact.Item{
				{
					Kind: knowledge.KindMeaning, Content: "lag", ChangeContexts: merged.Items[0].ChangeContexts,
					Metrics: merged.Items[0].Metrics, From: []string{"a"},
				},
				{
					ID: "b", Kind: knowledge.KindMeaning, Content: "basis", ChangeContexts: merged.Items[0].ChangeContexts,
					Metrics: merged.Items[0].Metrics, From: []string{"b"},
				},
			}}},
			want{err: knowledge.ErrCompactionOverlap},
		},
		{
			"an item wider than the items it names is refused",
			args{nil, "a", compact.Draft{Items: []compact.Item{
				{Kind: knowledge.KindMeaning, Content: "lag and basis", Metrics: []string{"conversion_count"}, From: []string{"a", "b"}},
			}}},
			want{err: knowledge.ErrCompactionInvalid},
		},
		{
			"an item scoped to a dim value no event carries is refused",
			args{nil, "a", compact.Draft{Items: []compact.Item{{
				Kind: knowledge.KindMeaning, Content: "lag and basis", ChangeContexts: merged.Items[0].ChangeContexts,
				Metrics: merged.Items[0].Metrics, Dims: map[string]string{"source": "nowhere"}, From: []string{"a", "b"},
			}}}},
			want{err: knowledge.ErrScopeUnobserved},
		},
		{"a folder without an expected status is refused", args{nil, "far", merged}, want{err: compact.ErrNothingToReplay}},
		{"an anchor that cites only paragraphs is refused", args{nil, "p", merged}, want{err: knowledge.ErrParagraphOnly}},
		{
			"a folder with a pending compaction is refused",
			args{[]compact.Draft{merged}, "b", merged},
			want{err: knowledge.ErrCompactionPending},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			seed(t, s)
			c := compact.New(s.Source, s.Ledger, s.Traces, s.Feedback, s.Outcomes, s.Replays)
			for _, d := range tc.args.first {
				_, _, err := c.Propose(ctx, "a", d, "")
				require.NoError(t, err)
			}

			got, expected, err := c.Propose(ctx, tc.args.anchor, tc.args.draft, "")
			var items, events []string
			for _, k := range got.Items {
				items = append(items, k.ID+" "+k.Author)
			}
			for _, e := range expected {
				events = append(events, e.EventID)
			}

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.items, items)
			assert.Equal(t, tc.want.events, events)
		})
	}
}

func TestDraft(t *testing.T) {
	type args struct {
		// The folder the draft compacts
		anchor string
		// Proposed on the anchor a before the folder is read
		first []compact.Draft
		// Model answers in call order
		outputs []string
		// Model error of every call
		err error
	}
	type want struct {
		items []string
		calls int
		// Calls whose prompt carries the refusal of the draft before
		resent int
		err    error
	}
	scope := `"change_contexts":["no_known_change"],"metrics":["conversion_count"]`
	answer := `{"items":[{"kind":"meaning","content":"lag and basis",` + scope + `,"from":["a","b"]}]}`
	empty := `{"items":[{"kind":"meaning","content":"",` + scope + `,"from":["a","b"]}]}`
	overlap := `{"items":[{"id":"a","kind":"meaning","content":"lag",` + scope + `,"from":["a"]},` +
		`{"id":"b","kind":"meaning","content":"basis",` + scope + `,"from":["b"]}]}`
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a valid draft is proposed after one call",
			args{anchor: "a", outputs: []string{answer}},
			want{[]string{"k-generated"}, 1, 0, nil},
		},
		{
			"a refused draft is sent back once and the fix is proposed",
			args{anchor: "a", outputs: []string{overlap, answer}},
			want{[]string{"k-generated"}, 2, 1, nil},
		},
		{
			"a draft refused for its content is sent back once and the fix is proposed",
			args{anchor: "a", outputs: []string{empty, answer}},
			want{[]string{"k-generated"}, 2, 1, nil},
		},
		{
			"a second refusal is returned as it is",
			args{anchor: "a", outputs: []string{overlap, overlap}},
			want{nil, 2, 1, knowledge.ErrCompactionOverlap},
		},
		{"output outside the schema fails", args{anchor: "a", outputs: []string{`[]`}}, want{nil, 1, 0, compact.ErrDraftInvalid}},
		{
			"a sent back output outside the schema fails",
			args{anchor: "a", outputs: []string{overlap, `[]`}},
			want{nil, 2, 1, compact.ErrDraftInvalid},
		},
		{"a model failure is returned", args{anchor: "a", outputs: []string{""}, err: assert.AnError}, want{nil, 1, 0, assert.AnError}},
		{
			"a folder with a pending compaction is refused before the model call",
			args{anchor: "a", first: []compact.Draft{merged}},
			want{nil, 0, 0, knowledge.ErrCompactionPending},
		},
		{
			"a folder without an expected status is refused before the model call",
			args{anchor: "far"},
			want{nil, 0, 0, compact.ErrNothingToReplay},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			seed(t, s)
			c := compact.New(s.Source, s.Ledger, s.Traces, s.Feedback, s.Outcomes, s.Replays)
			for _, d := range tc.args.first {
				_, _, err := c.Propose(ctx, "a", d, "")
				require.NoError(t, err)
			}
			folder, err := c.Folder(ctx, tc.args.anchor)
			require.NoError(t, err)
			var prompts []string
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					assert.Equal(t, compact.Rules, req.System)
					assert.JSONEq(t, compact.Schema, string(req.Schema))
					prompts = append(prompts, req.Prompt)
					return llm.Response{Output: json.RawMessage(tc.args.outputs[len(prompts)-1])}, tc.args.err
				}).Times(tc.want.calls)

			got, err := c.Draft(ctx, client, folder, "haiku", "jed")
			var items []string
			for _, k := range got.Items {
				items = append(items, k.ID)
			}
			sent := strings.Join(prompts, "\n")

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.items, items)
			assert.Equal(t, tc.want.calls, strings.Count(sent, "[a v1 meaning] lag"))
			assert.Equal(t, tc.want.resent, strings.Count(sent, "## Refused"))
		})
	}
}

// The demo source gives tq-001 no_action and tq-017 hold from their labels
// A compaction of a and b whose evidence reviews those two events replays them through a diagnoser over the preview
func TestReplay(t *testing.T) {
	type args struct {
		// Status the model answers per event in the first replay and in a rerun of chosen events
		first, rerun map[string]string
		// Empty reruns every replay event
		events []string
	}
	type want struct {
		passed bool
		// The refusal of an approval after the first replay
		refusal  string
		after    bool
		rerunErr error
		// Statuses of the new items after the last approval
		statuses   []knowledge.Status
		approveErr error
	}
	right := map[string]string{"tq-001": "no_action", "tq-017": "hold"}
	wrong := map[string]string{"tq-001": "no_action", "tq-017": "no_action"}
	missed := "tq-017 expects hold and got no_action"
	approved := []knowledge.Status{knowledge.StatusApproved}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a replay that reaches every expectation passes and approves",
			args{right, right, nil},
			want{passed: true, refusal: "<nil>", after: true, statuses: approved},
		},
		{
			"a missed expectation blocks approval until a rerun of that event fixes it",
			args{wrong, map[string]string{"tq-017": "hold"}, []string{"tq-017"}},
			want{refusal: missed, after: true, statuses: approved},
		},
		{
			"a rerun that misses again keeps approval refused",
			args{wrong, map[string]string{"tq-017": "no_action"}, []string{"tq-017"}},
			want{refusal: missed, approveErr: knowledge.ErrReplayNotPassed},
		},
		{
			"an event outside the replay is refused",
			args{right, right, []string{"tq-002"}},
			want{passed: true, refusal: "<nil>", rerunErr: compact.ErrEventOutsideReplay, statuses: approved},
		},
	}
	eventRe := regexp.MustCompile(`# Event (tq-\d+)\n`)
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			at := s.Clock.Now()
			for _, tr := range []trace.Trace{{ID: "t1", Subject: "tq-001"}, {ID: "t2", Subject: "tq-017"}} {
				tr.Name, tr.Time, tr.Output = trace.NameDiagnose, at, json.RawMessage(`{"status":"hold"}`)
				require.NoError(t, s.Traces.Append(ctx, tr))
			}
			items := seedItems(at)[:2]
			items[0].Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{"t1"}}
			items[1].Evidence = knowledge.Evidence{FeedbackTraceIDs: []string{"t2"}}
			require.NoError(t, testkit.Err(s.Ledger.Import(ctx, items)))
			c := compact.New(s.Source, s.Ledger, s.Traces, s.Feedback, s.Outcomes, s.Replays)
			proposed, _, err := c.Propose(ctx, "a", merged, "")
			require.NoError(t, err)
			answers := tc.args.first
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, req llm.Request) (llm.Response, error) {
					status := answers[eventRe.FindStringSubmatch(req.Prompt)[1]]
					return llm.Response{Output: json.RawMessage(`{"status":"` + status + `","observations":[],"causes":[],` +
						`"checks":[],"open_questions":[],"hold_reasons":["gap"]}`)}, nil
				}).AnyTimes()
			preview, err := s.Ledger.Preview(ctx, proposed.ID)
			require.NoError(t, err)
			d := diagnose.New(s.Source, testkit.Policy(t), client, s.Replays, s.Feedback, preview, s.Clock.Now)

			first, err := c.Replay(ctx, d, proposed.ID, compact.ReplayOptions{Parallel: 1})
			require.NoError(t, err)
			_, refused := c.Approve(ctx, proposed.ID, "jed")
			answers = tc.args.rerun
			after, rerunErr := c.Replay(ctx, d, proposed.ID, compact.ReplayOptions{Events: tc.args.events, Parallel: 1})
			got, approveErr := c.Approve(ctx, proposed.ID, "jed")
			reviews, err := s.Traces.List(ctx, trace.Filter{SessionID: proposed.ID})
			require.NoError(t, err)
			replayed, err := s.Replays.List(ctx, trace.Filter{
				Name: trace.NameDiagnose, SessionID: proposed.ID, Tags: []string{compact.TagReplay},
			})
			require.NoError(t, err)
			var statuses []knowledge.Status
			for _, k := range got.Items {
				statuses = append(statuses, k.Status)
			}

			assert.Equal(t, tc.want.passed, first.Passed())
			assert.Contains(t, fmt.Sprint(refused), tc.want.refusal)
			assert.Equal(t, tc.want.after, after.Passed())
			assert.ErrorIs(t, rerunErr, tc.want.rerunErr)
			assert.Equal(t, tc.want.statuses, statuses)
			assert.ErrorIs(t, approveErr, tc.want.approveErr)
			assert.Empty(t, reviews, "traces.jsonl never sees a replay")
			assert.NotEmpty(t, replayed)
		})
	}
}

func TestResult(t *testing.T) {
	type want struct {
		events []knowledge.ReplayEvent
		err    error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			"a compaction never replayed has no status for any event",
			"c-generated",
			want{events: []knowledge.ReplayEvent{
				{EventID: "e1", Expected: evidence.StatusNoAction},
				{EventID: "e2", Expected: evidence.StatusHold},
				{EventID: "tq-001", Expected: evidence.StatusNoAction},
			}},
		},
		{"an unknown compaction is not found", "c-none", want{err: knowledge.ErrNotFound}},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			seed(t, s)
			c := compact.New(s.Source, s.Ledger, s.Traces, s.Feedback, s.Outcomes, s.Replays)
			_, _, err := c.Propose(ctx, "a", merged, "")
			require.NoError(t, err)

			got, err := c.Result(ctx, tc.args)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.events, got.Events)
		})
	}
}

// The approval of t3 on e2 stands until a real check refutes it
// A refuted cause does not prove the status wrong so e2 turns unverifiable and never gets another status
func TestFolderOutcomes(t *testing.T) {
	type step struct {
		// An outcome result or a verdict on t3 in the order they are recorded
		result  feedback.Result
		verdict feedback.Verdict
	}
	type want struct {
		replay       []string
		unverifiable []string
	}
	tcs := []struct {
		name string
		args []step
		want want
	}{
		{
			"a refuted outcome after the approval leaves e2 unverifiable",
			[]step{{result: feedback.ResultRefuted}},
			want{[]string{"e1", "tq-001"}, []string{"e2", "e4"}},
		},
		{
			"an approval again after the refutation restores the expectation",
			[]step{{result: feedback.ResultRefuted}, {verdict: feedback.VerdictApprove}},
			want{[]string{"e1", "e2", "tq-001"}, []string{"e4"}},
		},
		{
			"a confirmed outcome keeps the expectation",
			[]step{{result: feedback.ResultConfirmed}},
			want{[]string{"e1", "e2", "tq-001"}, []string{"e4"}},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			seed(t, s)
			for _, st := range tc.args {
				if st.verdict != "" {
					fb, err := feedback.New("t3", st.verdict, "", "approved again", nil, "", s.Clock.Now())
					require.NoError(t, err)
					require.NoError(t, s.Feedback.Append(ctx, fb))
					continue
				}
				o, err := feedback.NewOutcome("t3", st.result, "", "checked", "ann", s.Clock.Now())
				require.NoError(t, err)
				require.NoError(t, s.Outcomes.Append(ctx, o))
			}

			got, err := compact.New(s.Source, s.Ledger, s.Traces, s.Feedback, s.Outcomes, s.Replays).Folder(ctx, "a")
			require.NoError(t, err)
			var replay []string
			for _, e := range got.Replay {
				replay = append(replay, e.EventID)
			}

			assert.Equal(t, tc.want, want{replay: replay, unverifiable: got.Unverifiable})
		})
	}
}

// Due only when the folder is crowded and a replay could check something
func TestFolderDue(t *testing.T) {
	crowd := make(knowledge.Set, knowledge.FolderItems+1)
	// An unscoped anchor and one item per change context so each review carries two
	spread := knowledge.Set{{ID: "g"}}
	for _, c := range evidence.DefaultContexts().Names() {
		spread = append(spread, knowledge.Knowledge{
			ID: string(c), Scope: knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{c}}},
		})
	}
	expected := []compact.Expectation{{EventID: "e1", Expected: evidence.StatusHold, Origin: compact.OriginApproval}}
	type args struct {
		items  knowledge.Set
		replay []compact.Expectation
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"a crowded folder with an expected status is due", args{crowd, expected}, true},
		{"a crowded folder without any expected status is not due because the compaction would be refused", args{crowd, nil}, false},
		{"a folder of FolderItems items is not due", args{crowd[1:], expected}, false},
		{"a folder whose union passes FolderItems while each review carries two is not due", args{spread, expected}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := compact.Folder{Compactable: knowledge.Compactable{Items: tc.args.items, Contexts: evidence.DefaultContexts()}, Replay: tc.args.replay}
			assert.Equal(t, tc.want, f.Due())
		})
	}
}
