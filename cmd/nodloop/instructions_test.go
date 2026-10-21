package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

func TestInstructionsTrace(t *testing.T) {
	at := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	hmac := strings.Repeat("ab", 32)
	type args struct {
		in      instructionsInput
		session string
	}
	type want struct {
		input string
		err   error
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a rules file names its path, the HMAC of its content and what the host reported",
			args{instructionsInput{Path: "CLAUDE.md", HMAC: hmac, MemoryType: "Project", LoadReason: "session_start"}, "s1"},
			want{`{"path":"CLAUDE.md","hmac":"` + hmac + `","memory_type":"Project","load_reason":"session_start"}`, nil},
		},
		{"a file of no session is refused", args{instructionsInput{Path: "CLAUDE.md", HMAC: hmac}, ""}, want{err: errSessionTrace}},
		{"a file without its path is refused", args{instructionsInput{HMAC: hmac}, "s1"}, want{err: errSessionTrace}},
		{"a content kept as text is refused", args{instructionsInput{Path: "CLAUDE.md", HMAC: "always use git -C"}, "s1"}, want{err: errSessionTrace}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.in.trace(tc.args.session, at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.input, string(got.Input))
		})
	}
	got, err := instructionsInput{Path: "CLAUDE.md", HMAC: hmac}.trace("s1", at)
	require.NoError(t, err)
	assert.Equal(t, []any{trace.NameInstructions, "s1", at}, []any{got.Name, got.SessionID, got.Time})
	assert.NoError(t, schema.Validate(string(trace.NameInstructions), got.Record()))
}

// A home, a record directory with approved.md, a repository with the rules files of a project and an auto memory file under each config
// The first replacer fills the placeholders and the second puts them back
func instructionsHome(t *testing.T) (place, named *strings.Replacer) {
	t.Helper()
	home, records, config, other := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	repo := filepath.Join(t.TempDir(), "acme")
	link := filepath.Join(t.TempDir(), "records")
	require.NoError(t, os.Symlink(records, link))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "folder", "CLAUDE.md"), 0o755))
	for path, content := range map[string]string{
		filepath.Join(repo, "CLAUDE.md"):                                           "use git -C\n",
		filepath.Join(repo, "notes.md"):                                            "a note\n",
		filepath.Join(repo, ".claude", "rules", "go.md"):                           "run gofmt\n",
		filepath.Join(other, "CLAUDE.md"):                                          "written elsewhere\n",
		filepath.Join(records, "approved.md"):                                      "- use git -C\n",
		filepath.Join(home, ".claude", "CLAUDE.md"):                                "answer short\n",
		filepath.Join(home, ".claude", "projects", "-acme", "memory", "MEMORY.md"): "remembered\n",
		filepath.Join(home, ".claude", "projects", "-acme", "CLAUDE.md"):           "kept by the person\n",
		filepath.Join(config, "projects", "-acme", "memory", "notes.md"):           "remembered\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	dirs := []string{"{home}", home, "{records}", records, "{config}", config, "{repo}", repo, "{link}", link, "{other}", other}
	back := make([]string, 0, len(dirs))
	for i := 0; i < len(dirs); i += 2 {
		back = append(back, dirs[i+1], dirs[i])
	}
	return strings.NewReplacer(dirs...), strings.NewReplacer(back...)
}

// The session, path, memory type and load reason of every instructions trace oldest first with the directories named
func instructionsIn(t *testing.T, records string, named *strings.Replacer) []string {
	t.Helper()
	store, err := tracefile.New(records)
	require.NoError(t, err)
	found, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameInstructions)})
	require.NoError(t, err)
	out := []string{}
	for _, tr := range found {
		var in instructionsInput
		require.NoError(t, json.Unmarshal(tr.Input, &in))
		out = append([]string{named.Replace(fmt.Sprintf("%s %s %s %s", tr.SessionID, in.Path, in.MemoryType, in.LoadReason))}, out...)
	}
	return out
}

// The instructions hook records every rules file of the person a session loads and none whose lines no person confirmed
// A file the host never loads as the person's instructions is refused so no CLI call sets a file as their direction
func TestRunHookInstructions(t *testing.T) {
	type args struct {
		stdin string
		flags []string
		env   map[string]string
		// session_mode of config.json and none when empty
		mode string
	}
	type want struct {
		recorded []string
		stderr   string
	}
	load := func(path, memoryType string) string {
		return fmt.Sprintf(`{"session_id":"s1","cwd":"{repo}","hook_event_name":"InstructionsLoaded","file_path":%q,"memory_type":%q,"load_reason":"session_start"}`,
			path, memoryType)
	}
	none := []string{}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{
			"a CLAUDE.md the session loads is recorded with what the host reported",
			args{stdin: load("{repo}/CLAUDE.md", "Project")},
			want{[]string{"s1 {repo}/CLAUDE.md Project session_start"}, ""},
		},
		{"a rules file of the project is recorded", args{stdin: load("{repo}/.claude/rules/go.md", "Project")}, want{[]string{"s1 {repo}/.claude/rules/go.md Project session_start"}, ""}},
		{"a CLAUDE.md of the user is recorded", args{stdin: load("{home}/.claude/CLAUDE.md", "User")}, want{[]string{"s1 {home}/.claude/CLAUDE.md User session_start"}, ""}},
		{"approved.md of the record directory is left out", args{stdin: load("{records}/approved.md", "Project")}, want{none, ""}},
		{"approved.md reported through a link is left out", args{stdin: load("{link}/approved.md", "Project")}, want{none, ""}},
		{"an auto memory file under home is left out", args{stdin: load("{home}/.claude/projects/-acme/memory/MEMORY.md", "")}, want{none, ""}},
		{
			"an auto memory file under the Claude Code config directory is left out",
			args{stdin: load("{config}/projects/-acme/memory/notes.md", ""), env: map[string]string{envClaudeConfigDir: "{config}"}},
			want{none, ""},
		},
		{"a file the host reports as auto memory is left out", args{stdin: load("{repo}/notes.md", autoMemoryType)}, want{none, ""}},
		{"a file of another name is refused", args{stdin: load("{repo}/notes.md", "Project")}, want{none, errInstructionsForeign.Error()}},
		{"a CLAUDE.md outside the project and the user folder is refused", args{stdin: load("{other}/CLAUDE.md", "Project")}, want{none, errInstructionsForeign.Error()}},
		{
			"a CLAUDE.md of the projects directory is refused",
			args{stdin: load("{home}/.claude/projects/-acme/CLAUDE.md", "Project")},
			want{none, errInstructionsForeign.Error()},
		},
		{"a memory type of no person is refused", args{stdin: load("{repo}/CLAUDE.md", "Managed")}, want{none, errInstructionsForeign.Error()}},
		{"a file that cannot be read records nothing", args{stdin: load("{repo}/sub/CLAUDE.md", "Project")}, want{none, "no such file"}},
		{"a path that names no regular file records nothing and never waits", args{stdin: load("{repo}/folder/CLAUDE.md", "Project")}, want{none, atomicfile.ErrNotRegular.Error()}},
		{
			"a load of no session records nothing",
			args{stdin: `{"cwd":"{repo}","file_path":"{repo}/CLAUDE.md","memory_type":"Project"}`},
			want{none, errSessionTrace.Error()},
		},
		{"off records nothing", args{stdin: load("{repo}/CLAUDE.md", "Project"), mode: "off"}, want{none, ""}},
		{"Codex runs no instructions hook", args{stdin: load("{repo}/CLAUDE.md", ""), flags: []string{"--host", "codex"}}, want{none, "codex runs no hook instructions"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			place, named := instructionsHome(t)
			saveMode(t, place.Replace("{home}"), tc.args.mode)
			env := map[string]string{"HOME": place.Replace("{home}"), envRecordDir: place.Replace("{records}")}
			for k, v := range tc.args.env {
				env[k] = place.Replace(v)
			}
			var stdout, stderr bytes.Buffer

			code := runHook(append([]string{"instructions"}, tc.args.flags...), envOf(env), noStart, hookClock(), strings.NewReader(place.Replace(tc.args.stdin)), &stdout, &stderr)

			assert.Equal(t, 0, code)
			assert.Empty(t, stdout.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, tc.want.recorded, instructionsIn(t, place.Replace("{records}"), named))
		})
	}
}

// The content of a rules file is kept only as its HMAC under the key of the record directory
func TestRunHookInstructionsHMAC(t *testing.T) {
	place, _ := instructionsHome(t)
	records := place.Replace("{records}")
	env := envOf(map[string]string{"HOME": place.Replace("{home}"), envRecordDir: records})
	stdin := place.Replace(`{"session_id":"s1","cwd":"{repo}","file_path":"{repo}/CLAUDE.md","memory_type":"Project","load_reason":"include"}`)
	var stderr bytes.Buffer

	require.Equal(t, 0, runHook([]string{"instructions"}, env, noStart, hookClock(), strings.NewReader(stdin), &bytes.Buffer{}, &stderr))

	require.Empty(t, stderr.String())
	key, err := textkey.Load(records)
	require.NoError(t, err)
	store, err := tracefile.New(records)
	require.NoError(t, err)
	found, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameInstructions)})
	require.NoError(t, err)
	require.Len(t, found, 1)
	var in instructionsInput
	require.NoError(t, json.Unmarshal(found[0].Input, &in))
	assert.Equal(t, key.Sum("use git -C\n"), in.HMAC)
	assert.NotContains(t, string(found[0].Input), "use git -C")
	assert.NoError(t, schema.Validate(string(trace.NameInstructions), found[0]))
}

func TestRulesFilesUnconfirmed(t *testing.T) {
	type args struct {
		home, configDir, path, memoryType string
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"approved.md of the record directory", args{"/home/ann", "", "/records/approved.md", ""}, true},
		{"approved.md of another directory", args{"/home/ann", "", "/acme/approved.md", ""}, false},
		{"a memory folder under home", args{"/home/ann", "", "/home/ann/.claude/projects/-acme/memory/x.md", ""}, true},
		{"a memory folder deeper down", args{"/home/ann", "", "/home/ann/.claude/projects/-acme/memory/topics/x.md", ""}, true},
		{"a folder named memory right under projects", args{"/home/ann", "", "/home/ann/.claude/projects/memory/x.md", ""}, false},
		{"the config directory wins over home", args{"/home/ann", "/cfg", "/home/ann/.claude/projects/-acme/memory/x.md", ""}, false},
		{"no home leaves the memory type alone to tell", args{"", "", "/home/ann/.claude/projects/-acme/memory/x.md", ""}, false},
		{"no home still leaves out the auto memory type", args{"", "", "/acme/x.md", autoMemoryType}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRulesFiles("/records", homeDir(tc.args.home), tc.args.configDir, "/work/acme")
			assert.Equal(t, tc.want, r.unconfirmed(tc.args.path, tc.args.memoryType))
		})
	}
}

// The projects directory is under CLAUDE_CONFIG_DIR, else under .claude of home, and none without either
func TestClaudeProjects(t *testing.T) {
	type args struct {
		home, configDir string
	}
	tcs := []struct {
		name string
		args args
		want filePath
	}{
		{"home alone", args{"/home/ann", ""}, "/home/ann/.claude/projects"},
		{"the config directory wins over home", args{"/home/ann", "/cfg"}, "/cfg/projects"},
		{"neither names none", args{"", ""}, ""},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, claudeProjects(homeDir(tc.args.home), tc.args.configDir))
		})
	}
}

// The files Claude Code loads as instructions: CLAUDE.md and CLAUDE.local.md of the project tree and the user and their rules folders
// and a file one of them imports through an @path
func TestRulesFilesLoads(t *testing.T) {
	type args struct {
		home, configDir, project, path, memoryType string
	}
	// What the host reported of an import with the text of its parent written before the check
	// {tmp} is a folder of the test
	type include struct {
		reason, parent, text string
	}
	none := include{}
	imported := func(text string) include { return include{includeReason, "{tmp}/acme/CLAUDE.md", text} }
	tcs := []struct {
		name    string
		args    args
		include include
		want    bool
	}{
		{"a CLAUDE.md of the project", args{"/home/ann", "", "/work/acme", "/work/acme/CLAUDE.md", "Project"}, none, true},
		{"a CLAUDE.local.md of the project", args{"/home/ann", "", "/work/acme", "/work/acme/CLAUDE.local.md", "Local"}, none, true},
		{"a CLAUDE.md of a folder above", args{"/home/ann", "", "/work/acme", "/work/CLAUDE.md", "Project"}, none, true},
		{"a CLAUDE.md of a folder below", args{"/home/ann", "", "/work/acme", "/work/acme/svc/.claude/CLAUDE.md", "Project"}, none, true},
		{"a CLAUDE.md of a folder beside", args{"/home/ann", "", "/work/acme", "/work/other/CLAUDE.md", "Project"}, none, false},
		{"a CLAUDE.md of the user", args{"/home/ann", "", "/work/acme", "/home/ann/.claude/CLAUDE.md", "User"}, none, true},
		{"a CLAUDE.md of the config directory", args{"/home/ann", "/cfg", "/work/acme", "/cfg/CLAUDE.md", "User"}, none, true},
		{"a CLAUDE.md of home without a config", args{"", "", "/work/acme", "/home/ann/.claude/CLAUDE.md", "User"}, none, false},
		{"a rules file of the project", args{"/home/ann", "", "/work/acme", "/work/acme/.claude/rules/go.md", "Project"}, none, true},
		{"a rules file in a folder of the rules", args{"/home/ann", "", "/work/acme", "/work/acme/.claude/rules/lang/go.md", "Project"}, none, true},
		{"a rules file of the user", args{"/home/ann", "", "/work/acme", "/home/ann/.claude/rules/go.md", "User"}, none, true},
		{"a rules file of the config directory", args{"/home/ann", "/cfg", "/tmp/acme", "/cfg/rules/go.md", "User"}, none, true},
		{"a rules folder of no .claude folder", args{"/home/ann", "", "/work/acme", "/work/acme/rules/go.md", "Project"}, none, false},
		{"a rules folder beside the project", args{"/home/ann", "", "/work/acme", "/work/other/.claude/rules/go.md", "Project"}, none, false},
		{"a file of another name", args{"/home/ann", "", "/work/acme", "/work/acme/AGENTS.md", "Project"}, none, false},
		{"a file of no project", args{"/home/ann", "", "", "/work/acme/CLAUDE.md", "Project"}, none, false},
		{"managed instructions", args{"/home/ann", "", "/work/acme", "/work/acme/CLAUDE.md", "Managed"}, none, false},
		{"no memory type", args{"/home/ann", "", "/work/acme", "/work/acme/CLAUDE.md", ""}, none, false},
		{"an AGENTS.md a CLAUDE.md imports", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/AGENTS.md", "Project"}, imported("See @AGENTS.md\n"), true},
		{
			"an index file a CLAUDE.md imports from a folder",
			args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/docs/index.md", "Project"}, imported("- @docs/index.md\n"), true,
		},
		{"an import from home", args{"{tmp}/home", "", "{tmp}/acme", "{tmp}/home/notes.md", "Project"}, imported("@~/notes.md\n"), true},
		{
			"an import with an escaped space", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/Design Docs/api.md", "Project"},
			imported(`@Design\ Docs/api.md` + "\n"), true,
		},
		{
			"an import in a code span", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/AGENTS.md", "Project"},
			imported("keep `@AGENTS.md` literal\n"), false,
		},
		{
			"an import in a fenced code block", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/AGENTS.md", "Project"},
			imported("```\n@AGENTS.md\n```\n"), false,
		},
		{"an address that ends in the name", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/AGENTS.md", "Project"}, imported("ann@AGENTS.md\n"), false},
		{"a parent that imports another file", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/AGENTS.md", "Project"}, imported("@README.md\n"), false},
		{
			"a parent that is no instructions file", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/more.md", "Project"},
			include{includeReason, "{tmp}/acme/AGENTS.md", "@more.md\n"}, false,
		},
		{
			"a parent that is not there", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/AGENTS.md", "Project"},
			include{includeReason, "{tmp}/acme/CLAUDE.md", ""}, false,
		},
		{
			"a parent of another load reason", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/AGENTS.md", "Project"},
			include{"session_start", "{tmp}/acme/CLAUDE.md", "@AGENTS.md\n"}, false,
		},
		{
			"an import of managed instructions", args{"/home/ann", "", "{tmp}/acme", "{tmp}/acme/AGENTS.md", "Managed"},
			imported("@AGENTS.md\n"), false,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmp := strings.NewReplacer("{tmp}", t.TempDir())
			parent := tmp.Replace(tc.include.parent)
			if tc.include.text != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(parent), 0o700))
				require.NoError(t, os.WriteFile(parent, []byte(tc.include.text), 0o600))
			}
			r := newRulesFiles("/records", homeDir(tmp.Replace(tc.args.home)), tc.args.configDir, tmp.Replace(tc.args.project))

			got := r.loads(tmp.Replace(tc.args.path), tc.args.memoryType, tc.include.reason, parent)

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestInstructionsTracesLoaded(t *testing.T) {
	at := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	hmac := strings.Repeat("ab", 32)
	kept, err := instructionsInput{Path: "/acme/CLAUDE.md", HMAC: hmac, MemoryType: "Project"}.trace("s1", at)
	require.NoError(t, err)
	type want struct {
		loaded []curate.Loaded
		err    error
	}
	tcs := []struct {
		name string
		args instructionsTraces
		want want
	}{
		{
			"a trace gives the file with its session",
			instructionsTraces{kept},
			want{[]curate.Loaded{{Session: "s1", File: curate.AnchorFile{Path: "/acme/CLAUDE.md", HMAC: hmac, MemoryType: "Project"}}}, nil},
		},
		{"an input that does not decode fails", instructionsTraces{{ID: "t-1", Input: []byte(`[]`)}}, want{nil, errSessionTrace}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.loaded()

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.loaded, got)
		})
	}
}

// A record directory the hook cannot write records nothing and still exits 0
func TestRunHookInstructionsFailures(t *testing.T) {
	type args struct {
		// The record file and the text written to it before the hook
		file, text string
		// NODLOOP_RECORD_DIR as {records} or a {records}/file that is no directory
		records string
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"a key of another length", args{"hmac.key", "short", "{records}"}, "hmac.key"},
		{"a record directory that is a file", args{"file", "x", "{records}/file"}, "not a directory"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			place, _ := instructionsHome(t)
			records := place.Replace("{records}")
			require.NoError(t, os.WriteFile(filepath.Join(records, tc.args.file), []byte(tc.args.text), 0o600))
			env := envOf(map[string]string{"HOME": place.Replace("{home}"), envRecordDir: place.Replace(tc.args.records)})
			stdin := place.Replace(`{"session_id":"s1","cwd":"{repo}","file_path":"{repo}/CLAUDE.md","memory_type":"Project"}`)
			var stderr bytes.Buffer

			code := runHook([]string{"instructions"}, env, noStart, hookClock(), strings.NewReader(stdin), &bytes.Buffer{}, &stderr)

			assert.Equal(t, 0, code)
			assert.Contains(t, stderr.String(), tc.want)
			assert.NoFileExists(t, filepath.Join(records, "traces.jsonl"))
		})
	}
}
