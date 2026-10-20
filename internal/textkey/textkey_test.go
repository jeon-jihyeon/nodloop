package textkey_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/textkey"
)

// The environment that names the directory a process of TestLoadInAnotherProcess loads the key from
const envDir = "NODLOOP_TEXTKEY_DIR"

func TestLoad(t *testing.T) {
	fixed := bytes.Repeat([]byte{7}, 32)
	// A failed load returns the zero key
	zero := textkey.Key{}.Sum("refund window")
	type args struct {
		// Writes the record directory before the load and returns it
		dir func(t *testing.T) string
	}
	type want struct {
		// The HMAC of the text refund window
		sum string
		err error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a key the directory holds is read", args{func(t *testing.T) string {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, textkey.FileName), fixed, 0o600))
			return dir
		}}, want{sum: "f61d88b7bd64d1721a978366d6a551864aa1324b581003d903be4226851c0b09"}},
		{"a key cut short is refused", args{func(t *testing.T) string {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, textkey.FileName), fixed[:31], 0o600))
			return dir
		}}, want{zero, textkey.ErrInvalid}},
		{"a key that cannot be read is refused", args{func(t *testing.T) string {
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, textkey.FileName), 0o700))
			return dir
		}}, want{zero, textkey.ErrRead}},
		{"a directory that does not exist creates no key", args{func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "missing")
		}}, want{zero, textkey.ErrCreate}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := tc.args.dir(t)

			k, err := textkey.Load(dir)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.sum, k.Sum("refund window"))
		})
	}
}

// The first load creates 32 bytes of mode 0600 and leaves no temp file and a second load hashes under the same key
func TestLoadCreates(t *testing.T) {
	dir := t.TempDir()

	first, err := textkey.Load(dir)
	require.NoError(t, err)
	second, err := textkey.Load(dir)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(dir, textkey.FileName))
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Equal(t, int64(32), info.Size())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Len(t, entries, 1)
	assert.Equal(t, first.Sum("refund window"), second.Sum("refund window"))
	assert.NotEqual(t, first.Sum("refund window"), first.Sum("refund windows"))
	assert.Len(t, first.Sum(""), 64)
}

// Processes that create the key at once all hash under the key the directory keeps
func TestLoadAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	outs := make([]bytes.Buffer, 8)
	cmds := make([]*exec.Cmd, len(outs))
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestLoadInAnotherProcess$")
		cmds[i].Env = append(os.Environ(), envDir+"="+dir)
		cmds[i].Stdout = &outs[i]
		require.NoError(t, cmds[i].Start())
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "start"), nil, 0o600))
	sums := map[string]int{}
	for i, cmd := range cmds {
		require.NoError(t, cmd.Wait(), outs[i].String())
		sums[strings.SplitN(outs[i].String(), "\n", 2)[0]]++
	}

	k, err := textkey.Load(dir)
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{k.Sum("refund window"): len(cmds)}, sums)
	assert.Len(t, entries, 2, "the key and the start file and no temp file")
}

// Loads the key of the directory TestLoadAcrossProcesses names once its start file exists and prints the HMAC
func TestLoadInAnotherProcess(t *testing.T) {
	dir := os.Getenv(envDir)
	if dir == "" {
		t.Skip("a process of TestLoadAcrossProcesses")
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, err := os.Stat(filepath.Join(dir, "start")); err != nil && time.Now().Before(deadline); _, err = os.Stat(filepath.Join(dir, "start")) {
		time.Sleep(time.Millisecond)
	}

	k, err := textkey.Load(dir)

	require.NoError(t, err)
	fmt.Println(k.Sum("refund window"))
}
