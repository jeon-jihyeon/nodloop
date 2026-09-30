package diagnose_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/llm/llmmock"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The caps are constants so every row sizes its items against them
// Sizes count runes so a Korean or emoji item counts the same as an ASCII one of the same length
func TestCaps(t *testing.T) {
	type args struct {
		items int
		// Runes of content per item
		chars int
		unit  string
	}
	type sections struct {
		Knowledge int `json:"knowledge"`
		Examples  int `json:"examples"`
	}
	type want struct {
		candidates []string
		omitted    bool
		applied    []string
		// Runes of each applied item as sent
		chars []int
		// Runes left out of each applied item
		omittedChars []int
		// The select trace section sizes
		section, sectionOmitted sections
	}
	ids := func(n int) []string {
		var out []string
		for i := range n {
			out = append(out, fmt.Sprintf("k-%02d", i))
		}
		return out
	}
	repeat := func(v, n int) []int {
		var out []int
		for range n {
			out = append(out, v)
		}
		return out
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "the candidate cap offers ten items and marks the rest omitted",
			args: args{items: 11, chars: 10, unit: "x"},
			want: want{
				candidates: ids(10), omitted: true, applied: ids(10), chars: repeat(47, 10), omittedChars: repeat(0, 10),
				section: sections{Knowledge: knowledgeHeading + 470},
			},
		},
		{
			name: "two folders at FolderItems fit the candidate list whole",
			args: args{items: 2 * knowledge.FolderItems, chars: 10, unit: "x"},
			want: want{
				candidates: ids(10), applied: ids(10), chars: repeat(47, 10), omittedChars: repeat(0, 10),
				section: sections{Knowledge: knowledgeHeading + 470},
			},
		},
		{
			name: "the knowledge cap gives every item an equal share in runes",
			args: args{items: 3, chars: 30_000, unit: "x"},
			want: want{
				candidates: ids(3), applied: ids(3), chars: []int{23_332, 23_332, 23_333},
				omittedChars:   []int{6743, 6743, 6742},
				section:        sections{Knowledge: knowledgeHeading + 2*23_332 + 23_333 + knowledgeNotice},
				sectionOmitted: sections{Knowledge: 2*6743 + 6742},
			},
		},
		{
			name: "a Korean item is cut at a rune and counted in runes",
			args: args{items: 1, chars: 80_000, unit: "한"},
			want: want{
				candidates: ids(1), applied: ids(1), chars: []int{knowledge.ReviewChars}, omittedChars: []int{10_076},
				section:        sections{Knowledge: knowledgeHeading + knowledge.ReviewChars + knowledgeNotice},
				sectionOmitted: sections{Knowledge: 10_076},
			},
		},
		{
			name: "an emoji item is cut at a rune and counted in runes",
			args: args{items: 1, chars: 80_000, unit: "😀"},
			want: want{
				candidates: ids(1), applied: ids(1), chars: []int{knowledge.ReviewChars}, omittedChars: []int{10_076},
				section:        sections{Knowledge: knowledgeHeading + knowledge.ReviewChars + knowledgeNotice},
				sectionOmitted: sections{Knowledge: 10_076},
			},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(s.Source, testkit.Policy(t), nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)
			importApproved(t, s, tc.args.items, strings.Repeat(tc.args.unit, tc.args.chars))

			c, err := d.Prepare(ctx, "tq-005", diagnose.ModeInteractive, diagnose.Session{})
			require.NoError(t, err)
			var candidates []string
			var choices diagnose.Choices
			for _, k := range c.KnowledgeCandidates {
				candidates = append(candidates, k.ID)
				choices.Knowledge = append(choices.Knowledge, diagnose.Choice{ID: k.ID})
			}
			sel, err := d.Select(ctx, c.PendingID, choices)
			require.NoError(t, err)
			recorded, err := s.Traces.List(ctx, trace.Filter{Name: trace.NameSelect, Ref: c.PendingID})
			require.NoError(t, err)
			require.Len(t, recorded, 1)
			var in struct {
				Chars        sections `json:"chars"`
				OmittedChars sections `json:"omitted_chars"`
			}
			require.NoError(t, json.Unmarshal(recorded[0].Input, &in))
			view := want{
				candidates: candidates, omitted: c.CandidatesOmitted, section: in.Chars, sectionOmitted: in.OmittedChars,
			}
			for _, k := range sel.Applied {
				view.applied = append(view.applied, k.ID)
				view.chars = append(view.chars, k.Chars)
				view.omittedChars = append(view.omittedChars, k.OmittedChars)
			}

			assert.Equal(t, tc.want, view)
			assert.True(t, utf8.ValidString(sel.Text))
			assert.Equal(t, in.Chars.Knowledge, utf8.RuneCountInString(sel.Text))
		})
	}
}

// Approved items that fit every event
// Imported because approval refuses a folder over the caps
func importApproved(t *testing.T, s testkit.Stores, items int, content string) {
	t.Helper()
	for i := range items {
		require.NoError(t, testkit.Err(s.Ledger.Import(context.Background(), []knowledge.Knowledge{{
			ID:       fmt.Sprintf("k-%02d", i),
			Version:  1,
			Kind:     knowledge.KindMeaning,
			Content:  content,
			Evidence: knowledge.Evidence{ParagraphIDs: []string{"p-1"}},
			Basis:    knowledge.BasisStated,
			Status:   knowledge.StatusApproved,
			Approver: "author",
			Author:   "author",
			Time:     s.Clock.Now(),
		}})))
	}
}

// A knowledge list cut at the cap leaves every offered item whole so only the traces can tell a reader of eval
func TestCandidatesOmittedTraces(t *testing.T) {
	type args struct {
		items int
		// Writes the select and diagnose traces of one review
		review func(ctx context.Context, t *testing.T, d *diagnose.Diagnoser)
	}
	// Whether each trace says a candidate list was cut
	type want map[trace.Name]bool
	ready := diagnose.Diagnosis{Status: evidence.StatusNoAction}
	output, err := json.Marshal(ready)
	require.NoError(t, err)
	interactive := func(ctx context.Context, t *testing.T, d *diagnose.Diagnoser) {
		c, err := d.Prepare(ctx, "tq-005", diagnose.ModeInteractive, diagnose.Session{})
		require.NoError(t, err)
		var choices diagnose.Choices
		for _, k := range c.KnowledgeCandidates {
			choices.Knowledge = append(choices.Knowledge, diagnose.Choice{ID: k.ID})
		}
		_, err = d.Select(ctx, c.PendingID, choices)
		require.NoError(t, err)
		res, err := d.Record(ctx, c.PendingID, ready)
		require.NoError(t, err)
		// A review sent back once is recorded on the second submission
		if res.TraceID == "" {
			_, err = d.Record(ctx, c.PendingID, ready)
			require.NoError(t, err)
		}
	}
	batch := func(ctx context.Context, t *testing.T, d *diagnose.Diagnoser) {
		_, err := d.Run(ctx, "tq-005", diagnose.BatchOptions{Knowledge: diagnose.KnowledgeSelected})
		require.NoError(t, err)
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "an imported ledger over the item cap marks the select and diagnose traces of a conversation",
			args: args{items: knowledge.ReviewItems + 1, review: interactive},
			want: want{trace.NameContext: true, trace.NameSelect: true, trace.NameDiagnose: true},
		},
		{
			name: "an imported ledger over the item cap marks the select and diagnose traces of a batch run",
			args: args{items: knowledge.ReviewItems + 1, review: batch},
			want: want{trace.NameContext: true, trace.NameSelect: true, trace.NameDiagnose: true},
		},
		{
			name: "a ledger at the item cap marks no trace",
			args: args{items: knowledge.ReviewItems, review: batch},
			want: want{trace.NameContext: false, trace.NameSelect: false, trace.NameDiagnose: false},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			client := llmmock.NewMockClient(gomock.NewController(t))
			client.EXPECT().Complete(gomock.Any(), gomock.Any()).Return(llm.Response{Output: output}, nil).AnyTimes()
			d := diagnose.New(s.Source, testkit.Policy(t), client, s.Traces, s.Feedback, s.Ledger, s.Clock.Now)
			importApproved(t, s, tc.args.items, "one")

			tc.args.review(ctx, t, d)
			all, err := s.Traces.List(ctx, trace.Filter{})
			require.NoError(t, err)
			got := want{}
			for _, tr := range all {
				var marked struct {
					CandidatesOmitted bool `json:"candidates_omitted"`
				}
				body := tr.Input
				if tr.Name == trace.NameContext {
					body = tr.Output
				}
				require.NoError(t, json.Unmarshal(body, &marked))
				got[tr.Name] = marked.CandidatesOmitted
			}

			assert.Equal(t, tc.want, got)
		})
	}
}
