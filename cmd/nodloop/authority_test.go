package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The commands that decide for a person carry out what they record only with the terminal proof
// Without it they record an agent proposal and change nothing
func TestRunKnowledgeJudgmentProof(t *testing.T) {
	type args struct {
		proof trace.Proof
		// approver of config.json and none when empty
		saved   string
		command []string
		// approved.md of the records turned into a directory so the export after the write fails
		blocked bool
	}
	type want struct {
		code   int
		stdout string
		stderr string
		// The newest status of git-c v1 and v2 afterwards
		statuses []string
		// The authority, proof, person and choice of each intervention after the approval of the setup
		interventions []string
	}
	unchanged := []string{"v1 approved", "v2 candidate"}
	proposal := func(command string) want {
		return want{stdout: "proposal\tgit-c\t", statuses: unchanged, interventions: []string{"agent none  " + command}}
	}
	none := []string{}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"approve from a script records a proposal", args{trace.ProofNone, "", []string{"approve", "git-c", "--version", "2", "--approver", "ann"}, false},
			proposal("keep")},
		{"approve at the terminal names the person given", args{atTerminal, "", []string{"approve", "git-c", "--version", "2", "--approver", "ann"}, false},
			want{stdout: "git-c\tv2\tapproved\tann", statuses: []string{"v1 superseded", "v2 approved"}, interventions: []string{"person terminal ann keep"}}},
		{"approve at the terminal falls back to the saved name", args{atTerminal, "sam", []string{"approve", "git-c", "--version", "2"}, false},
			want{stdout: "git-c\tv2\tapproved\tsam", statuses: []string{"v1 superseded", "v2 approved"}, interventions: []string{"person terminal sam keep"}}},
		{"approve at the terminal with no name is the person's", args{atTerminal, "", []string{"approve", "git-c", "--version", "2"}, false},
			want{stdout: "git-c\tv2\tapproved\tperson", statuses: []string{"v1 superseded", "v2 approved"}, interventions: []string{"person terminal person keep"}}},
		{"approve everywhere from a script records a scope proposal", args{trace.ProofNone, "", []string{"approve", "git-c", "--version", "2", "--everywhere"}, false},
			proposal("scope")},
		{"retire from a script records a proposal", args{trace.ProofNone, "", []string{"retire", "git-c", "--version", "1"}, false}, proposal("undo")},
		{"reaffirm from a script records a proposal", args{trace.ProofNone, "", []string{"reaffirm", "git-c"}, false}, proposal("keep")},
		{"reaffirm at the terminal decides", args{atTerminal, "", []string{"reaffirm", "git-c", "--approver", "ann"}, false},
			want{stdout: "git-c\tv1\treaffirmed\tann", statuses: unchanged, interventions: []string{"person terminal ann keep"}}},
		{"the reserved name is refused at the terminal", args{atTerminal, "", []string{"approve", "git-c", "--version", "2", "--approver", "nodloop"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"the reserved name is refused from a script", args{trace.ProofNone, "", []string{"retire", "git-c", "--version", "1", "--approver", "nodloop"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"the reserved name is refused for an approval from a script", args{trace.ProofNone, "", []string{"approve", "git-c", "--version", "2", "--approver", "nodloop"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"the reserved name is refused for an approval in labels at the terminal",
			args{atTerminal, "", []string{"approve", "git-c", "--version", "2", "--everywhere", "--approver", "nodloop"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"the reserved name is refused for a retire at the terminal", args{atTerminal, "", []string{"retire", "git-c", "--version", "1", "--approver", "nodloop"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"the reserved name is refused for a reaffirm at the terminal", args{atTerminal, "", []string{"reaffirm", "git-c", "--approver", "nodloop"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"the reserved name is refused for a reaffirm from a script", args{trace.ProofNone, "", []string{"reaffirm", "git-c", "--approver", "nodloop"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"the reserved name is refused for approve-compaction at the terminal", args{atTerminal, "", []string{"approve-compaction", "c-nope", "--approver", "nodloop"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"the reserved name is refused for approve-compaction from a script", args{trace.ProofNone, "", []string{"approve-compaction", "c-nope", "--approver", "nodloop"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"a reserved name saved by hand in config.json is refused at the terminal", args{atTerminal, "nodloop", []string{"approve", "git-c", "--version", "2"}, false},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"a proposal of an unknown version is refused", args{trace.ProofNone, "", []string{"approve", "git-c", "--version", "9"}, false},
			want{code: 1, stderr: knowledge.ErrNotFound.Error(), statuses: unchanged, interventions: none}},
		{"a proposal of an unknown compaction is refused", args{trace.ProofNone, "", []string{"approve-compaction", "c-nope"}, false},
			want{code: 1, stderr: knowledge.ErrNotFound.Error(), statuses: unchanged, interventions: none}},
		{"an approval without a version is refused", args{trace.ProofNone, "", []string{"approve", "git-c"}, false},
			want{code: 1, stderr: "approve: an id and --version is required", statuses: unchanged, interventions: none}},
		{"a reaffirm without an id is refused", args{atTerminal, "", []string{"reaffirm"}, false},
			want{code: 1, stderr: "reaffirm: an id is required", statuses: unchanged, interventions: none}},
		{"a reaffirm of an id with no approved version is refused", args{atTerminal, "", []string{"reaffirm", "git-x"}, false},
			want{code: 1, stderr: "git-x", statuses: unchanged, interventions: none}},
		{"a reaffirm proposal of a version that is not approved is refused", args{trace.ProofNone, "", []string{"reaffirm", "git-c", "--version", "2"}, false},
			want{code: 1, stderr: knowledge.ErrVersionUnapproved.Error(), statuses: unchanged, interventions: none}},
		{"a reaffirm at the terminal of a version that is not approved writes nothing", args{atTerminal, "", []string{"reaffirm", "git-c", "--version", "2"}, false},
			want{code: 1, stderr: knowledge.ErrVersionUnapproved.Error(), statuses: unchanged, interventions: none}},
		{"an import from a script is refused", args{trace.ProofNone, "", []string{"import", "--file", "{records}/knowledge.jsonl"}, false},
			want{code: 1, stderr: errPersonOnly.Error(), statuses: unchanged, interventions: none}},
		{"an import of no regular file is refused at the terminal", args{atTerminal, "", []string{"import", "--file", "{records}"}, false},
			want{code: 1, stderr: atomicfile.ErrNotRegular.Error(), statuses: unchanged, interventions: none}},
		{"an approval whose export fails still prints and records its intervention", args{atTerminal, "", []string{"approve", "git-c", "--version", "2", "--approver", "ann"}, true},
			want{code: 1, stdout: "git-c\tv2\tapproved\tann", stderr: knowledge.ErrExport.Error(), statuses: []string{"v1 superseded", "v2 approved"},
				interventions: []string{"person terminal ann keep"}}},
		{"a reaffirm whose export fails still prints and records its intervention", args{atTerminal, "", []string{"reaffirm", "git-c", "--approver", "ann"}, true},
			want{code: 1, stdout: "git-c\tv1\treaffirmed\tann", stderr: knowledge.ErrExport.Error(), statuses: unchanged, interventions: []string{"person terminal ann keep"}}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, _ := hookHome(t, "use git -C")
			if tc.args.saved != "" {
				saveConfig(t, home, map[string]any{"approver": tc.args.saved})
			}
			if tc.args.blocked {
				require.NoError(t, os.Remove(knowledgefile.RulesPath(records)))
				require.NoError(t, os.Mkdir(knowledgefile.RulesPath(records), 0o755))
			}
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			proposeNext(t, getenv)
			command := strings.Split(strings.ReplaceAll(strings.Join(tc.args.command, "\x00"), "{records}", records), "\x00")
			var stdout, stderr bytes.Buffer

			code := runKnowledge(command, getenv, nil, time.Now, tc.args.proof, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stdout.String(), tc.want.stdout)
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, tc.want.statuses, statusesOf(t, records, "git-c"))
			assert.Equal(t, tc.want.interventions, interventionsIn(t, records)[1:])
		})
	}
}

// config approver names the person
// So it is saved only with the terminal proof and never as the reserved name
func TestRunConfigApproverProof(t *testing.T) {
	type args struct {
		proof trace.Proof
		name  string
	}
	type want struct {
		code   int
		stderr string
		saved  string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a name from a script is refused", args{trace.ProofNone, "ann"}, want{1, errPersonOnly.Error(), ""}},
		{"a name at the terminal is saved", args{atTerminal, "ann"}, want{0, "", "ann"}},
		{"the reserved name is refused", args{atTerminal, "nodloop"}, want{1, knowledge.ErrApproverReserved.Error(), ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			var stdout, stderr bytes.Buffer

			code := runConfig([]string{"approver", tc.args.name}, func(k string) string { return map[string]string{"HOME": home}[k] }, tc.args.proof, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			uc, err := homeDir(home).readConfig()
			require.NoError(t, err)
			assert.Equal(t, tc.want.saved, uc.Approver)
		})
	}
}

// Who answers for the person and whether their reactions are recorded change only with the terminal proof
// 1. llm set and unset pick the backend the judge of curate runs on
// 2. classifier set and unset of the judge and the reaction point pick who predicts the person, while the critic point stays open
// 3. config session_mode saves whether the reactions of the person are recorded and prints from anywhere
func TestRunPersonSettingsProof(t *testing.T) {
	type args struct {
		proof trace.Proof
		// The command and its words
		command []string
	}
	type want struct {
		code   int
		stderr string
		// config.json after the call where the home starts with {"approver":"ann"}
		saved string
	}
	start := `{"approver":"ann"}`
	refused := want{1, errPersonOnly.Error(), start}
	chat := []string{"llm", "set", "chat", "--base-url", "http://192.0.2.1/v1", "--model", "acme-model"}
	judge := []string{"classifier", "set", "judge", "--url", "http://192.0.2.1/v1/classify"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"llm set from a script is refused", args{trace.ProofNone, chat}, refused},
		{"llm set claude from a script is refused", args{trace.ProofNone, []string{"llm", "set", "claude"}}, refused},
		{"llm unset from a script is refused", args{trace.ProofNone, []string{"llm", "unset"}}, refused},
		{"llm set at the terminal is saved", args{atTerminal, chat}, want{0, "",
			"{\n  \"approver\": \"ann\",\n  \"llm\": {\n    \"backend\": \"chat\",\n    \"base_url\": \"http://192.0.2.1/v1\",\n    \"model\": \"acme-model\"\n  }\n}\n"}},
		{"classifier set judge from a script is refused", args{trace.ProofNone, judge}, refused},
		{"classifier unset judge from a script is refused", args{trace.ProofNone, []string{"classifier", "unset", "judge"}}, refused},
		{"classifier set reaction from a script is refused", args{trace.ProofNone, []string{"classifier", "set", "reaction", "--url", "http://192.0.2.1/v1/classify"}}, refused},
		{"classifier unset reaction from a script is refused", args{trace.ProofNone, []string{"classifier", "unset", "reaction"}}, refused},
		{"classifier set judge at the terminal is saved", args{atTerminal, judge}, want{0, "",
			"{\n  \"approver\": \"ann\",\n  \"decision_points\": {\n    \"judge\": {\n      \"url\": \"http://192.0.2.1/v1/classify\"\n    }\n  }\n}\n"}},
		{"classifier set critic from a script is saved", args{trace.ProofNone, []string{"classifier", "set", "critic", "--url", "http://192.0.2.1/v1/classify"}}, want{0, "",
			"{\n  \"approver\": \"ann\",\n  \"decision_points\": {\n    \"critic\": {\n      \"url\": \"http://192.0.2.1/v1/classify\"\n    }\n  }\n}\n"}},
		{"config session_mode from a script is refused", args{trace.ProofNone, []string{"config", "session_mode", "off"}}, refused},
		{"config session_mode at the terminal is saved", args{atTerminal, []string{"config", "session_mode", "off"}}, want{0, "",
			"{\n  \"approver\": \"ann\",\n  \"session_mode\": \"off\"\n}\n"}},
		{"config session_mode from a script prints", args{trace.ProofNone, []string{"config", "session_mode"}}, want{0, "", start}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			hookSaveConfig(t, home, start)
			getenv := envOf(map[string]string{"HOME": home})
			var stderr bytes.Buffer
			commands := map[string]func([]string) int{
				"llm": func(a []string) int { return runLLM(a, getenv, nil, time.Now, tc.args.proof, &bytes.Buffer{}, &stderr) },
				"classifier": func(a []string) int {
					return runClassifier(a, getenv, time.Now, tc.args.proof, &bytes.Buffer{}, &stderr)
				},
				"config": func(a []string) int { return runConfig(a, getenv, tc.args.proof, &bytes.Buffer{}, &stderr) },
			}

			code := commands[tc.args.command[0]](tc.args.command[1:])

			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Contains(t, stderr.String(), tc.want.stderr)
			saved, err := os.ReadFile(homeDir(home).config().ConfigPath())
			require.NoError(t, err)
			assert.Equal(t, tc.want.saved, string(saved))
		})
	}
}

// A stream that is no terminal or the environment of an agent session proves no person
// A terminal itself needs a person to stand behind it and a test cannot be one
func TestTerminalProof(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	type args struct {
		stdin, stdout any
		env           map[string]string
	}
	tcs := []struct {
		name string
		args args
		want trace.Proof
	}{
		{"buffers are no terminal", args{&bytes.Buffer{}, &bytes.Buffer{}, nil}, trace.ProofNone},
		{"a pipe is no terminal", args{r, w, nil}, trace.ProofNone},
		{"nothing is no terminal", args{nil, nil, nil}, trace.ProofNone},
		{"an agent session proves no person", args{r, w, map[string]string{"CLAUDECODE": "1"}}, trace.ProofNone},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, terminalProof(tc.args.stdin, tc.args.stdout, func(k string) string { return tc.args.env[k] }))
		})
	}
}

// Writes config.json under home with the keys given
func saveConfig(t *testing.T, home string, keys map[string]any) {
	t.Helper()
	b, err := json.Marshal(keys)
	require.NoError(t, err)
	path := filepath.Join(home, ".nodloop", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, b, 0o600))
}

// Proposes v2 of git-c from the run its v1 was taught by
func proposeNext(t *testing.T, getenv func(string) string) {
	t.Helper()
	store, err := tracefile.New(getenv(envRecordDir))
	require.NoError(t, err)
	runs, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameRun), Limit: 1})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	var stderr bytes.Buffer
	require.Equal(t, 0, runKnowledge([]string{
		"propose", "--id", "git-c", "--kind", "judgment", "--content", "use git -C and never cd", "--trace", runs[0].ID,
		"--producer", sessionProducer, "--label", "repo=nodloop",
	}, getenv, nil, time.Now, atTerminal, &bytes.Buffer{}, &stderr), stderr.String())
}

// The newest status of each version of the id in version order
func statusesOf(t *testing.T, records, id string) []string {
	t.Helper()
	store, err := knowledgefile.New(records)
	require.NoError(t, err)
	list, err := store.List(context.Background())
	require.NoError(t, err)
	versions := knowledge.RestoreAll(list).Versions()
	slices.SortFunc(versions, func(a, b knowledge.Knowledge) int { return a.Version - b.Version })
	var out []string
	for _, k := range versions {
		if k.ID == id {
			out = append(out, fmt.Sprintf("v%d %s", k.Version, k.Status))
		}
	}
	return out
}

// The authority, proof, person and choice of every intervention recorded oldest first
func interventionsIn(t *testing.T, records string) []string {
	t.Helper()
	store, err := tracefile.New(records)
	require.NoError(t, err)
	found, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameIntervention)})
	require.NoError(t, err)
	out := []string{}
	for _, tr := range slices.Backward(found) {
		var in trace.Intervention
		require.NoError(t, json.Unmarshal(tr.Input, &in))
		out = append(out, fmt.Sprintf("%s %s %s %s", in.Authority, in.Proof, in.Person, in.Choice))
	}
	return out
}
