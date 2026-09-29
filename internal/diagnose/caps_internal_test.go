package diagnose

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// The caps are constants so the cut arithmetic is checked here on small budgets
// Every size counts runes so the Korean and emoji rows read the same as the ASCII ones
func TestBudgetSection(t *testing.T) {
	type args struct {
		budget budget
		texts  []block
	}
	type want struct {
		sizes sizes
		// Text the section must carry and must not carry
		present, absent []string
	}
	x := strings.Repeat
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"no texts give no section", args{100, nil}, want{}},
		{
			"a text within its share arrives whole without the notice",
			args{100, []block{{lead: x("L", 10), body: x("B", 10), tail: x("T", 10)}}},
			want{
				sizes: sizes{{chars: 30}}, present: []string{"H" + x("L", 10) + x("B", 10) + x("T", 10)},
				absent: []string{"N"},
			},
		},
		{
			"the tail gives way first and ends with the cut mark",
			args{60, []block{{lead: x("L", 10), body: x("B", 10), tail: x("T", 100)}}},
			want{
				sizes:   sizes{{chars: 59, omitted: 97}},
				present: []string{x("B", 10) + "TTT\n[97 characters omitted at the cap]\nN"},
			},
		},
		{
			"a tail whose room cannot hold the mark is left out whole without a mark",
			args{40, []block{{lead: x("L", 10), body: x("B", 10), tail: x("T", 100)}}},
			want{sizes: sizes{{chars: 20, omitted: 100}}, present: []string{x("B", 10) + "N"}, absent: []string{"omitted"}},
		},
		{
			"a smaller share cuts the body and leaves out the tail",
			args{60, []block{{lead: x("L", 10), body: x("B", 100), tail: x("T", 10)}}},
			want{
				sizes:   sizes{{chars: 59, omitted: 97}},
				present: []string{x("L", 10) + x("B", 13) + "\n[97 characters omitted at the cap]\n"},
				absent:  []string{"T"},
			},
		},
		{
			"a Korean body is cut at a rune and counted in runes",
			args{60, []block{{lead: x("L", 10), body: x("한", 100)}}},
			want{sizes: sizes{{chars: 59, omitted: 87}}, present: []string{x("L", 10) + x("한", 13) + "\n[87 characters"}},
		},
		{
			"an emoji body is cut at a rune and counted in runes",
			args{60, []block{{lead: x("L", 10), body: x("😀", 100)}}},
			want{sizes: sizes{{chars: 59, omitted: 87}}, present: []string{x("L", 10) + x("😀", 13) + "\n[87 characters"}},
		},
		{
			"a share too small for the lead and the mark gives nothing",
			args{15, []block{{lead: x("L", 10), body: x("B", 10)}}},
			want{sizes: sizes{{omitted: 20}}, present: []string{"HN"}},
		},
		{
			"a share that holds the mark and no rune gives nothing",
			args{36, []block{{body: x("B", 100)}}},
			want{sizes: sizes{{omitted: 100}}, present: []string{"HN"}},
		},
		{
			"texts of equal need split the budget evenly",
			args{100, []block{{body: x("A", 100)}, {body: x("B", 100)}}},
			want{sizes: sizes{{chars: 49, omitted: 87}, {chars: 49, omitted: 87}}},
		},
		{
			"a smaller need takes its full size before the larger ones split the rest",
			args{100, []block{{body: x("A", 10)}, {body: x("B", 200)}}},
			want{sizes: sizes{{chars: 10}, {chars: 90, omitted: 147}}},
		},
		{
			"tails share what the leads and bodies leave",
			args{100, []block{{lead: x("A", 10), tail: x("a", 100)}, {lead: x("B", 10), tail: x("b", 100)}}},
			want{sizes: sizes{{chars: 49, omitted: 97}, {chars: 49, omitted: 97}}},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, measured := tc.args.budget.section("H", "N", tc.args.texts)

			assert.Equal(t, tc.want.sizes, measured)
			assert.True(t, utf8.ValidString(got))
			for _, text := range tc.want.present {
				assert.Contains(t, got, text)
			}
			for _, text := range tc.want.absent {
				assert.NotContains(t, got, text)
			}
			given := 0
			for _, s := range measured {
				given += s.chars
			}
			assert.LessOrEqual(t, given, int(tc.args.budget))
		})
	}
}

// A folder is crowded above FolderItems items
func TestCandidatesFolders(t *testing.T) {
	tcs := []struct {
		name string
		// Items of two folders on one event
		args int
		// Whether the candidate list holds them whole
		want bool
	}{
		{"two folders at FolderItems fit the candidate list whole", 2 * knowledge.FolderItems, true},
		{"two crowded folders overflow the candidate list", 2 * (knowledge.FolderItems + 1), false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args <= candidates)
		})
	}
}
