package veto_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

func TestMatches(t *testing.T) {
	b, err := os.ReadFile("testdata/valid.yaml")
	require.NoError(t, err)
	vetoes, err := veto.Parse(b)
	require.NoError(t, err)
	require.Len(t, vetoes, 2)
	sed, readme := vetoes[0], vetoes[1]
	require.Equal(t, "no-sed-inplace", sed.ID())
	require.Equal(t, "Do not create README files", readme.Reason())
	condition, err := veto.NewCondition("command", "sed -i", "")
	require.NoError(t, err)
	disabled, err := veto.New("no-sed-inplace", "Bash", []veto.Condition{condition}, "r", false)
	require.NoError(t, err)
	type args struct {
		veto  veto.Veto
		tool  string
		input map[string]any
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"sed in place edit is blocked", args{sed, "Bash", map[string]any{"command": "sed -i 's/a/b/' f"}}, true},
		{
			"perl in place edit after a pipe is blocked",
			args{sed, "Bash", map[string]any{"command": "cat f | perl -pi -e 's/a/b/'"}},
			true,
		},
		{"sed without in place flag passes", args{sed, "Bash", map[string]any{"command": "sed 's/a/b/' f"}}, false},
		{"tool outside the veto passes", args{sed, "Edit", map[string]any{"command": "sed -i x"}}, false},
		{"missing field passes", args{sed, "Bash", map[string]any{}}, false},
		{"non string field passes", args{sed, "Bash", map[string]any{"command": 42}}, false},
		{"disabled veto passes", args{disabled, "Bash", map[string]any{"command": "sed -i x"}}, false},
		{"explicitly enabled veto blocks", args{readme, "Write", map[string]any{"file_path": "/repo/README.md"}}, true},
		{"second tool in the list is blocked", args{readme, "Edit", map[string]any{"file_path": "README.md"}}, true},
		{"unless match passes", args{readme, "Write", map[string]any{"file_path": "/repo/node_modules/x/README.md"}}, false},
		{"zero veto never matches", args{veto.Veto{}, "Bash", map[string]any{"command": "x"}}, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.veto.Matches(tc.args.tool, tc.args.input))
		})
	}
}

func TestVetoesMatch(t *testing.T) {
	b, err := os.ReadFile("testdata/valid.yaml")
	require.NoError(t, err)
	vetoes, err := veto.Parse(b)
	require.NoError(t, err)
	require.Len(t, vetoes, 2)
	type args struct {
		tool  string
		input map[string]any
	}
	tcs := []struct {
		name string
		args args
		want *veto.Veto
	}{
		{"first veto in order is returned", args{"Bash", map[string]any{"command": "sed -i x"}}, &vetoes[0]},
		{"later veto is returned when the first passes", args{"Edit", map[string]any{"file_path": "README.md"}}, &vetoes[1]},
		{"no match returns nil", args{"Bash", map[string]any{"command": "ls"}}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, vetoes.Match(tc.args.tool, tc.args.input))
		})
	}
}
