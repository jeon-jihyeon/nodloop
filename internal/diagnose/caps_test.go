package diagnose_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/analysis"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestLoadLimits(t *testing.T) {
	type want struct {
		limits diagnose.Limits
		err    error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			name: "limits section of a policy file is read beside its analyzers",
			args: analysis.DefaultPolicyYAML + "limits:\n  knowledge_chars: 100\n  example_chars: 200\n  candidates: 3\n",
			want: want{limits: diagnose.Limits{KnowledgeChars: 100, ExampleChars: 200, Candidates: 3}},
		},
		{
			name: "policy file without limits gives zero limits",
			args: analysis.DefaultPolicyYAML,
		},
		{
			name: "broken yaml is refused",
			args: "limits: [",
			want: want{err: diagnose.ErrBadLimits},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := diagnose.LoadLimits([]byte(tc.args))
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.limits, got)
		})
	}
}

func TestCaps(t *testing.T) {
	type args struct {
		limits diagnose.Limits
		items  int
		// Runes of content per item
		chars int
		unit  string
	}
	type want struct {
		candidates []string
		applied    []string
		// Chars of each applied item
		chars []int
		// Characters left out of each applied item
		omittedChars []int
		omitted      bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			name: "default candidate cap offers ten items",
			args: args{items: 11, chars: 10, unit: "x"},
			want: want{
				candidates:   []string{"k-00", "k-01", "k-02", "k-03", "k-04", "k-05", "k-06", "k-07", "k-08", "k-09"},
				applied:      []string{"k-00", "k-01", "k-02", "k-03", "k-04", "k-05", "k-06", "k-07", "k-08", "k-09"},
				chars:        []int{47, 47, 47, 47, 47, 47, 47, 47, 47, 47},
				omittedChars: []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			},
		},
		{
			name: "policy candidate cap overrides the default",
			args: args{limits: diagnose.Limits{Candidates: 2}, items: 3, chars: 10, unit: "x"},
			want: want{
				candidates:   []string{"k-00", "k-01"},
				applied:      []string{"k-00", "k-01"},
				chars:        []int{47, 47},
				omittedChars: []int{0, 0},
			},
		},
		{
			name: "default knowledge cap of four thousand chars gives every item an equal share",
			args: args{items: 3, chars: 1900, unit: "x"},
			want: want{
				candidates:   []string{"k-00", "k-01", "k-02"},
				applied:      []string{"k-00", "k-01", "k-02"},
				chars:        []int{1332, 1332, 1333},
				omittedChars: []int{642, 642, 641},
				omitted:      true,
			},
		},
		{
			name: "policy knowledge cap overrides the default",
			args: args{limits: diagnose.Limits{KnowledgeChars: 120}, items: 2, chars: 60, unit: "x"},
			want: want{
				candidates:   []string{"k-00", "k-01"},
				applied:      []string{"k-00", "k-01"},
				chars:        []int{60, 60},
				omittedChars: []int{73, 73},
				omitted:      true,
			},
		},
		{
			name: "items whose shares cannot hold the cut mark do not reach the model",
			args: args{limits: diagnose.Limits{KnowledgeChars: 40}, items: 2, chars: 60, unit: "x"},
			want: want{candidates: []string{"k-00", "k-01"}, omitted: true},
		},
		{
			name: "an item whose share holds the cut mark and no character does not reach the model",
			args: args{limits: diagnose.Limits{KnowledgeChars: 36}, items: 1, chars: 60, unit: "x"},
			want: want{candidates: []string{"k-00"}, omitted: true},
		},
		{
			name: "a cut inside a three byte Korean rune backs off to its start and counts the rune as omitted",
			args: args{limits: diagnose.Limits{KnowledgeChars: 100}, items: 1, chars: 40, unit: "한"},
			want: want{
				candidates:   []string{"k-00"},
				applied:      []string{"k-00"},
				chars:        []int{97},
				omittedChars: []int{96},
				omitted:      true,
			},
		},
		{
			name: "a cut inside a four byte emoji rune backs off to its start and counts the rune as omitted",
			args: args{limits: diagnose.Limits{KnowledgeChars: 102}, items: 1, chars: 30, unit: "😀"},
			want: want{
				candidates:   []string{"k-00"},
				applied:      []string{"k-00"},
				chars:        []int{99},
				omittedChars: []int{94},
				omitted:      true,
			},
		},
		{
			name: "a cut that lands on a rune start keeps every whole rune before it",
			args: args{limits: diagnose.Limits{KnowledgeChars: 100}, items: 1, chars: 30, unit: "😀"},
			want: want{
				candidates:   []string{"k-00"},
				applied:      []string{"k-00"},
				chars:        []int{99},
				omittedChars: []int{94},
				omitted:      true,
			},
		},
	}
	ctx := context.Background()
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := testkit.Open(t)
			d := diagnose.New(
				s.Source, analysis.DefaultPolicy(), tc.args.limits, nil, s.Traces, s.Feedback, s.Ledger, s.Clock.Now,
			)
			for i := range tc.args.items {
				id := fmt.Sprintf("k-%02d", i)
				_, _, err := s.Ledger.Propose(
					ctx,
					knowledge.Knowledge{
						ID:       id,
						Kind:     knowledge.KindMeaning,
						Content:  strings.Repeat(tc.args.unit, tc.args.chars),
						Evidence: knowledge.Evidence{ParagraphIDs: []string{"p-1"}},
						Author:   "author",
					},
				)
				require.NoError(t, err)
				_, err = s.Ledger.Approve(ctx, id, 1, "author")
				require.NoError(t, err)
			}

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
			view := want{candidates: candidates, omitted: sel.Omitted}
			for _, k := range sel.Applied {
				view.applied = append(view.applied, k.ID)
				view.chars = append(view.chars, k.Chars)
				view.omittedChars = append(view.omittedChars, k.OmittedChars)
			}

			assert.Equal(t, tc.want, view)
			assert.True(t, utf8.ValidString(sel.Text))
		})
	}
}
