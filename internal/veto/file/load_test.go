package file_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
	"github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	content := "vetoes:\n  - id: project\n    tool: Bash\n    when: [{field: command, match: p}]\n    reason: project\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "valid.yaml"), []byte(content), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("vetoes: ["), 0o644))
	vetoes, err := veto.Parse([]byte(content))
	require.NoError(t, err)
	missingPath := filepath.Join(dir, "missing.yaml")
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
		{"valid file loads", "valid.yaml", want{vetoes, nil, "<nil>"}},
		{
			"missing file fails to read and keeps the not exist cause",
			"missing.yaml",
			want{
				nil,
				os.ErrNotExist,
				fmt.Sprintf("%[1]s: failed to read veto file: open %[1]s: no such file or directory", missingPath),
			},
		},
		{
			"broken file names the path",
			"broken.yaml",
			want{
				nil,
				veto.ErrYAMLInvalid,
				filepath.Join(dir, "broken.yaml") + ": failed to parse yaml: yaml: line 1: did not find expected node content",
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := file.Load(filepath.Join(dir, tc.args))
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.msg, fmt.Sprint(err))
			assert.Equal(t, tc.want.vetoes, got)
		})
	}
}
