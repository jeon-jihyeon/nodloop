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
	// A project whose file sits two levels above the cwd and a nested project inside it
	outer := t.TempDir()
	below := filepath.Join(outer, "internal", "x")
	nested := filepath.Join(outer, "nested")
	override := filepath.Join(outer, "override")
	cracked := filepath.Join(outer, "cracked")
	// A home that is an ancestor of the cwd and is itself under version control
	homeAbove := t.TempDir()
	underHome := filepath.Join(homeAbove, "work", "repo")
	// A shared directory outside home without .git above the cwd
	stray := t.TempDir()
	scratch := filepath.Join(stray, "someone", "scratch")
	// A repository inside a directory that has a veto file of its own
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	worktree := filepath.Join(parent, "worktree")
	// A project under home whose file is not valid YAML
	homeRepo := t.TempDir()
	homeBrokenPath := filepath.Join(homeRepo, "repo", file.RelPath)
	for _, d := range []string{
		below, underHome, scratch, filepath.Join(repo, "sub"), filepath.Join(outer, ".git"), filepath.Join(broken, ".git"),
		filepath.Join(homeAbove, ".git"), filepath.Join(repo, ".git"), filepath.Dir(homeBrokenPath),
	} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
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
		filepath.Join(outer, file.RelPath): "vetoes:\n  - id: outer\n    tool: Bash\n" +
			"    when: [{field: command, match: o}]\n    reason: outer\n",
		filepath.Join(nested, file.RelPath): "vetoes:\n  - id: nested\n    tool: Bash\n" +
			"    when: [{field: command, match: n}]\n    reason: nested\n",
		filepath.Join(override, file.RelPath): "vetoes:\n  - id: outer\n    tool: Bash\n" +
			"    when: [{field: command, match: v}]\n    reason: override\n",
		filepath.Join(homeAbove, file.RelPath): "vetoes:\n  - id: user\n    tool: Bash\n" +
			"    when: [{field: command, match: u}]\n    reason: user\n",
		filepath.Join(parent, file.RelPath): "vetoes:\n  - id: parent\n    tool: Bash\n" +
			"    when: [{field: command, match: p}]\n    reason: parent\n",
		filepath.Join(repo, file.RelPath): "vetoes:\n  - id: repo\n    tool: Bash\n" +
			"    when: [{field: command, match: r}]\n    reason: repo\n",
		filepath.Join(worktree, file.RelPath): "vetoes:\n  - id: worktree\n    tool: Bash\n" +
			"    when: [{field: command, match: w}]\n    reason: worktree\n",
	}
	sources := map[string]file.Source{}
	for path, content := range contents {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		vetoes, err := veto.Parse([]byte(content))
		require.NoError(t, err)
		sources[path] = file.Source{Path: path, Vetoes: vetoes}
	}
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere\n"), 0o644))
	strayPath := filepath.Join(stray, file.RelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(strayPath), 0o755))
	require.NoError(t, os.WriteFile(strayPath, []byte("vetoes: ["), 0o644))
	require.NoError(t, os.WriteFile(homeBrokenPath, []byte("vetoes: ["), 0o644))
	homeBrokenMsg := homeBrokenPath +
		": failed to read veto file: failed to parse yaml: yaml: line 1: did not find expected node content"
	partialPath := filepath.Join(partial, file.RelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(partialPath), 0o755))
	require.NoError(t, os.WriteFile(partialPath, []byte(contents[filepath.Join(project, file.RelPath)]+
		"  - id: shared\n    tool: Bash\n    when: [{field: command, match: x}]\n    reason: again\n"), 0o644))
	partialMsg := partialPath + ": vetoes[1] (shared): duplicate id"
	require.NoError(t, os.MkdirAll(filepath.Join(broken, filepath.Dir(file.RelPath)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, file.RelPath), []byte("vetoes: ["), 0o644))
	crackedPath := filepath.Join(cracked, file.RelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(crackedPath), 0o755))
	require.NoError(t, os.WriteFile(crackedPath, []byte("vetoes: ["), 0o644))
	crackedMsg := crackedPath +
		": failed to read veto file: failed to parse yaml: yaml: line 1: did not find expected node content"
	require.NoError(t, os.MkdirAll(filepath.Join(directory, file.RelPath), 0o755))
	// A file where the veto directory belongs so neither the user file nor the approved files can be read
	require.NoError(t, os.MkdirAll(filepath.Join(blocked, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(blocked, ".claude", "nodloop"), nil, 0o644))
	blockedDir := filepath.Join(blocked, ".claude", "nodloop")
	blockedMsg := fmt.Sprintf("%[1]s: failed to read veto file: open %[1]s: not a directory\n"+
		"%[2]s: failed to read veto file: open %[2]s: not a directory",
		blockedDir, filepath.Join(blocked, file.RelPath))
	parseMsg := filepath.Join(broken, file.RelPath) +
		": failed to read veto file: failed to parse yaml: yaml: line 1: did not find expected node content"
	directoryPath := filepath.Join(directory, file.RelPath)
	outside := file.ErrOutsideHome.Error() + ": "
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
			"project file two levels above the cwd applies",
			args{below, empty},
			want{file.Sources{sources[filepath.Join(outer, file.RelPath)]}, nil, "<nil>"},
		},
		{
			"nested project file stacks before the outer one",
			args{nested, empty},
			want{
				file.Sources{sources[filepath.Join(nested, file.RelPath)], sources[filepath.Join(outer, file.RelPath)]},
				nil, "<nil>",
			},
		},
		{
			"nested project file reusing an outer id comes first so it wins",
			args{override, empty},
			want{
				file.Sources{sources[filepath.Join(override, file.RelPath)], sources[filepath.Join(outer, file.RelPath)]},
				nil, "<nil>",
			},
		},
		{
			"broken nested project file is reported and the outer one still loads",
			args{cracked, empty},
			want{file.Sources{sources[filepath.Join(outer, file.RelPath)]}, file.ErrOutsideHome, outside + crackedMsg},
		},
		{
			"user file above the cwd loads once as the user file",
			args{underHome, homeAbove},
			want{file.Sources{sources[filepath.Join(homeAbove, file.RelPath)]}, nil, "<nil>"},
		},
		{
			"cwd equal to home loads the user file once",
			args{homeAbove, homeAbove},
			want{file.Sources{sources[filepath.Join(homeAbove, file.RelPath)]}, nil, "<nil>"},
		},
		{
			"broken file in an ancestor is reported with its path",
			args{filepath.Join(broken, "sub"), empty},
			want{nil, file.ErrOutsideHome, outside + parseMsg},
		},
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
			want{nil, syscall.EISDIR, outside + parseMsg + "\n" + readMsg},
		},
		{
			"cwd outside home without .git reads no file of a parent",
			args{scratch, empty},
			want{nil, nil, "<nil>"},
		},
		{
			"walk stops at the directory holding .git",
			args{filepath.Join(repo, "sub"), empty},
			want{file.Sources{sources[filepath.Join(repo, file.RelPath)]}, nil, "<nil>"},
		},
		{
			"walk stops at a .git file of a worktree",
			args{worktree, empty},
			want{file.Sources{sources[filepath.Join(worktree, file.RelPath)]}, nil, "<nil>"},
		},
		{
			"project file under home that is not valid yaml keeps the yaml error",
			args{filepath.Join(homeRepo, "repo"), homeRepo},
			want{nil, veto.ErrYAMLInvalid, homeBrokenMsg},
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
