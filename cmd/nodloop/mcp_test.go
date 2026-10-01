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
	data := testkit.DemoDir(t)
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
		{"list prints the tool names without opening any data", args{[]string{"--list"}, map[string]string{"HOME": "{home}"}}, want{0, `^$`, tools}},
		{"serves until stdin closes", args{nil, map[string]string{envFileDir: data, envRecordDir: "{records}"}}, want{0, `^$`, ""}},
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
			_, err = os.Stat(homeDir(home).configPath())
			assert.ErrorIs(t, err, os.ErrNotExist)
			_, err = os.Stat(homeDir(home).recordDir())
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

// What a call answers when the config of that moment does not open
func TestMCPOpen(t *testing.T) {
	data := testkit.DemoDir(t)
	const (
		unset = `^` + envFileDir + ` is not set: run nodloop setup or set the variable\. Run .* setup --data-dir <dir>$`
		fix   = `\. Fix what this names or run .* setup --data-dir <dir> again$`
	)
	type args struct {
		// Values with the `{home}` and `{records}` placeholders
		env map[string]string
		// Files written under home before the call keyed by their relative path
		files map[string]string
	}
	type want struct {
		// Regexp matched against the error or empty when the server opens
		err string
		// The setup config under home after the call
		config string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"records default under home", args{map[string]string{"HOME": "{home}", envFileDir: data}, nil}, want{}},
		{"nothing configured names setup", args{map[string]string{"HOME": "{home}"}, nil}, want{err: unset}},
		{
			"broken config is kept and named",
			args{map[string]string{"HOME": "{home}"}, map[string]string{".nodloop/config.json": "{broken"}},
			want{err: `^config.json is not valid JSON: .*` + fix, config: "{broken"},
		},
		{
			"data dir that moved away is named",
			args{map[string]string{envFileDir: "{home}/moved", envRecordDir: "{records}"}, nil},
			want{err: `^evidence file source: stat {home}/moved: no such file or directory` + fix},
		},
		{
			"broken policy is named",
			args{map[string]string{envFileDir: "{home}/data", envRecordDir: "{records}"}, map[string]string{"data/policy.yaml": "version: v1\n  bad: [\n"}},
			want{err: `^analysis: policy is not valid yaml: .*` + fix},
		},
		{
			"config without a data dir names setup and is kept",
			args{map[string]string{"HOME": "{home}"}, map[string]string{".nodloop/config.json": `{"file_dir":""}`}},
			want{err: unset, config: `{"file_dir":""}`},
		},
		{"nothing configured without a home names setup", args{nil, nil}, want{err: unset}},
		{
			"record dir that is a file is named",
			args{map[string]string{envFileDir: data, envRecordDir: "{records}/regular"}, nil},
			want{err: "^record dir: .*" + fix},
		},
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

			config, _ := os.ReadFile(homeDir(home).configPath())
			assert.Equal(t, tc.want.config, string(config))
			if tc.want.err == "" {
				require.NoError(t, err)
				assert.NotNil(t, s)
				assert.DirExists(t, homeDir(home).recordDir())
				return
			}
			require.Error(t, err)
			assert.Regexp(t, r.Replace(tc.want.err), err.Error())
			assert.NotContains(t, err.Error(), "reconnect")
		})
	}
}

// A setup through the CLI and an edit of policy.yaml reach the next call of one connection
func TestHostOpensPerCall(t *testing.T) {
	home := t.TempDir()
	data1, data2 := filepath.Join(t.TempDir(), "data1"), filepath.Join(t.TempDir(), "data2")
	records2 := filepath.Join(t.TempDir(), "records2")
	for _, dir := range []string{data1, data2} {
		require.NoError(t, os.CopyFS(dir, os.DirFS(testkit.DemoDir(t))))
	}
	setPolicyVersion(t, data2, "demo-2")
	getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
	now := func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	open := mcpOpen{getenv: getenv, now: now, session: mcp.NewSession(now())}.open
	c := testkit.Connect(t, mcp.NewHost(open, "test").ServeTransport)
	setup := func(args ...string) {
		var out, log bytes.Buffer
		require.Equal(t, 0, runSetup(args, getenv, &out, &log), log.String())
	}
	observe := func() string {
		var got struct {
			Version string `json:"policy_version"`
		}
		require.NoError(t, c.Call(t, "observe", map[string]any{"event_id": "tq-005"}, &got))
		return got.Version
	}

	unset := c.Run(t, "events", map[string]any{})
	setup("--data-dir", data1)
	first := observe()
	require.NoError(t, c.Run(t, "context", map[string]any{"event_id": "tq-005"}))
	setPolicyVersion(t, data1, "demo-1b")
	edited := observe()
	setup("--data-dir", data2, "--record-dir", records2)
	moved := observe()
	require.NoError(t, c.Run(t, "context", map[string]any{"event_id": "tq-005"}))

	assert.ErrorContains(t, unset, "setup --data-dir <dir>")
	assert.NotContains(t, unset.Error(), "reconnect")
	assert.Equal(t, []string{"demo-1", "demo-1b", "demo-2"}, []string{first, edited, moved})
	sessions := map[string]bool{}
	for _, dir := range []string{homeDir(home).recordDir(), records2} {
		traces, err := tracefile.New(dir)
		require.NoError(t, err)
		got, err := traces.List(t.Context(), trace.Filter{Name: trace.NameContext})
		require.NoError(t, err)
		require.Len(t, got, 1, dir)
		sessions[got[0].SessionID] = true
	}
	assert.Len(t, sessions, 1)
}

func setPolicyVersion(t *testing.T, dir, version string) {
	t.Helper()
	path := filepath.Join(dir, policyFile)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	_, rest, ok := strings.Cut(string(b), "\n")
	require.True(t, ok)
	require.NoError(t, os.WriteFile(path, []byte("version: "+version+"\n"+rest), 0o600))
}
