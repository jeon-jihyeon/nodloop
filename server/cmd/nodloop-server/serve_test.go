package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/server/internal/pg"
)

// Adds the bearer key to every request of the client
type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

func hashed(name, tenant string, role mcp.Role) serverKey {
	sum := sha256.Sum256([]byte("key-" + name))
	return serverKey{Name: name, Tenant: tenant, Role: role, SHA256: hex.EncodeToString(sum[:])}
}

// A session of the key named name against the server
func dial(t *testing.T, url, name string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	s, err := c.Connect(t.Context(), &sdk.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{"key-" + name}}, MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func call(t *testing.T, s *sdk.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := s.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	b, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	require.False(t, res.IsError, "%s: %v", name, res.Content)
	return out
}

func tools(t *testing.T, s *sdk.ClientSession) []string {
	t.Helper()
	res, err := s.ListTools(t.Context(), nil)
	require.NoError(t, err)
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

// The stores a server keeps tenants in: files always and PostgreSQL with NODLOOP_TEST_POSTGRES
// Each test of Postgres takes tenants of its own so earlier runs never show
func backends(t *testing.T) map[string]func(t *testing.T) tenantStores {
	t.Helper()
	out := map[string]func(t *testing.T) tenantStores{
		"files": func(t *testing.T) tenantStores { return fileTenants{base: t.TempDir()} },
	}
	if dsn := os.Getenv("NODLOOP_TEST_POSTGRES"); dsn != "" {
		out["postgres"] = func(t *testing.T) tenantStores {
			db, err := pg.Open(context.Background(), dsn)
			require.NoError(t, err)
			t.Cleanup(db.Close)
			return prefixed{pgTenants{db: db}, fmt.Sprintf("t%d-", time.Now().UnixNano())}
		}
	}
	return out
}

// Tenants under a prefix so a shared database starts empty for each test
type prefixed struct {
	tenantStores
	prefix string
}

func (p prefixed) open(key serverKey, now func() time.Time, session string) mcp.Open {
	key.Tenant = p.prefix + key.Tenant
	return p.tenantStores.open(key, now, session)
}

func (p prefixed) reports(key serverKey, now func() time.Time) (loop.Stores, error) {
	key.Tenant = p.prefix + key.Tenant
	return p.tenantStores.reports(key, now)
}

// Each key sees the tools of its role, writes the records of its tenant and approves under its own name
func TestServeHandler(t *testing.T) {
	for name, stores := range backends(t) {
		t.Run(name, func(t *testing.T) {
			serveScenario(t, stores(t))
		})
	}
}

func serveScenario(t *testing.T, stores tenantStores) {
	config := serverConfig{Keys: []serverKey{
		hashed("bot", "acme", mcp.RoleProducer), hashed("ann", "acme", mcp.RoleApprover), hashed("globex-bot", "globex", mcp.RoleProducer),
	}}
	srv := httptest.NewServer(newServeHandler(stores, config, time.Now, io.Discard))
	t.Cleanup(srv.Close)
	bot, ann, globex := dial(t, srv.URL, "bot"), dial(t, srv.URL, "ann"), dial(t, srv.URL, "globex-bot")
	labels := map[string]any{"task": []any{"refund"}}

	run := call(t, bot, "run", map[string]any{"producer": "support-bot", "labels": labels, "output": "steps"})["trace_id"]
	call(t, bot, "feedback", map[string]any{"trace_id": run, "verdict": "reject", "reason": "the window was missing"})
	proposed := call(t, ann, "propose", map[string]any{"kind": "judgment", "content": "Quote the refund window", "from": run, "id": "window"})
	approved := call(t, ann, "approve", map[string]any{"id": "window", "version": proposed["version"], "approver": "mallory"})
	acmeItems := call(t, bot, "knowledge_for", map[string]any{"producer": "support-bot", "labels": labels})["items"]
	globexItems := call(t, globex, "knowledge_for", map[string]any{"producer": "support-bot", "labels": labels})["items"]

	assert.ElementsMatch(t, mcp.RoleProducer.Tools(), tools(t, bot))
	assert.Contains(t, tools(t, ann), "approve")
	assert.Equal(t, "ann", approved["approver"], "an approval is recorded under the name of the key")
	assert.Len(t, acmeItems, 1)
	assert.Empty(t, globexItems, "another tenant reads its own records")
}

// A request without a known key is refused before any tool runs
func TestServeHandlerUnauthorized(t *testing.T) {
	srv := httptest.NewServer(newServeHandler(fileTenants{base: t.TempDir()}, serverConfig{Keys: []serverKey{hashed("bot", "acme", mcp.RoleProducer)}}, time.Now, io.Discard))
	t.Cleanup(srv.Close)
	tcs := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"an unknown key", "Bearer key-mallory"},
		{"a key without the bearer scheme", "key-bot"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(`{}`))
			require.NoError(t, err)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}

			res, err := http.DefaultClient.Do(req)

			require.NoError(t, err)
			_ = res.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
		})
	}
}

// The HTTP API answers reports of the tenant of the key to a role that reads them and a probe without a key
func TestServeHandlerReports(t *testing.T) {
	config := serverConfig{Keys: []serverKey{
		hashed("bot", "acme", mcp.RoleProducer), hashed("ann", "acme", mcp.RoleApprover), hashed("rita", "acme", mcp.RoleReviewer),
		hashed("gus", "globex", mcp.RoleReviewer),
	}}
	srv := httptest.NewServer(newServeHandler(fileTenants{base: t.TempDir()}, config, time.Now, io.Discard))
	t.Cleanup(srv.Close)
	call(t, dial(t, srv.URL+"/mcp", "bot"), "run", map[string]any{"producer": "support-bot", "output": "steps"})
	type want struct {
		status int
		// A fragment of the body or the runs of a loop report when it answers one
		body string
		runs int
	}
	tcs := []struct {
		name string
		path string
		key  string
		want want
	}{
		{"healthz needs no key", "/healthz", "", want{status: http.StatusOK, body: "ok\n"}},
		{"an approver reads the report of its tenant", "/v1/reports/loop", "ann", want{status: http.StatusOK, runs: 1}},
		{"a reviewer reads reports", "/v1/reports/loop", "rita", want{status: http.StatusOK, runs: 1}},
		{"another tenant reads its own records", "/v1/reports/loop", "gus", want{status: http.StatusOK, runs: 0}},
		{"health is a report too", "/v1/reports/health", "ann", want{status: http.StatusOK, body: "[]\n"}},
		{"a producer may not read reports", "/v1/reports/loop", "bot", want{status: http.StatusForbidden, body: "the role producer may not read reports\n"}},
		{"an unknown report is not found", "/v1/reports/nope", "ann", want{status: http.StatusNotFound, body: "no such report"}},
		{"a report needs a key", "/v1/reports/loop", "", want{status: http.StatusUnauthorized, body: "a nodloop server key is required\n"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+tc.path, nil)
			require.NoError(t, err)
			if tc.key != "" {
				req.Header.Set("Authorization", "Bearer key-"+tc.key)
			}

			res, err := http.DefaultClient.Do(req)

			require.NoError(t, err)
			body, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			_ = res.Body.Close()
			require.Equal(t, tc.want.status, res.StatusCode, string(body))
			switch {
			case strings.HasSuffix(tc.path, "/loop") && res.StatusCode == http.StatusOK:
				var report loop.LoopReport
				require.NoError(t, json.Unmarshal(body, &report))
				assert.Equal(t, tc.want.runs, report.Totals.Runs)
			case tc.want.body != "":
				assert.Contains(t, string(body), tc.want.body)
			}
		})
	}
}

// A tenant directory is open to the server user alone
func TestFileTenantsReportsPrivateDir(t *testing.T) {
	base := t.TempDir()

	_, err := fileTenants{base: base}.reports(hashed("ann", "acme", mcp.RoleApprover), time.Now)

	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(base, "tenants", "acme"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// Tenant stores that never open as a record directory without permission would
type unopened struct{}

func (unopened) open(serverKey, func() time.Time, string) mcp.Open {
	return func(context.Context) (*mcp.Server, error) {
		return nil, errUnopened
	}
}

func (unopened) reports(serverKey, func() time.Time) (loop.Stores, error) {
	return loop.Stores{}, errUnopened
}

var errUnopened = errors.New("mkdir /srv/records/tenants/acme: permission denied")

// A failure of the server answers without its cause
// The cause goes to the log
func TestServeHandlerInternalError(t *testing.T) {
	var log bytes.Buffer
	srv := httptest.NewServer(newServeHandler(unopened{}, serverConfig{Keys: []serverKey{hashed("ann", "acme", mcp.RoleApprover)}}, time.Now, &log))
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/reports/loop", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer key-ann")

	res, err := http.DefaultClient.Do(req)

	require.NoError(t, err)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	_ = res.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, res.StatusCode)
	assert.Equal(t, "internal error\n", string(body))
	assert.Contains(t, log.String(), "key ann: mkdir /srv/records/tenants/acme: permission denied")
}

// serve refuses to start without a home since the keys live under it
func TestRunServeWithoutHome(t *testing.T) {
	var stderr bytes.Buffer

	code := run([]string{"serve"}, func(string) string { return "" }, time.Now, &bytes.Buffer{}, &stderr)

	assert.Equal(t, 1, code)
	assert.Equal(t, "nodloop-server serve: home directory unknown\n", stderr.String())
}
