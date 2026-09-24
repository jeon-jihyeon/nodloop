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
	sed, err := veto.New("no-sed-inplace", "Bash", []veto.Condition{sedCondition}, sedReason, true)
	require.NoError(t, err)
	readmeCondition, err := veto.NewCondition("file_path", `(^|/)README\.md$`, "node_modules/")
	require.NoError(t, err)
	readmeReason := "Do not create README files"
	readme, err := veto.New("no-readme", "Write|Edit", []veto.Condition{readmeCondition}, readmeReason, true)
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
			"yaml syntax error fails",
			"vetoes: [",
			want{nil, veto.ErrYAMLInvalid, "failed to parse yaml: yaml: line 1: did not find expected node content"},
		},
		{
			"missing id fails with the index",
			"vetoes:\n  - tool: Bash\n    when: [{field: command, match: x}]\n    reason: r",
			want{nil, veto.ErrIDMissing, "vetoes[0] (): missing id"},
		},
		{
			"missing tool fails with the id",
			"vetoes:\n  - id: a\n    when: [{field: command, match: x}]\n    reason: r",
			want{nil, veto.ErrToolMissing, "vetoes[0] (a): missing tool"},
		},
		{
			"tool of only separators fails",
			"vetoes:\n  - id: a\n    tool: ' | '\n    when: [{field: command, match: x}]\n    reason: r",
			want{nil, veto.ErrToolMissing, "vetoes[0] (a): missing tool"},
		},
		{
			"missing when fails",
			"vetoes:\n  - id: a\n    tool: Bash\n    reason: r",
			want{nil, veto.ErrWhenMissing, "vetoes[0] (a): missing when"},
		},
		{
			"missing reason fails",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]",
			want{nil, veto.ErrReasonMissing, "vetoes[0] (a): missing reason"},
		},
		{
			"missing field fails with the condition index",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{match: x}]\n    reason: r",
			want{nil, veto.ErrFieldMissing, "vetoes[0] (a): when[0]: missing field"},
		},
		{
			"missing match fails with the condition index",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command}]\n    reason: r",
			want{nil, veto.ErrMatchMissing, "vetoes[0] (a): when[0]: missing match"},
		},
		{
			"unsupported match regexp fails",
			string(invalidRegex),
			want{
				nil,
				veto.ErrMatchInvalid,
				"vetoes[0] (bad-regex): when[0]: invalid match regexp: error parsing regexp: invalid named capture: `(?<=x)y`",
			},
		},
		{
			"invalid unless regexp fails",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x, unless: '('}]\n    reason: r",
			want{
				nil,
				veto.ErrUnlessInvalid,
				"vetoes[0] (a): when[0]: invalid unless regexp: error parsing regexp: missing closing ): `(`",
			},
		},
		{
			"duplicate id fails at the second entry",
			"vetoes:\n  - id: a\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: r\n" +
				"  - id: a\n    tool: Bash\n    when: [{field: command, match: y}]\n    reason: r",
			want{nil, veto.ErrIDDuplicate, "vetoes[1] (a): duplicate id"},
		},
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
