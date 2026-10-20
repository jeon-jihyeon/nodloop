package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// No classify trace of the reaction point holds the person's words or the answer they judged
// It keeps the HMAC of the state under the key the record directory keeps with mode 0600
func TestRunHookReactionKeepsNoWords(t *testing.T) {
	const words = "no, quote the refund window before the steps"
	tcs := []struct {
		name string
		// The probabilities the endpoint answers
		args string
		// The members the trace names
		want []string
	}{
		{"a sure endpoint answers alone", `{"answers":{"corrects":{"noul":0.95},"approves":{"noul":0.05}}}`, []string{"endpoint"}},
		{"an unsure endpoint defers to the conversation", `{"answers":{"corrects":{"noul":0.6},"approves":{"noul":0.4}}}`, []string{"endpoint", conversationMember}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, records, repo := hookHome(t, "use git -C")
			getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
			now := hookClock()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.args))
			}))
			t.Cleanup(srv.Close)
			require.Equal(t, 0, runClassifier([]string{"set", "reaction", "--url", srv.URL}, getenv, now, &bytes.Buffer{}, &bytes.Buffer{}))
			run := hookPromptSetup(t, getenv, now, repo, true, false, false)
			stdin := `{"session_id":"s1","cwd":"` + repo + `","prompt":"` + words + `"}`
			var stderr bytes.Buffer

			code := runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(stdin), &bytes.Buffer{}, &stderr)

			require.Equal(t, 0, code, stderr.String())
			store, err := tracefile.New(records)
			require.NoError(t, err)
			found, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameClassify)})
			require.NoError(t, err)
			require.Len(t, found, 1)
			answered, err := store.Get(context.Background(), run)
			require.NoError(t, err)
			key, err := textkey.Load(records)
			require.NoError(t, err)
			info, err := os.Stat(filepath.Join(records, textkey.FileName))
			require.NoError(t, err)
			raw, err := json.Marshal(found[0])
			require.NoError(t, err)
			var in classify.TraceInput
			require.NoError(t, json.Unmarshal(found[0].Input, &in))
			var out classify.TraceOutput
			require.NoError(t, json.Unmarshal(found[0].Output, &out))
			members := make([]string, 0, len(out.Members))
			for _, m := range out.Members {
				members = append(members, strings.Replace(m.Name, strings.TrimPrefix(srv.URL, "http://"), "endpoint", 1))
			}

			assert.Equal(t, string(classify.PointReaction), found[0].Subject)
			assert.NotContains(t, string(raw), "refund window", "no word of the person")
			assert.NotContains(t, string(raw), "done", "no word of the answer")
			assert.Equal(t, key.Sum(turn{run: trace.Restore(answered), message: words}.state()), in.State)
			assert.Equal(t, []any{"approves", "corrects"}, in.Questions)
			assert.Equal(t, tc.want, members)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		})
	}
}

// A key that cannot be read leaves the prompt without the reaction point so no text is hashed under a second key
func TestRunHookReactionWithoutKey(t *testing.T) {
	home, records, repo := hookHome(t, "use git -C")
	getenv := func(k string) string { return map[string]string{"HOME": home, envRecordDir: records}[k] }
	now := hookClock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"corrects":{"noul":0.95},"approves":{"noul":0.05}}}`))
	}))
	t.Cleanup(srv.Close)
	require.Equal(t, 0, runClassifier([]string{"set", "reaction", "--url", srv.URL}, getenv, now, &bytes.Buffer{}, &bytes.Buffer{}))
	hookPromptSetup(t, getenv, now, repo, true, false, false)
	require.NoError(t, os.Mkdir(filepath.Join(records, textkey.FileName), 0o700))
	var stderr bytes.Buffer

	code := runHook([]string{"prompt"}, getenv, nil, now, strings.NewReader(`{"session_id":"s1","cwd":"`+repo+`","prompt":"wrong"}`), &bytes.Buffer{}, &stderr)

	store, err := tracefile.New(records)
	require.NoError(t, err)
	found, err := store.List(context.Background(), record.TraceFilter{Name: string(trace.NameClassify)})
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Contains(t, stderr.String(), "nodloop hook: "+textkey.ErrRead.Error())
	assert.Empty(t, found)
}
