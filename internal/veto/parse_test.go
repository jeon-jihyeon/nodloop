package veto_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

func TestParse(t *testing.T) {
	valid, err := os.ReadFile("testdata/valid.yaml")
	require.NoError(t, err)
	invalidRegex, err := os.ReadFile("testdata/invalid_regex.yaml")
	require.NoError(t, err)
	sedCondition, err := veto.NewCondition("command", `(^|[;&|]\s*)(sed|perl)\s+(-[a-zA-Z]*i|--in-place)`, "")
	require.NoError(t, err)
	sedReason := "sed -i and perl -i are forbidden. Use the Edit tool to modify files"
	sed, err := veto.New("no-sed-inplace", "Bash", []veto.Condition{sedCondition}, sedReason, "", true)
	require.NoError(t, err)
	readmeCondition, err := veto.NewCondition("file_path", `(^|/)README\.md$`, "node_modules/")
	require.NoError(t, err)
	readmeReason := "Do not create README files"
	readme, err := veto.New("no-readme", "Write|Edit", []veto.Condition{readmeCondition}, readmeReason, "", true)
	require.NoError(t, err)
	xCondition, err := veto.NewCondition("command", "x", "")
	require.NoError(t, err)
	a, err := veto.New("a", "Bash", []veto.Condition{xCondition}, "r", "", true)
	require.NoError(t, err)
	asks, err := veto.New("a", "Bash", []veto.Condition{xCondition}, "r", veto.ActionAsk, true)
	require.NoError(t, err)
	type want struct {
		vetoes veto.Vetoes
		err    error
		msg    string
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"valid file loads every veto in order", string(valid), want{veto.Vetoes{sed, readme}, nil, "<nil>"}},
		{"empty list loads nothing", "vetoes: []", want{veto.Vetoes{}, nil, "<nil>"}},
		{
			"an entry that asks loads with its action",
			"vetoes:\n  - id: a\n    tool: Bash\n    action: ask\n    when: [{field: command, match: x}]\n    reason: r",
			want{veto.Vetoes{asks}, nil, "<nil>"},
		},
		{
			"an unknown action fails the entry alone",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r\n" +
				"  - id: b\n    tool: Bash\n    action: warn\n    when: [{field: command, match: x}]\n    reason: r",
			want{veto.Vetoes{a}, veto.ErrActionUnknown, `vetoes[1] (b): action must be block or ask: "warn"`},
		},
		{
			"yaml syntax error fails",
			"vetoes: [",
			want{nil, veto.ErrYAMLInvalid, "failed to parse yaml: yaml: line 1: did not find expected node content"},
		},
		{
			"missing id fails with the index",
			"vetoes:\n  - tool: Bash\n    when: [{field: command, match: x}]\n    reason: r",
			want{veto.Vetoes{}, veto.ErrIDMissing, "vetoes[0] (): missing id"},
		},
		{
			"missing tool fails with the id",
			"vetoes:\n  - id: a\n    when: [{field: command, match: x}]\n    reason: r",
			want{veto.Vetoes{}, veto.ErrToolMissing, "vetoes[0] (a): missing tool"},
		},
		{
			"tool of only separators fails",
			"vetoes:\n  - id: a\n    tool: ' | '\n    when: [{field: command, match: x}]\n    reason: r",
			want{veto.Vetoes{}, veto.ErrToolMissing, "vetoes[0] (a): missing tool"},
		},
		{
			"missing when fails",
			"vetoes:\n  - id: a\n    tool: Bash\n    reason: r",
			want{veto.Vetoes{}, veto.ErrWhenMissing, "vetoes[0] (a): missing when"},
		},
		{
			"missing reason fails",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]",
			want{veto.Vetoes{}, veto.ErrReasonMissing, "vetoes[0] (a): missing reason"},
		},
		{
			"missing field fails with the condition index",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{match: x}]\n    reason: r",
			want{veto.Vetoes{}, veto.ErrFieldMissing, "vetoes[0] (a): when[0]: missing field"},
		},
		{
			"missing match fails with the condition index",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command}]\n    reason: r",
			want{veto.Vetoes{}, veto.ErrMatchMissing, "vetoes[0] (a): when[0]: missing match"},
		},
		{
			"unsupported match regexp fails",
			string(invalidRegex),
			want{
				veto.Vetoes{},
				veto.ErrMatchInvalid,
				"vetoes[0] (bad-regex): when[0]: invalid match regexp: error parsing regexp: invalid named capture: `(?<=x)y`",
			},
		},
		{
			"invalid unless regexp fails",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x, unless: '('}]\n    reason: r",
			want{
				veto.Vetoes{},
				veto.ErrUnlessInvalid,
				"vetoes[0] (a): when[0]: invalid unless regexp: error parsing regexp: missing closing ): `(`",
			},
		},
		{
			"duplicate id keeps the first entry and reports the second",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r\n" +
				"  - id: a\n    tool: Bash\n    when: [{field: command, match: y}]\n    reason: r",
			want{veto.Vetoes{a}, veto.ErrIDDuplicate, "vetoes[1] (a): duplicate id"},
		},
		{
			"broken entry is left out and the valid ones still load",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r\n" +
				"  - id: b\n    tool: Bash\n    when: [{field: command, match: '('}]\n    reason: r",
			want{
				veto.Vetoes{a},
				veto.ErrMatchInvalid,
				"vetoes[1] (b): when[0]: invalid match regexp: error parsing regexp: missing closing ): `(`",
			},
		},
		{
			"entry of the wrong type is left out and the valid ones still load",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r\n" +
				"  - id: b\n    tool: Bash\n    when: sed\n    reason: r",
			want{
				veto.Vetoes{a},
				veto.ErrEntryInvalid,
				"vetoes[1] (b): invalid entry: yaml: unmarshal errors:\n" +
					"  line 8: cannot unmarshal !!str `sed` into []veto.When",
			},
		},
		{
			"key written twice in an entry fails that entry",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r\n    reason: s",
			want{
				veto.Vetoes{},
				veto.ErrEntryInvalid,
				"vetoes[0] (a): invalid entry: yaml: unmarshal errors:\n" +
					"  line 6: mapping key \"reason\" already defined at line 5",
			},
		},
		{
			"misspelled entry key fails",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r\n    enable: false",
			want{veto.Vetoes{}, veto.ErrKeyUnknown, `vetoes[0] (a): unknown key: line 6: "enable"`},
		},
		{
			"misspelled condition key fails",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x, unles: y}]\n    reason: r",
			want{veto.Vetoes{}, veto.ErrKeyUnknown, `vetoes[0] (a): unknown key: line 4: "unles"`},
		},
		{
			"misspelled top level key fails the file",
			"veto:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r",
			want{nil, veto.ErrKeyUnknown, `failed to parse yaml: unknown key: line 1: "veto"`},
		},
		{
			"misspelled top level key beside a helper anchor fails the file",
			"base: &base {tool: Bash}\nVetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r",
			want{nil, veto.ErrYAMLInvalid, `failed to parse yaml: unknown key: line 2: "Vetoes"`},
		},
		{
			"top level key beside vetoes without an anchor loads the entries and reports the key",
			"defs: x\nvetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r",
			want{veto.Vetoes{a}, veto.ErrKeyUnknown, `unknown key: line 1: "defs"`},
		},
		{
			"version key beside vetoes is reported with the broken entries",
			"version: 1\nvetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r\n" +
				"  - id: b\n    tool: Bash\n    reason: r",
			want{veto.Vetoes{a}, veto.ErrKeyUnknown, "unknown key: line 1: \"version\"\nvetoes[1] (b): missing when"},
		},
		{
			"vetoes merged in at the top level count as the vetoes key",
			"all: &all\n  vetoes:\n    - id: a\n      tool: Bash\n      when: [{field: command, match: x}]\n      reason: r\n" +
				"<<: *all\nversion: 1",
			want{veto.Vetoes{a}, veto.ErrKeyUnknown, `unknown key: line 8: "version"`},
		},
		{
			"top level key that holds anchors shares them with the entries",
			"defs:\n  bash: &bash Bash\n  cond: &cond {field: command, match: x}\n" +
				"vetoes:\n  - id: a\n    tool: *bash\n    when: [*cond]\n    reason: r",
			want{veto.Vetoes{a}, nil, "<nil>"},
		},
		{
			"entry built on a merge key loads",
			"base: &base\n  tool: Bash\n  reason: r\n" +
				"vetoes:\n  - <<: *base\n    id: a\n    when: [{field: command, match: x}]",
			want{veto.Vetoes{a}, nil, "<nil>"},
		},
		{
			"entry that is an alias loads",
			"base: &base\n  id: a\n  tool: Bash\n  when: [{field: command, match: x}]\n  reason: r\nvetoes:\n  - *base",
			want{veto.Vetoes{a}, nil, "<nil>"},
		},
		{
			"condition built on a merge key loads",
			"cond: &cond {field: command}\n" +
				"vetoes:\n  - id: a\n    tool: Bash\n    when: [{<<: *cond, match: x}]\n    reason: r",
			want{veto.Vetoes{a}, nil, "<nil>"},
		},
		{
			"misspelled key inside a merged mapping fails the entry",
			"base: &base\n  tool: Bash\n  reasn: r\n" +
				"vetoes:\n  - <<: *base\n    id: a\n    when: [{field: command, match: x}]\n    reason: r",
			want{veto.Vetoes{}, veto.ErrKeyUnknown, `vetoes[0] (a): unknown key: line 3: "reasn"`},
		},
		{"empty input loads nothing", "", want{veto.Vetoes{}, nil, "<nil>"}},
		{"comments alone load nothing", "# nothing yet\n", want{veto.Vetoes{}, nil, "<nil>"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := veto.Parse([]byte(tc.args))
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.msg, fmt.Sprint(err))
			assert.Equal(t, tc.want.vetoes, got)
		})
	}
}
