package main

import (
	"context"
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
	"github.com/jeon-jihyeon/nodloop/internal/userconfig"
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
	h := userconfig.Home(getenv("HOME"))
	if h == "" {
		return fail(stderr, "serve", errHomeUnknown)
	}
	var uc fileConfig
	if err := h.Read(&uc); err != nil {
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
		dir, err := h.RecordDir(*recordDir, getenv(envRecordDir), uc.RecordDir)
		if err != nil {
			return fail(stderr, "serve", err)
		}
		stores, where = fileTenants{base: dir}, "records under "+dir
	}
	fmt.Fprintf(stderr, "nodloop-server: %d keys, %s, listening on http://%s/mcp with reports at /v1/reports/{name}\n", len(uc.Server.Keys), where, *addr)
	srv := &http.Server{Addr: *addr, Handler: newServeHandler(stores, uc.Server, now, stderr), ReadHeaderTimeout: 10 * time.Second}
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
	// Where an internal failure is written since its answer names none of it
	log   io.Writer
	mu    sync.Mutex
	hosts map[string]*sdk.Server
	mcp   http.Handler
}

type keyContext struct{}

// Every other path is the MCP endpoint as it was before the reports came
// A client that names no path keeps working
func newServeHandler(stores tenantStores, config serverConfig, now func() time.Time, log io.Writer) http.Handler {
	h := &serveHandler{stores: stores, config: config, now: now, log: log, hosts: map[string]*sdk.Server{}}
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
	stores, err := h.stores.reports(key, h.now)
	if err != nil {
		h.internal(w, key, err)
		return
	}
	report, err := stores.Report(r.Context(), loop.ReportName(r.PathValue("name")), h.now())
	switch {
	case errors.Is(err, loop.ErrReportUnknown):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		h.internal(w, key, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
}

// Answers a failure of the server without its cause and writes the cause to the log
// A cause may name a path or a database error that a key holder has no need to read
func (h *serveHandler) internal(w http.ResponseWriter, key serverKey, err error) {
	fmt.Fprintf(h.log, "nodloop-server: key %s: %v\n", key.Name, err)
	http.Error(w, "internal error", http.StatusInternalServerError)
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
	// The stores the reports of the tenant read without the tools of a host
	reports(key serverKey, now func() time.Time) (loop.Stores, error)
}

// The record directory of each tenant under tenants of the base directory
type fileTenants struct {
	base string
}

// The record stores of one tenant directory
type tenantFiles struct {
	traces   *tracefile.Store
	verdicts *feedbackfile.Store
	outcomes *feedbackfile.OutcomeStore
	ledger   *knowledge.Ledger
}

// No home so the config of the server user is not read for a tenant and no veto file is written under it
func (f fileTenants) files(tenant string, now func() time.Time) (tenantFiles, error) {
	dir := filepath.Join(f.base, "tenants", tenant)
	// The records of a tenant are private to the server user so no other account on the host reads them
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tenantFiles{}, fmt.Errorf("record dir: %w", err)
	}
	traces, err := tracefile.New(dir)
	if err != nil {
		return tenantFiles{}, err
	}
	verdicts, err := feedbackfile.New(dir)
	if err != nil {
		return tenantFiles{}, err
	}
	outcomes, err := feedbackfile.NewOutcomeStore(dir)
	if err != nil {
		return tenantFiles{}, err
	}
	items, err := knowledgefile.New(dir)
	if err != nil {
		return tenantFiles{}, err
	}
	ledger := knowledge.NewLedger(items, vetofile.NewApprovedFile("", ""), now, knowledge.NewIDs(now))
	return tenantFiles{traces: traces, verdicts: verdicts, outcomes: outcomes, ledger: ledger}, nil
}

func (f fileTenants) open(key serverKey, now func() time.Time, session string) mcp.Open {
	return func(context.Context) (*mcp.Server, error) {
		t, err := f.files(key.Tenant, now)
		if err != nil {
			return nil, err
		}
		compactor := compact.New(t.ledger, t.traces, t.verdicts, t.traces, now)
		extractor := extract.New(t.ledger, t.traces, t.verdicts, now)
		return mcp.New(t.traces, t.verdicts, t.outcomes, t.ledger, compactor, extractor, now, session, cliName, "", key.Name), nil
	}
}

func (f fileTenants) reports(key serverKey, now func() time.Time) (loop.Stores, error) {
	t, err := f.files(key.Tenant, now)
	if err != nil {
		return loop.Stores{}, err
	}
	return loop.Stores{Traces: t.traces, Verdicts: t.verdicts, Outcomes: t.outcomes, Items: t.ledger}, nil
}

// The rows of each tenant in one database
type pgTenants struct {
	db *pg.DB
}

// No veto file is written since the tool calls of a server's producers are checked through check_call
func (p pgTenants) ledger(tenant string, now func() time.Time) *knowledge.Ledger {
	return knowledge.NewLedger(p.db.Knowledge(tenant), vetofile.NewApprovedFile("", ""), now, knowledge.NewIDs(now))
}

func (p pgTenants) open(key serverKey, now func() time.Time, session string) mcp.Open {
	return func(context.Context) (*mcp.Server, error) {
		traces, verdicts := p.db.Traces(key.Tenant), p.db.Feedback(key.Tenant)
		ledger := p.ledger(key.Tenant, now)
		compactor := compact.New(ledger, traces, verdicts, traces, now)
		extractor := extract.New(ledger, traces, verdicts, now)
		return mcp.New(traces, verdicts, p.db.Outcomes(key.Tenant), ledger, compactor, extractor, now, session, cliName, "", key.Name), nil
	}
}

func (p pgTenants) reports(key serverKey, now func() time.Time) (loop.Stores, error) {
	return loop.Stores{
		Traces: p.db.Traces(key.Tenant), Verdicts: p.db.Feedback(key.Tenant), Outcomes: p.db.Outcomes(key.Tenant), Items: p.ledger(key.Tenant, now),
	}, nil
}

// The CLI a tool answer names for a check that runs a model such as knowledge check
const cliName = "nodloop"
