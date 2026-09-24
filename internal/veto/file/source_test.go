package file_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
	"github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestSourcesVetoes(t *testing.T) {
	condition, err := veto.NewCondition("command", "x", "")
	require.NoError(t, err)
	when := []veto.Condition{condition}
	projectShared, err := veto.New("shared", "Bash", when, "project", true)
	require.NoError(t, err)
	projectOnly, err := veto.New("project-only", "Bash", when, "project", true)
	require.NoError(t, err)
	userShared, err := veto.New("shared", "Bash", when, "user", true)
	require.NoError(t, err)
	userOnly, err := veto.New("user-only", "Bash", when, "user", true)
	require.NoError(t, err)
	tcs := []struct {
		name string
		args file.Sources
		want veto.Vetoes
	}{
		{
			"earlier source wins on a shared id",
			file.Sources{
				{Path: "project", Vetoes: veto.Vetoes{projectShared, projectOnly}},
				{Path: "user", Vetoes: veto.Vetoes{userShared, userOnly}},
			},
			veto.Vetoes{projectShared, projectOnly, userOnly},
		},
		{"no sources merge to nothing", nil, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Vetoes())
		})
	}
}
