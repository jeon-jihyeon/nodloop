package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
	"github.com/jeon-jihyeon/nodloop/server/internal/pg"
)

// Serves the MCP tools over streamable HTTP until the process ends
func runServe(args []string, getenv func(string) string, now func() time.Time, stderr io.Writer) int {
	fs := newFlagSet("serve", stderr)
	recordDir := fs.String("record-dir", "", "record directory. Overrides "+envRecordDir)
	addr := fs.String("addr", "127.0.0.1:8787", "the address to listen on")
	dsn := fs.String("postgres", getenv(envPostgres), "a PostgreSQL URL to keep the records of every tenant in. "+envPostgres+" when empty")
	if err := fs.Parse(args); err != nil {
		return parseFailed(err)
	}
	h := homeDir(getenv("HOME"))
	uc, err := h.readConfig()
	if err != nil {
		return fail(stderr, "serve", err)
	}
	if len(uc.Server.Keys) == 0 {
		return fail(stderr, "serve", fmt.Errorf("%w. Add one with nodloop-server key add", errNoKeys))
	}
	var stores tenantStores
	var where string
	if *dsn != "" {
		db, err := pg.Open(context.Background(), *dsn)
		if err != nil {
			return fail(stderr, "serve", err)
		}
		defer db.Close()
		stores, where = pgTenants{db: db}, "records in PostgreSQL"
	} else {
		dir, err := h.recordDir(*recordDir, getenv(envRecordDir), uc.RecordDir)
		if err != nil {
			return fail(stderr, "serve", err)
		}
		stores, where = fileTenants{base: dir}, "records under "+dir
	}
	fmt.Fprintf(stderr, "nodloop-server: %d keys, %s, listening on http://%s/mcp with reports at /v1/reports/{name}\n", len(uc.Server.Keys), where, *addr)
	srv := &http.Server{Addr: *addr, Handler: newServeHandler(stores, uc.Server, now), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fail(stderr, "serve", err)
	}
	return 0
}

// Authenticates every request and hands it to the MCP server of its key
// 1. stateless so every request carries its own key and no session outlives it
// 2. a tenant reads and writes the records under tenants of the record directory and never those of another tenant
// 3. one host per key so its calls are serialized like a stdio server while keys of one tenant share the file locks of its records
// 4. GET /v1/reports/{name} answers a report as JSON for a dashboard to a key whose role may read reports
// 5. GET /healthz answers without a key so a load balancer can probe it
type serveHandler struct {
	stores tenantStores
	config serverConfig
	now    func() time.Time
	mu     sync.Mutex
	hosts  map[string]*sdk.Server
	mcp    http.Handler
}

type keyContext struct{}

// Every other path is the MCP endpoint, as before the reports came, so a client that names no path keeps working
func newServeHandler(stores tenantStores, config serverConfig, now func() time.Time) http.Handler {
	h := &serveHandler{stores: stores, config: config, now: now, hosts: map[string]*sdk.Server{}}
	h.mcp = sdk.NewStreamableHTTPHandler(h.server, &sdk.StreamableHTTPOptions{Stateless: true})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.Handle("GET /v1/reports/{name}", h.authorized(http.HandlerFunc(h.report)))
	mux.Handle("/", h.authorized(h.mcp))
	return mux
}

// The handler behind a known bearer key with the key in the request context
func (h *serveHandler) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		key, known := h.config.match(token)
		if !ok || !known {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nodloop"`)
			http.Error(w, "a nodloop server key is required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyContext{}, key)))
	})
}

// One report of the tenant of the key as JSON
// A role without the MCP tool report is forbidden and an unknown name is not found
func (h *serveHandler) report(w http.ResponseWriter, r *http.Request) {
	key, _ := r.Context().Value(keyContext{}).(serverKey)
	if !slices.Contains(key.Role.Tools(), "report") {
		http.Error(w, fmt.Sprintf("the role %s may not read reports", key.Role), http.StatusForbidden)
		return
	}
	srv, err := h.stores.open(key, h.now, mcp.NewSession(h.now()))(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	report, err := srv.Report(r.Context(), loop.ReportName(r.PathValue("name")))
	switch {
	case errors.Is(err, loop.ErrReportUnknown):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
}

// The MCP server of the key the request was authenticated with
func (h *serveHandler) server(r *http.Request) *sdk.Server {
	key, _ := r.Context().Value(keyContext{}).(serverKey)
	h.mu.Lock()
	defer h.mu.Unlock()
	if srv, ok := h.hosts[key.Name]; ok {
		return srv
	}
	srv := mcp.NewHost(h.stores.open(key, h.now, mcp.NewSession(h.now())), buildVersion(), "").Server(key.Role.Tools())
	h.hosts[key.Name] = srv
	return srv
}

// Where the server keeps the records of a tenant
type tenantStores interface {
	open(key serverKey, now func() time.Time, session string) mcp.Open
}

// The record directory of each tenant under tenants of the base directory
type fileTenants struct {
	base string
}

// No home so the config of the server user is not read for a tenant and no veto file is written under it
func (f fileTenants) open(key serverKey, now func() time.Time, session string) mcp.Open {
	dir := filepath.Join(f.base, "tenants", key.Tenant)
	return func(context.Context) (*mcp.Server, error) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("record dir: %w", err)
		}
		traces, err := tracefile.New(dir)
		if err != nil {
			return nil, err
		}
		verdicts, err := feedbackfile.New(dir)
		if err != nil {
			return nil, err
		}
		outcomes, err := feedbackfile.NewOutcomeStore(dir)
		if err != nil {
			return nil, err
		}
		items, err := knowledgefile.New(dir)
		if err != nil {
			return nil, err
		}
		ledger := knowledge.NewLedger(items, vetofile.NewApprovedFile("", ""), now, idsAt(now))
		compactor := compact.New(ledger, traces, verdicts, traces, now)
		extractor := extract.New(ledger, traces, verdicts, now)
		return mcp.New(traces, verdicts, outcomes, ledger, compactor, extractor, now, session, cliName, "", key.Name), nil
	}
}

// The rows of each tenant in one database
type pgTenants struct {
	db *pg.DB
}

// The stores of the tenant of the key
// No veto file is written since the tool calls of a server's producers are checked through check_call
func (p pgTenants) open(key serverKey, now func() time.Time, session string) mcp.Open {
	return func(context.Context) (*mcp.Server, error) {
		traces, verdicts := p.db.Traces(key.Tenant), p.db.Feedback(key.Tenant)
		ledger := knowledge.NewLedger(p.db.Knowledge(key.Tenant), vetofile.NewApprovedFile("", ""), now, idsAt(now))
		compactor := compact.New(ledger, traces, verdicts, traces, now)
		extractor := extract.New(ledger, traces, verdicts, now)
		return mcp.New(traces, verdicts, p.db.Outcomes(key.Tenant), ledger, compactor, extractor, now, session, cliName, "", key.Name), nil
	}
}

// The CLI a tool answer names for a check that runs a model, such as knowledge check
const cliName = "nodloop"

// Knowledge ids as the nodloop CLI makes them: the prefix, the clock milliseconds in hex and two random bytes
func idsAt(now func() time.Time) func(prefix string) string {
	return func(prefix string) string {
		var suffix [2]byte
		// crypto rand Read never returns an error
		_, _ = rand.Read(suffix[:])
		return fmt.Sprintf("%s%x%x", prefix, now().UnixMilli(), suffix)
	}
}
