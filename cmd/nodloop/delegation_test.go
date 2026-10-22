package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestRunConfigDelegation(t *testing.T) {
	type args struct {
		flags []string
		proof trace.Proof
	}
	type want struct {
		code, count int
		error       string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a terminal delegates a recorded place", args{[]string{"add", "--producer", "session", "--label", "repo=acme"}, atTerminal}, want{0, 1, ""}},
		{"a terminal explicitly delegates every place", args{[]string{"add", "--producer", "session", "--everywhere"}, atTerminal}, want{0, 1, ""}},
		{"an agent cannot delegate", args{[]string{"add", "--producer", "session", "--label", "repo=acme"}, trace.ProofNone}, want{1, 0, "only a person"}},
		{"an elicitation proof is not a terminal setting", args{[]string{"add", "--producer", "session", "--label", "repo=acme"}, trace.ProofElicitation}, want{1, 0, "only a person"}},
		{"a global scope must be explicit", args{[]string{"add", "--producer", "session"}, atTerminal}, want{1, 0, "explicit everywhere"}},
		{"labels and everywhere exclude each other", args{[]string{"add", "--producer", "session", "--everywhere", "--label", "repo=acme"}, atTerminal}, want{1, 0, "explicit everywhere"}},
		{"an unobserved producer fails", args{[]string{"add", "--producer", "other", "--everywhere"}, atTerminal}, want{1, 0, "no recorded run"}},
		{"an unobserved label fails", args{[]string{"add", "--producer", "session", "--label", "repo=other"}, atTerminal}, want{1, 0, "no run"}},
		{"a host is not a delegated place", args{[]string{"add", "--producer", "session", "--label", "host=claude-code"}, atTerminal}, want{1, 0, "not a delegation scope"}},
		{"a correlation is not a delegated place", args{[]string{"add", "--producer", "session", "--label", "correlation=C1"}, atTerminal}, want{1, 0, "not a delegation scope"}},
		{"an empty label fails", args{[]string{"add", "--producer", "session", "--label", "repo="}, atTerminal}, want{1, 0, "empty"}},
		{"an invalid action fails", args{[]string{"change"}, atTerminal}, want{1, 0, "unknown action"}},
		{"an unknown flag fails", args{[]string{"add", "--unknown"}, atTerminal}, want{1, 0, "flag provided"}},
		{"extra positional arguments fail", args{[]string{"add", "extra"}, atTerminal}, want{1, 0, "unexpected argument"}},
		{"the default list grants nothing", args{nil, trace.ProofNone}, want{0, 0, ""}},
		{"removing a scope never granted fails", args{[]string{"remove", "--producer", "session", "--label", "repo=acme"}, atTerminal},
			want{1, 0, "no saved delegation"}},
		{"an agent cannot remove", args{[]string{"remove", "--producer", "session", "--label", "repo=acme"}, trace.ProofNone},
			want{1, 0, "only a person"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, _ := curateHome(t, nil)
			h := homeDir(getenv("HOME"))
			require.NoError(t, h.config().Save(map[string]any{"unrelated": "kept"}))
			var stdout, stderr bytes.Buffer
			code := runConfig(append([]string{"delegation"}, tc.args.flags...), getenv, tc.args.proof, &stdout, &stderr)
			saved, err := h.readConfig()
			require.NoError(t, err)
			assert.Equal(t, tc.want.code, code, stderr.String())
			assert.Len(t, saved.Curation.Delegations, tc.want.count)
			assert.Contains(t, stderr.String(), tc.want.error)
			var other struct {
				Unrelated string `json:"unrelated"`
			}
			require.NoError(t, h.config().Read(&other))
			assert.Equal(t, "kept", other.Unrelated)
		})
	}
}

func TestRunConfigDelegationRenew(t *testing.T) {
	getenv, _ := curateHome(t, nil)
	var stdout, stderr bytes.Buffer
	flags := []string{"delegation", "add", "--producer", "session", "--label", "repo=acme"}
	require.Equal(t, 0, runConfig(flags, getenv, atTerminal, &stdout, &stderr), stderr.String())
	h := homeDir(getenv("HOME"))
	first, err := h.readConfig()
	require.NoError(t, err)
	require.Len(t, first.Curation.Delegations, 1)
	require.NotEmpty(t, first.Curation.Delegations[0].ID)
	require.Equal(t, 0, runConfig(flags, getenv, atTerminal, &stdout, &stderr), stderr.String())
	second, err := h.readConfig()
	require.NoError(t, err)
	require.Len(t, second.Curation.Delegations, 1)
	assert.NotEqual(t, first.Curation.Delegations[0].ID, second.Curation.Delegations[0].ID)
	flags[1] = "remove"
	require.Equal(t, 0, runConfig(flags, getenv, atTerminal, &stdout, &stderr), stderr.String())
	last, err := h.readConfig()
	require.NoError(t, err)
	assert.Empty(t, last.Curation.Delegations)
}

func TestConfigDelegations(t *testing.T) {
	type want struct {
		count int
		err   error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"a grant the person added", `{"delegations":[{"id":"g1","producer":"session","labels":{"repo":["acme"]}}]}`, want{1, nil}},
		{"a label retention pruned since the grant", `{"delegations":[{"id":"g1","producer":"session","labels":{"repo":["other"]}}]}`,
			want{1, nil}},
		{"a missing grant", `{"delegations":[{"producer":"session","labels":{"repo":["acme"]}}]}`, want{0, errRequired}},
		{"an implicit global scope", `{"delegations":[{"id":"g1","producer":"session"}]}`, want{0, errRequired}},
		{"a host scope", `{"delegations":[{"id":"g1","producer":"session","labels":{"host":["claude-code"]}}]}`,
			want{0, errLabelFlag}},
		{"an empty label", `{"delegations":[{"id":"g1","producer":"session","labels":{"repo":[""]}}]}`,
			want{0, trace.ErrLabelEmpty}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			getenv, records := curateHome(t, nil)
			require.NoError(t, homeDir(getenv("HOME")).config().Save(map[string]any{"curation": json.RawMessage(tc.args)}))
			a, err := (recordFlags{recordDir: records}).app(getenv, func() time.Time { return curateNow })
			require.NoError(t, err)

			grants, err := a.cfg.delegations()

			assert.ErrorIs(t, err, tc.want.err)
			assert.Len(t, grants, tc.want.count)
		})
	}
}
