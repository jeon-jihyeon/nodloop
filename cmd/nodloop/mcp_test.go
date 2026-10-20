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
	"github.com/jeon-jihyeon/nodloop/internal/record"
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
	agentTools := strings.Join(mcp.RoleReviewer.Tools(), "\n") + "\n"
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
		{"the agent server lists the reviewer role and no tool that decides", args{[]string{"--agent", "--list"}, map[string]string{"HOME": "{home}"}},
			want{0, `^$`, agentTools}},
		{"the agent server serves until stdin closes", args{[]string{"--agent"}, map[string]string{envRecordDir: "{records}"}}, want{0, `^$`, ""}},
		{"serves until stdin closes", args{nil, map[string]string{envRecordDir: "{records}"}}, want{0, `^$`, ""}},
		{"nothing configured still serves", args{nil, map[string]string{"HOME": "{home}"}}, want{0, `^$`, ""}},
		{
			"an OTLP endpoint serves without an export before a verdict",
			args{nil, map[string]string{envRecordDir: "{records}", envOTLPEndpoint: "http://192.0.2.1:4318"}},
			want{0, `^$`, ""},
		},
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
			var starts [][]string
			start := func(args []string) error {
				starts = append(starts, args)
				return nil
			}
			var stderr bytes.Buffer

			got := runMCP(tc.args.args, getenv, start, func() time.Time { return at }, strings.NewReader(""), stdout, &stderr)
			out, err := os.ReadFile(stdout.Name())
			require.NoError(t, err)
			_, statErr := os.Stat(homeDir(home).config().Records())

			assert.Equal(t, tc.want.code, got)
			assert.Regexp(t, tc.want.stderr, stderr.String())
			assert.Equal(t, tc.want.stdout, string(out))
			assert.ErrorIs(t, statErr, os.ErrNotExist, "serving opens no record before a call")
			assert.Empty(t, starts, "no export starts before a verdict")
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
	require.NoError(t, os.WriteFile(home.config().ConfigPath(), []byte(`{"record_dir":"`+moved+`"}`), 0o600))
	run()

	sessions := map[string]bool{}
	for _, dir := range []string{home.config().Records(), moved} {
		traces, err := tracefile.New(dir)
		require.NoError(t, err)
		got, err := traces.List(t.Context(), record.TraceFilter{Name: string(trace.NameRun)})
		require.NoError(t, err)
		require.Len(t, got, 1, dir)
		sessions[got[0].SessionID] = true
	}
	assert.Len(t, sessions, 1)
}

// The MCP run tool starts the daily prune of config retention and knowledge_for draws with config holdout
func TestMCPOpenPolicy(t *testing.T) {
	getenv, records := pruneHome(t, "30")
	home := getenv("HOME")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".nodloop", "config.json"), []byte(`{"retention":30,"holdout":0.999}`), 0o600))
	now := func() time.Time { return pruneNow }
	var log bytes.Buffer
	open := mcpOpen{getenv: getenv, now: now, session: mcp.NewSession(now()), log: &log}.open
	c := testkit.Connect(t, mcp.NewHost(open, "test", "").ServeTransport)
	var answer struct {
		Withheld []any `json:"withheld"`
	}

	require.NoError(t, c.Run(t, "run", map[string]any{"producer": "bot", "output": "x"}))
	require.NoError(t, c.Call(t, "knowledge_for", map[string]any{"producer": "bot", "holdout_key": "window-1"}, &answer))

	assert.Empty(t, log.String())
	assert.NotContains(t, traceIDs(t, records), "old-unjudged")
	assert.NotNil(t, answer.Withheld, "the holdout of config.json draws the call")
}

// A verdict through the MCP server starts the pending export of its record directory and a run starts none
// A start that fails goes to the log and the verdict still stands
func TestMCPOpenExport(t *testing.T) {
	type args struct {
		// The otel key of config.json and none when empty
		saved string
		// The error of the starter
		err error
	}
	type want struct {
		starts int
		// The log after the verdict
		log string
	}
	saved := `{"endpoint":"http://192.0.2.1:4318"}`
	failed := "nodloop: export: " + errExecutableTemporary.Error() + "\n"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a run and a verdict each start the export", args{saved, nil}, want{2, ""}},
		{"no saved target starts nothing", args{"", nil}, want{0, ""}},
		{"a start that fails is logged", args{saved, errExecutableTemporary}, want{2, failed + failed}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			records, home := t.TempDir(), t.TempDir()
			exportConfig(t, home, tc.args.saved)
			getenv := func(k string) string { return map[string]string{envRecordDir: records, "HOME": home}[k] }
			now := func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
			var log bytes.Buffer
			var starts [][]string
			start := func(args []string) error {
				starts = append(starts, args)
				return tc.args.err
			}
			open := mcpOpen{getenv: getenv, now: now, session: mcp.NewSession(now()), log: &log, export: start}.open
			c := testkit.Connect(t, mcp.NewHost(open, "test", "").ServeTransport)
			var run struct {
				TraceID string `json:"trace_id"`
			}

			require.NoError(t, c.Call(t, "run", map[string]any{"producer": "bot", "output": "x"}, &run))
			require.NoError(t, c.Run(t, "feedback", map[string]any{"trace_id": run.TraceID, "verdict": "approve", "reviewer": "ann"}))

			assert.Len(t, starts, tc.want.starts)
			for _, s := range starts {
				assert.Equal(t, []string{"export", "otel", "--pending", "--record-dir", records}, s)
			}
			assert.Equal(t, tc.want.log, log.String())
		})
	}
}
