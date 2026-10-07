package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/pg"
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
	srv := httptest.NewServer(newServeHandler(stores, config, time.Now))
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
	srv := httptest.NewServer(newServeHandler(fileTenants{base: t.TempDir()}, serverConfig{Keys: []serverKey{hashed("bot", "acme", mcp.RoleProducer)}}, time.Now))
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

// The HTTP API answers reports to a key whose role reads them and a probe without a key
func TestServeHandlerReports(t *testing.T) {
	config := serverConfig{Keys: []serverKey{hashed("bot", "acme", mcp.RoleProducer), hashed("ann", "acme", mcp.RoleApprover)}}
	srv := httptest.NewServer(newServeHandler(fileTenants{base: t.TempDir()}, config, time.Now))
	t.Cleanup(srv.Close)
	call(t, dial(t, srv.URL+"/mcp", "bot"), "run", map[string]any{"producer": "support-bot", "output": "steps"})
	type want struct {
		status int
		// A fragment of the body
		body string
	}
	tcs := []struct {
		name string
		path string
		key  string
		want want
	}{
		{"healthz needs no key", "/healthz", "", want{http.StatusOK, "ok"}},
		{"a report of the tenant of the key", "/v1/reports/loop", "ann", want{http.StatusOK, `"totals":{"runs":1,`}},
		{"health is a report too", "/v1/reports/health", "ann", want{http.StatusOK, `[]`}},
		{"a producer may not read reports", "/v1/reports/loop", "bot", want{http.StatusForbidden, "may not read reports"}},
		{"an unknown report is not found", "/v1/reports/nope", "ann", want{http.StatusNotFound, "no such report"}},
		{"a report needs a key", "/v1/reports/loop", "", want{http.StatusUnauthorized, "key is required"}},
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
			assert.Equal(t, tc.want.status, res.StatusCode, string(body))
			assert.Contains(t, string(body), tc.want.body)
		})
	}
}

func TestRunServerKey(t *testing.T) {
	type args struct {
		setup [][]string
		args  []string
	}
	type want struct {
		code int
		// stdout with {key} for a printed key
		stdout string
		stderr string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a new key is printed once", args{nil, []string{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}}, want{0, "{key}\n", ""}},
		{"keys list without their secret", args{[][]string{{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}}, []string{"key", "list"}},
			want{0, "ann\tacme\tapprover\n", ""}},
		{"a removed key leaves the list", args{[][]string{{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}, {"key", "remove", "ann"}}, []string{"key", "list"}},
			want{0, "", ""}},
		{"a name is used once", args{[][]string{{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}}, []string{"key", "add", "ann", "--tenant", "acme", "--role", "producer"}},
			want{1, "", "nodloop server: a server key of that name exists: ann"}},
		{"a tenant is one path element", args{nil, []string{"key", "add", "ann", "--tenant", "../etc", "--role", "approver"}}, want{1, "", "nodloop server: invalid tenant"}},
		{"an unknown role is refused", args{nil, []string{"key", "add", "ann", "--tenant", "acme", "--role", "admin"}}, want{1, "", "nodloop server: invalid role"}},
		{"an unknown key is not removed", args{nil, []string{"key", "remove", "ann"}}, want{1, "", "nodloop server: no server key of that name: ann"}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			getenv := func(k string) string { return map[string]string{"HOME": home}[k] }
			for _, args := range tc.args.setup {
				var stderr bytes.Buffer
				require.Equal(t, 0, runServer(args, getenv, time.Now, &bytes.Buffer{}, &stderr), stderr.String())
			}
			var stdout, stderr bytes.Buffer

			code := runServer(tc.args.args, getenv, time.Now, &stdout, &stderr)

			assert.Equal(t, tc.want.code, code)
			key := strings.TrimSpace(stdout.String())
			assert.Equal(t, strings.ReplaceAll(tc.want.stdout, "{key}", key), stdout.String())
			assert.True(t, strings.HasPrefix(stderr.String(), tc.want.stderr), stderr.String())
		})
	}
}

// config.json keeps the hash of a new key and never the key it printed
func TestServerKeySavedAsHash(t *testing.T) {
	home := homeDir(t.TempDir())
	getenv := func(k string) string { return map[string]string{"HOME": string(home)}[k] }
	var stdout bytes.Buffer
	require.Equal(t, 0, runServer([]string{"key", "add", "ann", "--tenant", "acme", "--role", "approver"}, getenv, time.Now, &stdout, &bytes.Buffer{}))
	key := strings.TrimSpace(stdout.String())

	uc, err := home.readConfig()

	require.NoError(t, err)
	sum := sha256.Sum256([]byte(key))
	assert.True(t, strings.HasPrefix(key, "nl_"))
	assert.Equal(t, []serverKey{{Name: "ann", Tenant: "acme", Role: mcp.RoleApprover, SHA256: hex.EncodeToString(sum[:])}}, uc.Server.Keys)
	saved, err := os.ReadFile(home.configPath())
	require.NoError(t, err)
	assert.NotContains(t, string(saved), key)
}
