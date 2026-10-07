package userconfig_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/userconfig"
)

func TestHomeRead(t *testing.T) {
	type got struct {
		RecordDir string `json:"record_dir"`
	}
	type want struct {
		got got
		err error
	}
	tcs := []struct {
		name string
		// config.json and none when empty
		args string
		want want
	}{
		{"a missing file leaves the value", "", want{got{}, nil}},
		{"a key the value does not name is ignored", `{"record_dir":"/r","server":{"keys":[]}}`, want{got{RecordDir: "/r"}, nil}},
		{"a broken file fails", "{broken", want{got{}, userconfig.ErrInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := userconfig.Home(t.TempDir())
			if tc.args != "" {
				require.NoError(t, os.MkdirAll(home.Dir(), 0o700))
				require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(tc.args), 0o600))
			}
			var g got

			err := home.Read(&g)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.got, g)
		})
	}
}

func TestHomeSave(t *testing.T) {
	type args struct {
		// config.json before the call and none when empty
		saved  string
		values map[string]any
	}
	type want struct {
		saved string
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a first save creates the file", args{"", map[string]any{"approver": "ann"}}, want{"{\n  \"approver\": \"ann\"\n}\n", nil}},
		{
			"several keys are written at once and the others kept",
			args{`{"record_dir":"/r","classifiers":{"laya":{"url":"http://x"}},"decisions":{}}`,
				map[string]any{"decision_points": map[string]string{"critic": "x"}, "classifiers": nil, "decisions": nil}},
			want{"{\n  \"decision_points\": {\n    \"critic\": \"x\"\n  },\n  \"record_dir\": \"/r\"\n}\n", nil},
		},
		{"removing a missing key changes nothing", args{`{"approver":"ann"}`, map[string]any{"holdout": nil}}, want{"{\n  \"approver\": \"ann\"\n}\n", nil}},
		{"a broken file is not overwritten", args{"{broken", map[string]any{"approver": "ann"}}, want{"{broken", userconfig.ErrInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := userconfig.Home(t.TempDir())
			if tc.args.saved != "" {
				require.NoError(t, os.MkdirAll(home.Dir(), 0o700))
				require.NoError(t, os.WriteFile(home.ConfigPath(), []byte(tc.args.saved), 0o600))
			}

			err := home.Save(tc.args.values)

			assert.ErrorIs(t, err, tc.want.err)
			saved, err := os.ReadFile(home.ConfigPath())
			require.NoError(t, err)
			assert.Equal(t, tc.want.saved, string(saved))
		})
	}
}

// Saves of different keys at once all land because each holds the lock from its read to its rename
func TestHomeSaveConcurrent(t *testing.T) {
	home := userconfig.Home(t.TempDir())
	const writers = 20
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			assert.NoError(t, home.Save(map[string]any{fmt.Sprintf("key%02d", i): i}))
		})
	}
	wg.Wait()

	saved := map[string]int{}
	require.NoError(t, home.Read(&saved))
	assert.Len(t, saved, writers)
}

func TestHomeRecordDir(t *testing.T) {
	home := userconfig.Home("/home/ann")
	wd, err := os.Getwd()
	require.NoError(t, err)
	type args struct {
		home                     userconfig.Home
		flag, env, configuration string
	}
	type want struct {
		dir string
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"the flag wins", args{home, "/flag", "/env", "/saved"}, want{"/flag", nil}},
		{"a relative flag resolves against the working directory", args{home, "rec", "", ""}, want{filepath.Join(wd, "rec"), nil}},
		{"the env wins over the config", args{home, "", "/env/", "/saved"}, want{"/env", nil}},
		{"the config wins over home", args{home, "", "", "/saved"}, want{"/saved", nil}},
		{"home names the default", args{home, "", "", ""}, want{"/home/ann/.nodloop/records", nil}},
		{"nothing names none", args{"", "", "", ""}, want{"", nil}},
		{"a relative env fails", args{home, "", "records", ""}, want{"", userconfig.ErrRecordDirRelative}},
		{"a relative config fails", args{home, "", "", "records"}, want{"", userconfig.ErrRecordDirRelative}},
		{"an absolute env passes over a relative config", args{home, "", "/env", "records"}, want{"/env", nil}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.home.RecordDir(tc.args.flag, tc.args.env, tc.args.configuration)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.dir, got)
		})
	}
}
