package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestShellWord(t *testing.T) {
	tcs := []struct {
		name string
		args string
		want string
	}{
		{"a plain path stays as it is", "/home/u/.nodloop/bin/nodloop", "/home/u/.nodloop/bin/nodloop"},
		{"a path with a space is quoted", "/Users/u/My Tools/nodloop", "'/Users/u/My Tools/nodloop'"},
		{"a quote in the path is escaped", "/tmp/it's/nodloop", `'/tmp/it'\''s/nodloop'`},
		{"a command separator is quoted", "/data/a;b", "'/data/a;b'"},
		{"an ampersand and a pipe are quoted", "/data/R&D|x", "'/data/R&D|x'"},
		{"parentheses and redirections are quoted", "/data/v(2)<in>", "'/data/v(2)<in>'"},
		{"glob characters are quoted", "/data/run[1]*?", "'/data/run[1]*?'"},
		{"a leading tilde and a hash are quoted", "~/data#1", "'~/data#1'"},
		{"a newline is quoted", "/data/a\nb", "'/data/a\nb'"},
		{"an empty path is quoted so it stays a word", "", "''"},
		{"other safe characters stay", "/data/v1.2_x-y/a@b%c+d=e,f:g", "/data/v1.2_x-y/a@b%c+d=e,f:g"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, shellWord(tc.args))
		})
	}
}

func TestRunMCP(t *testing.T) {
	tools := strings.Join(mcp.Tools(), "\n") + "\n"
	at := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	type args struct {
		args []string
		// Values with the `{home}` and `{records}` placeholders
		env map[string]string
	}
	type want struct {
		code int
		// Regexp matched against stderr
		stderr string
		stdout string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"list prints the tool names without opening any record", args{[]string{"--list"}, map[string]string{"HOME": "{home}"}}, want{0, `^$`, tools}},
		{"serves until stdin closes", args{nil, map[string]string{envRecordDir: "{records}"}}, want{0, `^$`, ""}},
		{"nothing configured still serves", args{nil, map[string]string{"HOME": "{home}"}}, want{0, `^$`, ""}},
		{"unknown flag fails", args{[]string{"--nope"}, nil}, want{1, "^flag provided but not defined: -nope\n", ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			stdout, err := os.Create(filepath.Join(t.TempDir(), "stdout"))
			require.NoError(t, err)
			r := strings.NewReplacer("{home}", home, "{records}", records)
			getenv := func(k string) string { return r.Replace(tc.args.env[k]) }
			var stderr bytes.Buffer

			got := runMCP(tc.args.args, getenv, func() time.Time { return at }, strings.NewReader(""), stdout, &stderr)

			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, tc.want.stderr, stderr.String())
			out, err := os.ReadFile(stdout.Name())
			require.NoError(t, err)
			assert.Equal(t, tc.want.stdout, string(out))
			_, err = os.Stat(homeDir(home).recordDir())
			assert.ErrorIs(t, err, os.ErrNotExist, "serving opens no record before a call")
		})
	}
}

func TestMCPOpen(t *testing.T) {
	const fix = `\. Fix what this names, or set --record-dir or NODLOOP_RECORD_DIR$`
	type args struct {
		// Values with the `{home}` and `{records}` placeholders
		env map[string]string
		// Files written under home before the call keyed by their relative path
		files map[string]string
	}
	tcs := []struct {
		name string
		args args
		// Regexp matched against the error or empty when the server opens
		want string
	}{
		{"records default under home", args{map[string]string{"HOME": "{home}"}, nil}, ""},
		{"the env names the records", args{map[string]string{envRecordDir: "{records}"}, nil}, ""},
		{"an old config with a data dir still opens", args{map[string]string{"HOME": "{home}"}, map[string]string{".nodloop/config.json": `{"file_dir":"/gone"}`}}, ""},
		{"a broken config is named", args{map[string]string{"HOME": "{home}"}, map[string]string{".nodloop/config.json": "{broken"}}, `^config.json is not valid JSON: .*` + fix},
		{"nothing configured without a home names the record dir", args{nil, nil}, `^home directory unknown`},
		{"a record dir that is a file is named", args{map[string]string{envRecordDir: "{records}/regular"}, nil}, "^record dir: "},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records := t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(records, "regular"), nil, 0o600))
			for rel, content := range tc.args.files {
				path := filepath.Join(home, rel)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}
			r := strings.NewReplacer("{home}", home, "{records}", records)
			now := func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
			o := mcpOpen{getenv: func(k string) string { return r.Replace(tc.args.env[k]) }, now: now, session: mcp.NewSession(now())}

			s, err := o.open(t.Context())

			if tc.want == "" {
				require.NoError(t, err)
				assert.NotNil(t, s)
				return
			}
			assert.Regexp(t, tc.want, err.Error())
		})
	}
}

// Every call reads the config of that moment so a record dir saved between two calls takes the next call with the same session
func TestHostOpensPerCall(t *testing.T) {
	home := homeDir(t.TempDir())
	moved := filepath.Join(t.TempDir(), "moved")
	getenv := func(k string) string { return map[string]string{"HOME": string(home)}[k] }
	now := func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	c := testkit.Connect(t, mcp.NewHost(mcpOpen{getenv: getenv, now: now, session: mcp.NewSession(now())}.open, "test", "").ServeTransport)
	run := func() {
		require.NoError(t, c.Run(t, "run", map[string]any{"producer": "session", "output": "x"}))
	}

	run()
	require.NoError(t, os.WriteFile(home.configPath(), []byte(`{"record_dir":"`+moved+`"}`), 0o600))
	run()

	sessions := map[string]bool{}
	for _, dir := range []string{home.recordDir(), moved} {
		traces, err := tracefile.New(dir)
		require.NoError(t, err)
		got, err := traces.List(t.Context(), trace.Filter{Name: trace.NameRun})
		require.NoError(t, err)
		require.Len(t, got, 1, dir)
		sessions[got[0].SessionID] = true
	}
	assert.Len(t, sessions, 1)
}
