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
		{"approve from a script records a proposal", args{trace.ProofNone, "", []string{"approve", "git-c", "--version", "2", "--approver", "ann"}},
			proposal("keep")},
		{"approve at the terminal names the person given", args{atTerminal, "", []string{"approve", "git-c", "--version", "2", "--approver", "ann"}},
			want{stdout: "git-c\tv2\tapproved\tann", statuses: []string{"v1 superseded", "v2 approved"}, interventions: []string{"person terminal ann keep"}}},
		{"approve at the terminal falls back to the saved name", args{atTerminal, "sam", []string{"approve", "git-c", "--version", "2"}},
			want{stdout: "git-c\tv2\tapproved\tsam", statuses: []string{"v1 superseded", "v2 approved"}, interventions: []string{"person terminal sam keep"}}},
		{"approve at the terminal with no name is the person's", args{atTerminal, "", []string{"approve", "git-c", "--version", "2"}},
			want{stdout: "git-c\tv2\tapproved\tperson", statuses: []string{"v1 superseded", "v2 approved"}, interventions: []string{"person terminal person keep"}}},
		{"approve everywhere from a script records a scope proposal", args{trace.ProofNone, "", []string{"approve", "git-c", "--version", "2", "--everywhere"}},
			proposal("scope")},
		{"retire from a script records a proposal", args{trace.ProofNone, "", []string{"retire", "git-c", "--version", "1"}}, proposal("undo")},
		{"reaffirm from a script records a proposal", args{trace.ProofNone, "", []string{"reaffirm", "git-c"}}, proposal("keep")},
		{"reaffirm at the terminal decides", args{atTerminal, "", []string{"reaffirm", "git-c", "--approver", "ann"}},
			want{stdout: "git-c\tv1\treaffirmed\tann", statuses: unchanged, interventions: []string{"person terminal ann keep"}}},
		{"the reserved name is refused at the terminal", args{atTerminal, "", []string{"approve", "git-c", "--version", "2", "--approver", "nodloop"}},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"the reserved name is refused from a script", args{trace.ProofNone, "", []string{"retire", "git-c", "--version", "1", "--approver", "nodloop"}},
			want{code: 1, stderr: knowledge.ErrApproverReserved.Error(), statuses: unchanged, interventions: none}},
		{"a proposal of an unknown version is refused", args{trace.ProofNone, "", []string{"approve", "git-c", "--version", "9"}},
			want{code: 1, stderr: knowledge.ErrNotFound.Error(), statuses: unchanged, interventions: none}},
		{"a proposal of an unknown compaction is refused", args{trace.ProofNone, "", []string{"approve-compaction", "c-nope"}},
			want{code: 1, stderr: knowledge.ErrNotFound.Error(), statuses: unchanged, interventions: none}},
		{"an import from a script is refused", args{trace.ProofNone, "", []string{"import", "--file", "{records}/knowledge.jsonl"}},
			want{code: 1, stderr: errPersonOnly.Error(), statuses: unchanged, interventions: none}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, _ := hookHome(t, "use git -C")
			if tc.args.saved != "" {
				saveConfig(t, home, map[string]any{"approver": tc.args.saved})
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
