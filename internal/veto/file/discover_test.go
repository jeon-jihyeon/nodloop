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
	approved, blocked, partial := t.TempDir(), t.TempDir(), t.TempDir()
	approvedPath := file.NewApprovedFile(approved, "/records").Path()
	contents := map[string]string{
		filepath.Join(project, file.RelPath): "vetoes:\n  - id: shared\n    tool: Bash\n" +
			"    when: [{field: command, match: p}]\n    reason: project\n",
		filepath.Join(user, file.RelPath): "vetoes:\n  - id: shared\n    tool: Bash\n" +
			"    when: [{field: command, match: u}]\n    reason: user\n",
		filepath.Join(approved, file.RelPath): "vetoes:\n  - id: shared\n    tool: Bash\n" +
			"    when: [{field: command, match: u}]\n    reason: user\n",
		approvedPath: "vetoes:\n  - id: shared\n    tool: Bash\n" +
			"    when: [{field: command, match: a}]\n    reason: approved\n",
	}
	sources := map[string]file.Source{}
	for path, content := range contents {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		vetoes, err := veto.Parse([]byte(content))
		require.NoError(t, err)
		sources[path] = file.Source{Path: path, Vetoes: vetoes}
	}
	partialPath := filepath.Join(partial, file.RelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(partialPath), 0o755))
	require.NoError(t, os.WriteFile(partialPath, []byte(contents[filepath.Join(project, file.RelPath)]+
		"  - id: shared\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: again\n"), 0o644))
	partialMsg := partialPath + ": vetoes[1] (shared): duplicate id"
	require.NoError(t, os.MkdirAll(filepath.Join(broken, filepath.Dir(file.RelPath)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, file.RelPath), []byte("vetoes: ["), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(directory, file.RelPath), 0o755))
	// A file where the veto directory belongs so neither the user file nor the approved files can be read
	require.NoError(t, os.MkdirAll(filepath.Join(blocked, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(blocked, ".claude", "nodloop"), nil, 0o644))
	blockedDir := filepath.Join(blocked, ".claude", "nodloop")
	blockedMsg := fmt.Sprintf("%[1]s: failed to read veto file: open %[1]s: not a directory\n"+
		"%[2]s: failed to read veto file: open %[2]s: not a directory",
		blockedDir, filepath.Join(blocked, file.RelPath))
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
			want{
				file.Sources{sources[filepath.Join(project, file.RelPath)], sources[filepath.Join(user, file.RelPath)]},
				nil, "<nil>",
			},
		},
		{
			"approved files come after the user file",
			args{empty, approved},
			want{
				file.Sources{
					sources[filepath.Join(approved, file.RelPath)], sources[approvedPath],
				},
				nil, "<nil>",
			},
		},
		{
			"empty home skips the user and approved files",
			args{project, ""},
			want{file.Sources{sources[filepath.Join(project, file.RelPath)]}, nil, "<nil>"},
		},
		{"missing files are left out", args{empty, empty}, want{nil, nil, "<nil>"}},
		{
			"file with a broken entry still applies its valid entries",
			args{partial, ""},
			want{
				file.Sources{{Path: partialPath, Vetoes: sources[filepath.Join(project, file.RelPath)].Vetoes}},
				veto.ErrIDDuplicate, partialMsg,
			},
		},
		{"parse error names the path", args{empty, broken}, want{nil, veto.ErrYAMLInvalid, parseMsg}},
		{"directory in place of the file fails to read", args{directory, ""}, want{nil, file.ErrRead, readMsg}},
		{
			"broken user file is skipped and the project file still loads",
			args{project, broken},
			want{file.Sources{sources[filepath.Join(project, file.RelPath)]}, veto.ErrYAMLInvalid, parseMsg},
		},
		{
			"a file in place of the veto directory is reported for the user and approved files",
			args{"", blocked},
			want{nil, file.ErrRead, blockedMsg},
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
