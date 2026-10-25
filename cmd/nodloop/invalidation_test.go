package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestRunHookRetiredContext(t *testing.T) {
	tcs := []struct {
		name, source  string
		before, after []string
		want          bool
	}{
		{"an open context gets an invalidation", "", []string{"prompt", "stop"}, nil, true},
		{"retirement during the first turn", "", []string{"prompt"}, []string{"stop"}, true},
		{"retirement during a later turn", "", []string{"prompt", "stop", "prompt"}, []string{"stop"}, true},
		{"compaction reloads the current empty ledger", "compact", []string{"prompt", "stop"}, nil, false},
		{"clear reloads the current empty ledger", "clear", []string{"prompt", "stop"}, nil, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "Use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			at := curateNow
			now := func() time.Time { at = at.Add(time.Second); return at }
			var stdout, stderr bytes.Buffer
			input, err := json.Marshal(map[string]any{"session_id": "s1", "cwd": repo, "last_assistant_message": "done"})
			require.NoError(t, err)
			for _, action := range tc.before {
				require.Equal(t, 0, runHook([]string{action}, getenv, noStart, now, bytes.NewReader(input), &stdout, &stderr))
			}
			require.Equal(t, 0, runKnowledge([]string{"retire", "git-c", "--version", "1", "--approver", "ann"},
				getenv, nil, now, atTerminal, &stdout, &stderr), stderr.String())
			for _, action := range tc.after {
				require.Equal(t, 0, runHook([]string{action}, getenv, noStart, now, bytes.NewReader(input), &stdout, &stderr))
			}
			if tc.source != "" {
				start, err := json.Marshal(map[string]string{"session_id": "s1", "cwd": repo, "source": tc.source})
				require.NoError(t, err)
				require.Equal(t, 0, runHook([]string{"start"}, getenv, noStart, now, bytes.NewReader(start), &stdout, &stderr))
			}
			stdout.Reset()
			require.Equal(t, 0, runHook([]string{"prompt"}, getenv, noStart, now, bytes.NewReader(input), &stdout, &stderr))
			assert.Empty(t, stderr.String())
			assert.Equal(t, tc.want, strings.Contains(stdout.String(), "stop following inactive items git-c v1"))
			assert.NotContains(t, stdout.String(), "Use git -C")
			require.Equal(t, 0, runHook([]string{"stop"}, getenv, noStart, now, bytes.NewReader(input), &stdout, &stderr))
			stdout.Reset()
			require.Equal(t, 0, runHook([]string{"prompt"}, getenv, noStart, now, bytes.NewReader(input), &stdout, &stderr))
			assert.NotContains(t, stdout.String(), "stop following inactive items")
		})
	}
}

func TestPromptContextRefs(t *testing.T) {
	at := curateNow
	turn := trace.Trace{Name: trace.NameTurn, Time: at, Input: json.RawMessage(`{"context":[{"id":"k1","version":1}]}`)}
	refill := trace.Trace{ID: "f1", Name: trace.NameRefill, Time: at.Add(time.Second), Input: json.RawMessage(`{"applied":[]}`)}
	old := refill
	old.Time = at.Add(-time.Second)
	bad := refill
	bad.Input = json.RawMessage(`{"applied":7}`)
	legacy := trace.Trace{Name: trace.NameRun, Time: at, Input: json.RawMessage(`{"applied":[{"id":"k2","version":1}]}`)}
	type args struct {
		traces sessionTraces
		refill trace.Trace
	}
	tcs := []struct {
		name string
		args args
		want []knowledge.Ref
	}{
		{"a saved prompt keeps its references", args{traces: sessionTraces{turn}}, []knowledge.Ref{{ID: "k1", Version: 1}}},
		{"an empty refill resets the context", args{sessionTraces{turn}, refill}, []knowledge.Ref{}},
		{"an older refill cannot replace a prompt", args{sessionTraces{turn}, old}, []knowledge.Ref{{ID: "k1", Version: 1}}},
		{"an unreadable reset establishes no references", args{sessionTraces{turn}, bad}, nil},
		{"a legacy turn uses the recorded answer", args{traces: sessionTraces{{Name: trace.NameTurn, Input: json.RawMessage(`{}`)}, legacy}},
			[]knowledge.Ref{{ID: "k2", Version: 1}}},
		{"a new session has no earlier references", args{}, nil},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.traces.contextRefs(nil, tc.args.refill))
		})
	}
}

func TestPromptContextInvalidation(t *testing.T) {
	one := knowledge.Ref{ID: "k1", Version: 1}
	retired := knowledge.Set{{ID: one.ID, Version: one.Version, Status: knowledge.StatusRetired}}
	var many promptRefs
	var all knowledge.Set
	for i := range 30 {
		id := fmt.Sprintf("k-item-%d", i)
		many = append(many, knowledge.Ref{ID: id, Version: 1})
		all = append(all, knowledge.Knowledge{ID: id, Version: 1, Status: knowledge.StatusRetired})
	}
	type args struct {
		refs promptRefs
		all  knowledge.Set
	}
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"an empty context has no notice", args{}, ""},
		{"an unknown version establishes no retirement", args{refs: promptRefs{one}}, ""},
		{"an approved version needs no notice", args{promptRefs{one}, knowledge.Set{{ID: one.ID, Version: one.Version, Status: knowledge.StatusApproved}}}, ""},
		{"a retired version is named without lesson text", args{promptRefs{one}, retired},
			"nodloop: stop following inactive items k1 v1"},
		{"many retired versions stay in the note budget", args{many, all}, "nodloop: earlier items became inactive"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.args.refs.invalidated(tc.args.refs.retired(tc.args.all))
			if tc.want == "" {
				assert.Empty(t, got)
			} else {
				assert.True(t, strings.HasPrefix(got, tc.want), got)
				assert.LessOrEqual(t, len([]rune(got)), contextRunes-promptRunes)
			}
		})
	}
}

func TestPromptContextContinues(t *testing.T) {
	one := knowledge.Ref{ID: "k1", Version: 1}
	next := knowledge.Ref{ID: "k1", Version: 2}
	other := knowledge.Ref{ID: "k2", Version: 1}
	type args struct {
		prior promptRefs
		added []knowledge.Ref
		all   knowledge.Set
	}
	tcs := []struct {
		name string
		args args
		want []knowledge.Ref
	}{
		{"empty context stays an explicit empty list", args{}, []knowledge.Ref{}},
		{"items no longer fitting remain in context", args{prior: promptRefs{one}}, []knowledge.Ref{one}},
		{"new versions replace their old context reference", args{prior: promptRefs{one}, added: []knowledge.Ref{next, other}}, []knowledge.Ref{next, other}},
		{"a notified retired version leaves the checkpoint", args{prior: promptRefs{one},
			all: knowledge.Set{{ID: one.ID, Version: one.Version, Status: knowledge.StatusRetired}}}, []knowledge.Ref{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.prior.continued(tc.args.prior.retired(tc.args.all), tc.args.added))
		})
	}
}
