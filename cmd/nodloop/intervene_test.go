package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/atomicfile"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// nodloop intervene decides at once only from a person's terminal and records a proposal from a script
// The payload of a file or of stdin takes the flags given over it
func TestRunIntervene(t *testing.T) {
	type args struct {
		proof   trace.Proof
		command []string
		// The payload file --payload {payload} names and stdin for -
		payload string
		saved   string
	}
	type want struct {
		code   int
		stdout []string
		stderr string
		// The newest status of git-c v1 afterwards
		status knowledge.Status
	}
	undo := []string{"judge", "--target", "git-c:1", "--choice", "undo"}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an undo at the terminal retires the lesson under the person's name", args{proof: atTerminal, command: undo},
			want{stdout: []string{"\tjudge\tperson\tterminal\n", "wrote\tgit-c\tv1\tretired\n"}, status: knowledge.StatusRetired}},
		{"an undo at the terminal names the saved approver", args{proof: atTerminal, command: undo, saved: "sam"},
			want{stdout: []string{"wrote\tgit-c\tv1\tretired\n"}, status: knowledge.StatusRetired}},
		{"an undo from a script records a proposal and changes nothing", args{proof: trace.ProofNone, command: undo},
			want{stdout: []string{"\tjudge\tagent\tnone\n", "proposal\trecorded as a proposal"}, status: knowledge.StatusApproved}},
		{"a payload file", args{proof: atTerminal, command: []string{"--payload", "{payload}"},
			payload: `{"action":"judge","target":"git-c:1","choice":"undo"}`}, want{stdout: []string{"wrote\tgit-c\tv1\tretired\n"}, status: knowledge.StatusRetired}},
		{"a payload on stdin with the action given over it", args{proof: atTerminal, command: []string{"judge", "--payload", "-"},
			payload: `{"action":"digest","target":"git-c:1","choice":"undo"}`}, want{stdout: []string{"wrote\tgit-c\tv1\tretired\n"}, status: knowledge.StatusRetired}},
		{"a digest prints the cursor since the last decision", args{proof: trace.ProofNone, command: []string{"digest"}},
			want{stdout: []string{"since\t", "page the records"}, status: knowledge.StatusApproved}},
		{"a pause with labels", args{proof: atTerminal, command: []string{"pause", "--label", "repo=nodloop"}},
			want{stdout: []string{"\tpause\tperson\tterminal\n"}, status: knowledge.StatusApproved}},
		{"a payload that is no JSON is refused", args{proof: atTerminal, command: []string{"--payload", "{payload}"}, payload: `{"action":`},
			want{code: 1, stderr: errPayload.Error(), status: knowledge.StatusApproved}},
		{"a payload with a field of no payload is refused", args{proof: atTerminal, command: []string{"--payload", "{payload}"}, payload: `{"act":"judge"}`},
			want{code: 1, stderr: errPayload.Error(), status: knowledge.StatusApproved}},
		{"a file payload with a second object is refused", args{proof: atTerminal, command: []string{"--payload", "{payload}"},
			payload: `{"action":"judge","target":"git-c:1","choice":"undo"} {"action":"digest"}`},
			want{code: 1, stderr: errPayload.Error(), status: knowledge.StatusApproved}},
		{"a stdin payload with trailing garbage is refused", args{proof: atTerminal, command: []string{"--payload", "-"},
			payload: `{"action":"judge","target":"git-c:1","choice":"undo"} invalid`},
			want{code: 1, stderr: errPayload.Error(), status: knowledge.StatusApproved}},
		{"a complete stdin payload past the limit is refused", args{proof: atTerminal, command: []string{"--payload", "-"},
			payload: `{"action":"judge","target":"git-c:1","choice":"undo"}` + strings.Repeat(" ", inputLimit)},
			want{code: 1, stderr: errPayload.Error(), status: knowledge.StatusApproved}},
		{"a payload file that is missing is refused", args{proof: atTerminal, command: []string{"--payload", "{payload}.gone"}},
			want{code: 1, stderr: "no such file", status: knowledge.StatusApproved}},
		{"a payload that names no regular file is refused", args{proof: atTerminal, command: []string{"--payload", os.DevNull}},
			want{code: 1, stderr: atomicfile.ErrNotRegular.Error(), status: knowledge.StatusApproved}},
		{"a payload on stdin past the limit fails to decode", args{proof: atTerminal, command: []string{"--payload", "-"},
			payload: `{"action":"judge","target":"` + strings.Repeat("x", inputLimit) + `"}`}, want{code: 1, stderr: errPayload.Error(), status: knowledge.StatusApproved}},
		{"a knob outside its range is refused", args{proof: atTerminal, command: []string{"knob", "--target", "audit_tier1", "--value", "0.9"}},
			want{code: 1, stderr: curate.ErrTargetInvalid.Error(), status: knowledge.StatusApproved}},
		{"a value that is no number is refused", args{proof: atTerminal, command: []string{"knob", "--target", "audit_tier1", "--value", "high"}},
			want{code: 1, stderr: "invalid value", status: knowledge.StatusApproved}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, _ := hookHome(t, "use git -C")
			if tc.args.saved != "" {
				saveConfig(t, home, map[string]any{"approver": tc.args.saved})
			}
			payload := filepath.Join(t.TempDir(), "payload.json")
			if tc.args.payload != "" {
				require.NoError(t, os.WriteFile(payload, []byte(tc.args.payload), 0o600))
			}
			command := strings.Split(strings.ReplaceAll(strings.Join(tc.args.command, "\x00"), "{payload}", payload), "\x00")
			getenv := envOf(map[string]string{"HOME": home, envRecordDir: records})
			var stdout, stderr bytes.Buffer

			code := runIntervene(command, getenv, hookClock(), tc.args.proof, strings.NewReader(tc.args.payload), &stdout, &stderr)

			assert.Equal(t, tc.want.code, code, stderr.String())
			for _, line := range tc.want.stdout {
				assert.Contains(t, stdout.String(), line)
			}
			assert.Contains(t, stderr.String(), tc.want.stderr)
			assert.Equal(t, tc.want.status, newestStatus(t, records, "git-c", 1))
		})
	}
}

// The newest status of a version in the records
func newestStatus(t *testing.T, records, id string, version int) knowledge.Status {
	t.Helper()
	store, err := knowledgefile.New(records)
	require.NoError(t, err)
	all, err := knowledge.NewLedger(store, vetofile.NewApprovedFile("", ""), hookClock(), knowledge.NewIDs(hookClock())).All(context.Background())
	require.NoError(t, err)
	k, err := all.Version(id, version)
	require.NoError(t, err)
	return k.Status
}

// A record directory that cannot be made fails the command before anything is read
func TestRunInterveneFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	type args struct {
		env     map[string]string
		command []string
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"a record dir that is a file", args{map[string]string{"HOME": t.TempDir(), envRecordDir: file}, []string{"digest"}}, "not a directory"},
		{"an unknown flag", args{map[string]string{"HOME": t.TempDir()}, []string{"digest", "--nope"}}, "flag provided but not defined"},
		{"a relative record dir", args{map[string]string{"HOME": t.TempDir(), envRecordDir: "relative"}, []string{"digest"}}, "relative"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer

			code := runIntervene(tc.args.command, envOf(tc.args.env), hookClock(), atTerminal, strings.NewReader(""), &bytes.Buffer{}, &stderr)

			assert.NotEqual(t, 0, code)
			assert.Contains(t, stderr.String(), tc.want)
		})
	}
}
