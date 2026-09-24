package file_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/settings"
	"github.com/jeon-jihyeon/nodloop/internal/settings/file"
)

func TestInstall(t *testing.T) {
	const exe = "/Users/me/go/bin/nodloop"
	b, err := os.ReadFile(filepath.Join("testdata", "existing.json"))
	require.NoError(t, err)
	existing := string(b)
	b, err = os.ReadFile(filepath.Join("testdata", "installed.json"))
	require.NoError(t, err)
	installed := string(b)
	b, err = os.ReadFile(filepath.Join("testdata", "fresh.json"))
	require.NoError(t, err)
	fresh := string(b)
	type args struct {
		files map[string]string
		dirs  []string
		mode  os.FileMode
	}
	type want struct {
		changed   bool
		err       error
		settings  string
		backup    string
		backupErr error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"existing settings gain our hook after other hooks",
			args{files: map[string]string{"settings.json": existing}, mode: 0o700},
			want{changed: true, settings: installed, backup: existing},
		},
		{
			"already installed hook is left unchanged",
			args{files: map[string]string{"settings.json": installed}, mode: 0o700},
			want{settings: installed, backupErr: os.ErrNotExist},
		},
		{
			"missing file is created without a backup",
			args{mode: 0o700},
			want{changed: true, settings: fresh, backupErr: os.ErrNotExist},
		},
		{
			"blank file is treated as empty settings",
			args{files: map[string]string{"settings.json": "  \n"}, mode: 0o700},
			want{changed: true, settings: fresh, backup: "  \n"},
		},
		{
			"invalid json fails and leaves the file",
			args{files: map[string]string{"settings.json": "{not json"}, mode: 0o700},
			want{err: file.ErrJSONInvalid, settings: "{not json", backupErr: os.ErrNotExist},
		},
		{
			"hooks of an unexpected shape fail and leave the file",
			args{files: map[string]string{"settings.json": `{"hooks":"odd"}`}, mode: 0o700},
			want{err: settings.ErrHooksInvalid, settings: `{"hooks":"odd"}`, backupErr: os.ErrNotExist},
		},
		{
			"backup failure leaves the file and reports no change",
			args{files: map[string]string{"settings.json": "{}"}, dirs: []string{"settings.json.bak"}, mode: 0o700},
			want{err: file.ErrBackup, settings: "{}", backupErr: syscall.EISDIR},
		},
		{
			"read only directory fails after the backup and reports no change",
			args{files: map[string]string{"settings.json": "{}", "settings.json.bak": "old"}, mode: 0o500},
			want{err: file.ErrWrite, settings: "{}", backup: "{}"},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, d := range tc.args.dirs {
				require.NoError(t, os.Mkdir(filepath.Join(dir, d), 0o755))
			}
			for name, content := range tc.args.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}
			require.NoError(t, os.Chmod(dir, tc.args.mode))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			path := filepath.Join(dir, "settings.json")

			changed, err := file.Install(path, exe)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.changed, changed)
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, tc.want.settings, string(got))
			backup, err := os.ReadFile(path + ".bak")
			assert.ErrorIs(t, err, tc.want.backupErr)
			assert.Equal(t, tc.want.backup, string(backup))
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			temps, err := filepath.Glob(filepath.Join(dir, ".settings-*.json"))
			require.NoError(t, err)
			assert.Empty(t, temps)
		})
	}
}

func TestUninstall(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "installed.json"))
	require.NoError(t, err)
	installed := string(b)
	b, err = os.ReadFile(filepath.Join("testdata", "removed.json"))
	require.NoError(t, err)
	removed := string(b)
	type args struct {
		settings string
		mode     os.FileMode
	}
	type want struct {
		changed   bool
		err       error
		settings  string
		backup    string
		backupErr error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"only our hook is removed and the rest is backed up",
			args{installed, 0o700},
			want{changed: true, settings: removed, backup: installed},
		},
		{
			"settings without our hook are left unchanged",
			args{removed, 0o700},
			want{settings: removed, backupErr: os.ErrNotExist},
		},
		{
			"invalid json fails and leaves the file",
			args{"{not json", 0o700},
			want{err: file.ErrJSONInvalid, settings: "{not json", backupErr: os.ErrNotExist},
		},
		{
			"read only directory fails and reports no change",
			args{installed, 0o500},
			want{err: file.ErrBackup, settings: installed, backupErr: os.ErrNotExist},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "settings.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.args.settings), 0o600))
			require.NoError(t, os.Chmod(dir, tc.args.mode))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

			changed, err := file.Uninstall(path)
			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.changed, changed)
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, tc.want.settings, string(got))
			backup, err := os.ReadFile(path + ".bak")
			assert.ErrorIs(t, err, tc.want.backupErr)
			assert.Equal(t, tc.want.backup, string(backup))
		})
	}
}

func TestEditOfAnUnusablePath(t *testing.T) {
	install := func(path string) (bool, error) { return file.Install(path, "/Users/me/go/bin/nodloop") }
	type args struct {
		dirs []string
		mode os.FileMode
		path string
		run  func(path string) (bool, error)
	}
	tcs := []struct {
		name string
		args args
		want error
	}{
		{
			"install fails when settings.json is a directory",
			args{[]string{"settings.json"}, 0o700, "settings.json", install},
			file.ErrRead,
		},
		{
			"uninstall fails when settings.json is a directory",
			args{[]string{"settings.json"}, 0o700, "settings.json", file.Uninstall},
			file.ErrRead,
		},
		{
			"install fails when the parent cannot be created",
			args{nil, 0o500, "missing/settings.json", install},
			file.ErrWrite,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, d := range tc.args.dirs {
				require.NoError(t, os.Mkdir(filepath.Join(dir, d), 0o755))
			}
			require.NoError(t, os.Chmod(dir, tc.args.mode))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

			changed, err := tc.args.run(filepath.Join(dir, tc.args.path))
			assert.ErrorIs(t, err, tc.want)
			assert.False(t, changed)
		})
	}
}
