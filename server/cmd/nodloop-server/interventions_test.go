package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The status and the body of a POST of the body to the path under the key
func post(t *testing.T, url, key, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	if key != "" {
		req.Header.Set("Authorization", "Bearer key-"+key)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	_ = res.Body.Close()
	return res.StatusCode, string(b)
}

// Only an approver key added with --person decides through POST /v1/interventions and every other key records a proposal
func TestServeHandlerInterventions(t *testing.T) {
	for name, stores := range backends(t) {
		t.Run(name, func(t *testing.T) {
			interventionsScenario(t, stores(t))
		})
	}
}

func interventionsScenario(t *testing.T, stores tenantStores) {
	config := serverConfig{Keys: []serverKey{
		hashed("bot", "acme", mcp.RoleProducer), held(hashed("ann", "acme", mcp.RoleApprover)), hashed("ci", "acme", mcp.RoleApprover),
		held(hashed("rev", "acme", mcp.RoleReviewer)),
	}}
	srv := httptest.NewServer(newServeHandler(stores, config, time.Now, io.Discard))
	t.Cleanup(srv.Close)
	bot, ann := dial(t, srv.URL, "bot"), dial(t, srv.URL, "ann")
	labels := map[string]any{"task": []any{"refund"}}
	run := call(t, bot, "run", map[string]any{"producer": "support-bot", "labels": labels, "output": "steps"})["trace_id"]
	call(t, bot, "feedback", map[string]any{"trace_id": run, "verdict": "reject", "reason": "the window was missing"})
	call(t, ann, "propose", map[string]any{"kind": "judgment", "content": "Quote the refund window", "from": run, "id": "window"})
	items := func() int {
		got, _ := call(t, bot, "knowledge_for", map[string]any{"producer": "support-bot", "labels": labels})["items"].([]any)
		return len(got)
	}
	keep := `{"action":"judge","target":"window:1","choice":"keep"}`
	decode := func(body string) mcp.InterveneAnswer {
		var a mcp.InterveneAnswer
		require.NoError(t, json.Unmarshal([]byte(body), &a), body)
		return a
	}

	ciStatus, ciBody := post(t, srv.URL+"/v1/interventions", "ci", keep)
	revStatus, revBody := post(t, srv.URL+"/v1/interventions", "rev", keep)
	before := items()
	annStatus, annBody := post(t, srv.URL+"/v1/interventions", "ann", keep)
	kept := items()
	var id struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(decode(annBody).Intervention, &id))
	backStatus, backBody := post(t, srv.URL+"/v1/interventions", "ann", `{"action":"reverse","target":"`+id.ID+`"}`)

	assert.Equal(t, []any{http.StatusOK, trace.AuthorityAgent}, []any{ciStatus, decode(ciBody).Authority}, "an approver key without --person records a proposal")
	assert.Equal(t, []any{http.StatusOK, trace.AuthorityAgent}, []any{revStatus, decode(revBody).Authority}, "a reviewer key held by a person records a proposal")
	assert.Equal(t, 0, before, "a proposal changes nothing a run receives")
	assert.Equal(t, []any{http.StatusOK, trace.AuthorityPerson, trace.ProofKey}, []any{annStatus, decode(annBody).Authority, decode(annBody).Proof})
	assert.NoError(t, schema.Validate(string(trace.NameIntervention), decode(annBody).Intervention))
	assert.Equal(t, 1, kept)
	assert.Equal(t, http.StatusOK, backStatus, backBody)
	assert.Equal(t, 0, items(), "the reverse takes the lesson back")
}

// POST /v1/interventions refuses a key without the role, a body that is no payload and a payload the records cannot carry out
func TestServeHandlerInterventionsRefuses(t *testing.T) {
	config := serverConfig{Keys: []serverKey{hashed("bot", "acme", mcp.RoleProducer), held(hashed("ann", "acme", mcp.RoleApprover))}}
	srv := httptest.NewServer(newServeHandler(fileTenants{base: t.TempDir()}, config, time.Now, io.Discard))
	t.Cleanup(srv.Close)
	type args struct {
		key, body string
	}
	type want struct {
		status int
		body   string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"an intervention needs a key", args{"", `{"action":"digest"}`}, want{http.StatusUnauthorized, "a nodloop server key is required"}},
		{"a producer may not intervene", args{"bot", `{"action":"digest"}`}, want{http.StatusForbidden, "the role producer may not intervene"}},
		{"a body that is no JSON", args{"ann", `{"action":`}, want{http.StatusBadRequest, errPayloadInvalid.Error()}},
		{"a field of no payload", args{"ann", `{"action":"digest","verdict":"keep"}`}, want{http.StatusBadRequest, errPayloadInvalid.Error()}},
		{"a knob outside its range", args{"ann", `{"action":"knob","target":"audit_tier1","value":0.9}`},
			want{http.StatusUnprocessableEntity, curate.ErrTargetInvalid.Error()}},
		{"a version the records do not hold", args{"ann", `{"action":"judge","target":"gone:1","choice":"keep"}`},
			want{http.StatusUnprocessableEntity, "not found"}},
		{"a digest of a person", args{"ann", `{"action":"digest"}`}, want{http.StatusOK, `"authority":"person"`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			status, body := post(t, srv.URL+"/v1/interventions", tc.args.key, tc.args.body)

			assert.Equal(t, tc.want.status, status, body)
			assert.Contains(t, body, tc.want.body)
		})
	}
}

// A failure of the stores answers an intervention without its cause
func TestServeHandlerInterventionsInternalError(t *testing.T) {
	srv := httptest.NewServer(newServeHandler(unopened{}, serverConfig{Keys: []serverKey{held(hashed("ann", "acme", mcp.RoleApprover))}}, time.Now, io.Discard))
	t.Cleanup(srv.Close)

	status, body := post(t, srv.URL+"/v1/interventions", "ann", `{"action":"digest"}`)

	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, "internal error\n", body)
}
