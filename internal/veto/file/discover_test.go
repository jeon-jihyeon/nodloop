package file_test

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
	"github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

func TestDiscover(t *testing.T) {
	project, user, empty, broken, directory := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	contents := map[string]string{
		project: "vetoes:\n  - id: shared\n    tool: Bash\n    when: [{field: command, match: p}]\n    reason: project\n",
		user:    "vetoes:\n  - id: shared\n    tool: Bash\n    when: [{field: command, match: u}]\n    reason: user\n",
	}
	sources := map[string]file.Source{}
	for base, content := range contents {
		path := filepath.Join(base, file.RelPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		vetoes, err := veto.Parse([]byte(content))
		require.NoError(t, err)
		sources[base] = file.Source{Path: path, Vetoes: vetoes}
	}
	require.NoError(t, os.MkdirAll(filepath.Join(broken, filepath.Dir(file.RelPath)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, file.RelPath), []byte("vetoes: ["), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(directory, file.RelPath), 0o755))
	parseMsg := filepath.Join(broken, file.RelPath) +
		": failed to parse yaml: yaml: line 1: did not find expected node content"
	directoryPath := filepath.Join(directory, file.RelPath)
	readMsg := fmt.Sprintf("%[1]s: failed to read veto file: read %[1]s: is a directory", directoryPath)
	type args struct {
		cwd  string
		home string
	}
	type want struct {
		sources file.Sources
		err     error
		msg     string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"project file comes before user file",
			args{project, user},
			want{file.Sources{sources[project], sources[user]}, nil, "<nil>"},
		},
		{"empty home skips the user file", args{project, ""}, want{file.Sources{sources[project]}, nil, "<nil>"}},
		{"missing files are left out", args{empty, empty}, want{nil, nil, "<nil>"}},
		{"parse error names the path", args{empty, broken}, want{nil, veto.ErrYAMLInvalid, parseMsg}},
		{"directory in place of the file fails to read", args{directory, ""}, want{nil, file.ErrRead, readMsg}},
		{
			"broken user file is skipped and the project file still loads",
			args{project, broken},
			want{file.Sources{sources[project]}, veto.ErrYAMLInvalid, parseMsg},
		},
		{
			"both broken files are reported together",
			args{broken, directory},
			want{nil, syscall.EISDIR, parseMsg + "\n" + readMsg},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := file.Discover(tc.args.cwd, tc.args.home)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.msg, fmt.Sprint(err))
			assert.Equal(t, tc.want.sources, got)
		})
	}
}
