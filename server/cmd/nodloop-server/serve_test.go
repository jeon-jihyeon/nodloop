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
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
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

func (p prefixed) reports(ctx context.Context, key serverKey, now func() time.Time) (loop.Stores, error) {
	key.Tenant = p.prefix + key.Tenant
	return p.tenantStores.reports(ctx, key, now)
}

func (p prefixed) retainer(tenant string, now func() time.Time) (*retain.Retainer, error) {
	return p.tenantStores.retainer(p.prefix+tenant, now)
}

func (p prefixed) marks(tenant string) (retain.Marks, error) {
	return p.tenantStores.marks(p.prefix + tenant)
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

	_, err := fileTenants{base: base}.reports(context.Background(), hashed("ann", "acme", mcp.RoleApprover), time.Now)

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

func (unopened) reports(context.Context, serverKey, func() time.Time) (loop.Stores, error) {
	return loop.Stores{}, errUnopened
}

func (unopened) retainer(string, func() time.Time) (*retain.Retainer, error) {
	return nil, errUnopened
}

func (unopened) marks(string) (retain.Marks, error) {
	return nil, errUnopened
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

// serve refuses a policy of config.json and an address it cannot listen on before it serves
func TestRunServeRefuses(t *testing.T) {
	type args struct {
		config string
		addr   string
	}
	keys := `"server":{"keys":[{"name":"ann","tenant":"acme","role":"approver","sha256":"00"}]}`
	tcs := []struct {
		name string
		args args
		want string
	}{
		{"a retention under a week", args{`{"retention":3,` + keys + `}`, "127.0.0.1:0"}, retain.ErrDaysTooShort.Error()},
		{"a holdout of 1", args{`{"holdout":1,` + keys + `}`, "127.0.0.1:0"}, knowledge.ErrHoldoutInvalid.Error()},
		{"an address it cannot listen on", args{`{"retention":30,` + keys + `}`, "127.0.0.1:-1"}, "listen tcp"},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".nodloop"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(home, ".nodloop", "config.json"), []byte(tc.args.config), 0o600))
			var stderr bytes.Buffer
			getenv := func(k string) string { return map[string]string{"HOME": home}[k] }

			code := run([]string{"serve", "--addr", tc.args.addr, "--record-dir", t.TempDir()}, getenv, time.Now, &bytes.Buffer{}, &stderr)

			assert.Equal(t, 1, code)
			assert.Contains(t, stderr.String(), tc.want)
		})
	}
}

// A pruner of the file backend never creates the directory of a tenant without records
func TestPrunerSkipsATenantWithoutRecords(t *testing.T) {
	base := t.TempDir()
	var log bytes.Buffer
	p := pruner{stores: fileTenants{base: base}, tenants: []string{"acme"}, days: 30, now: time.Now, log: &log}

	p.once(context.Background())

	assert.Empty(t, log.String())
	assert.NoDirExists(t, filepath.Join(base, "tenants", "acme"))
}

// The holdout share the stores carry reaches knowledge_for of every key
func TestServeHandlerHoldout(t *testing.T) {
	stores := fileTenants{base: t.TempDir(), policy: mcp.Policy{Holdout: 0.999}}
	config := serverConfig{Keys: []serverKey{hashed("ann", "acme", mcp.RoleApprover)}}
	srv := httptest.NewServer(newServeHandler(stores, config, time.Now, io.Discard))
	t.Cleanup(srv.Close)
	ann := dial(t, srv.URL, "ann")
	run := call(t, ann, "run", map[string]any{"producer": "bot", "output": "steps"})["trace_id"]
	call(t, ann, "feedback", map[string]any{"trace_id": run, "verdict": "reject", "reason": "no window"})
	call(t, ann, "propose", map[string]any{"id": "window", "kind": "judgment", "content": "quote the window", "from": run})
	call(t, ann, "approve", map[string]any{"id": "window", "version": 1, "approver": "ann"})

	got := call(t, ann, "knowledge_for", map[string]any{"producer": "bot", "holdout_key": "window-1"})

	assert.Equal(t, []any{map[string]any{"id": "window", "version": float64(1)}}, got["withheld"])
	assert.Empty(t, got["items"])
}

// A daily prune removes the old unjudged runs of each tenant and never a run of another tenant
func TestPrunerOnce(t *testing.T) {
	for name, stores := range backends(t) {
		t.Run(name, func(t *testing.T) {
			pruneScenario(t, stores(t))
		})
	}
}

func pruneScenario(t *testing.T, stores tenantStores) {
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return at }
	config := serverConfig{Keys: []serverKey{hashed("ann", "acme", mcp.RoleApprover), hashed("globex-bot", "globex", mcp.RoleProducer)}}
	srv := httptest.NewServer(newServeHandler(stores, config, func() time.Time { return at.Add(-40 * 24 * time.Hour) }, io.Discard))
	t.Cleanup(srv.Close)
	ann, globex := dial(t, srv.URL, "ann"), dial(t, srv.URL, "globex-bot")
	judged := call(t, ann, "run", map[string]any{"producer": "bot", "output": "judged"})["trace_id"]
	call(t, ann, "feedback", map[string]any{"trace_id": judged, "verdict": "approve"})
	call(t, ann, "run", map[string]any{"producer": "bot", "output": "unjudged"})
	call(t, globex, "run", map[string]any{"producer": "bot", "output": "pruned too"})
	var log bytes.Buffer
	p := pruner{stores: stores, tenants: append(config.tenants(), "unknown"), days: 30, now: now, log: &log}

	p.once(context.Background())

	for tenant, want := range map[string][]any{"acme": {judged}, "globex": nil} {
		r, err := stores.reports(context.Background(), serverKey{Tenant: tenant}, func() time.Time { return time.Time{} })
		require.NoError(t, err)
		runs, err := r.Traces.List(context.Background(), trace.Filter{Name: trace.NameRun})
		require.NoError(t, err)
		var ids []any
		for _, run := range runs {
			ids = append(ids, run.ID)
		}
		assert.Equal(t, want, ids, tenant)
	}
	assert.Empty(t, log.String())
}

// The mark of the last prune lives with the records so a restarted server prunes at start only when a day passed
func TestPrunerAfterARestart(t *testing.T) {
	for name, stores := range backends(t) {
		t.Run(name, func(t *testing.T) {
			at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
			config := serverConfig{Keys: []serverKey{hashed("ann", "acme", mcp.RoleApprover)}}
			tenants := stores(t)
			srv := httptest.NewServer(newServeHandler(tenants, config, func() time.Time { return at.Add(-40 * 24 * time.Hour) }, io.Discard))
			t.Cleanup(srv.Close)
			ann := dial(t, srv.URL, "ann")
			left := func() int {
				r, err := tenants.reports(context.Background(), serverKey{Tenant: "acme"}, func() time.Time { return time.Time{} })
				require.NoError(t, err)
				runs, err := r.Traces.List(context.Background(), trace.Filter{Name: trace.NameRun})
				require.NoError(t, err)
				return len(runs)
			}
			// A new pruner at each time stands for a server started again
			prune := func(now time.Time) {
				var log bytes.Buffer
				pruner{stores: tenants, tenants: config.tenants(), days: 30, now: func() time.Time { return now }, log: &log}.once(context.Background())
				require.Empty(t, log.String())
			}

			call(t, ann, "run", map[string]any{"producer": "bot", "output": "first"})
			prune(at)
			call(t, ann, "run", map[string]any{"producer": "bot", "output": "second"})
			prune(at.Add(time.Hour))
			afterRestart := left()
			prune(at.Add(25 * time.Hour))

			assert.Equal(t, 1, afterRestart, "a restart within the day leaves the second run")
			assert.Equal(t, 0, left(), "a day later the prune runs at start")
		})
	}
}

// A pruner that cannot open a tenant names it on the log and goes on with the next tick
func TestPrunerLogsAFailure(t *testing.T) {
	log := &lockedBuffer{}
	p := pruner{stores: unopened{}, tenants: []string{"acme"}, days: 30, now: time.Now, log: log}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		p.run(ctx, time.Millisecond)
		close(done)
	}()

	require.Eventually(t, func() bool { return strings.Count(log.String(), "retention of tenant acme") >= 2 }, 5*time.Second, time.Millisecond)
	cancel()
	<-done
}

// A log the pruner writes while the test reads it
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
