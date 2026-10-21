package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	feedbackfile "github.com/jeon-jihyeon/nodloop/internal/feedback/file"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
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
	if err := uc.Server.check(); err != nil {
		return fail(stderr, "serve", err)
	}
	policy := mcp.Policy{Holdout: knowledge.Holdout(uc.Holdout), Retention: retain.Days(uc.Retention), Build: buildVersion()}
	if err := errors.Join(policy.Holdout.Check(), policy.Retention.Check()); err != nil {
		return fail(stderr, "serve", err)
	}
	var stores tenantStores
	var where string
	if *dsn != "" {
		db, err := pg.Open(context.Background(), *dsn)
		if err != nil {
			return fail(stderr, "serve", err)
		}
		defer db.Close()
		stores, where = pgTenants{db: db, policy: policy}, "records in PostgreSQL"
	} else {
		dir, err := h.RecordDir(*recordDir, getenv(envRecordDir), uc.RecordDir)
		if err != nil {
			return fail(stderr, "serve", err)
		}
		stores, where = fileTenants{base: dir, policy: policy}, "records under "+dir
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fail(stderr, "serve", err)
	}
	// The pruner stops and is waited for before the deferred close of the database
	ctx, cancel := context.WithCancel(context.Background())
	pruned := make(chan struct{})
	defer func() {
		cancel()
		<-pruned
	}()
	go func() {
		defer close(pruned)
		if policy.Retention > 0 {
			pruner{stores: stores, tenants: uc.Server.tenants(), days: policy.Retention, now: now, log: stderr}.run(ctx, pruneTick)
		}
	}()
	fmt.Fprintf(stderr, "nodloop-server: %d keys, %s, listening on http://%s/mcp with records at /v1/records/{kind} and their OpenAPI document at /v1/openapi.json\n",
		len(uc.Server.Keys), where, *addr)
	srv := &http.Server{Handler: newServeHandler(stores, uc.Server, now, stderr), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fail(stderr, "serve", err)
	}
	return 0
}

// Authenticates every request and hands it to the MCP server of its key
// 1. stateless so every request carries its own key and no session outlives it
// 2. a tenant reads and writes the records under tenants of the record directory and never those of another tenant
// 3. one host per key so its calls are serialized like a stdio server while keys of one tenant share the file locks of its records
// 4. GET /v1/records/{kind} answers a page of the raw records of a kind to a key whose role may read records
// 5. POST /v1/interventions records one intervention of the write side through the host of the key so it serializes with its tool calls
// 6. GET /v1/reports/{name} answers a report as JSON to a key whose role may read reports until the record pages replace it in the next release
// 7. GET /healthz and GET /v1/openapi.json answer without a key so a load balancer probes it and a client reads the contract
type serveHandler struct {
	stores tenantStores
	config serverConfig
	now    func() time.Time
	// Where an internal failure is written since its answer names none of it
	log   io.Writer
	mu    sync.Mutex
	hosts map[string]keyHost
	mcp   http.Handler
}

// The host of one key with the MCP server of its role
type keyHost struct {
	host   *mcp.Host
	server *sdk.Server
}

type keyContext struct{}

// Every other path is the MCP endpoint as it was before the reports came
// A client that names no path keeps working
func newServeHandler(stores tenantStores, config serverConfig, now func() time.Time, log io.Writer) http.Handler {
	h := &serveHandler{stores: stores, config: config, now: now, log: log, hosts: map[string]keyHost{}}
	h.mcp = sdk.NewStreamableHTTPHandler(h.server, &sdk.StreamableHTTPOptions{Stateless: true})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.Handle("GET /v1/openapi.json", newOpenAPIHandler(schema.Published, buildVersion(), log))
	mux.Handle("GET /v1/records/{kind}", h.authorized(http.HandlerFunc(h.records)))
	mux.Handle("POST /v1/interventions", h.authorized(http.HandlerFunc(h.interventions)))
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
	stores, err := h.stores.reports(r.Context(), key, h.now)
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

// One page of the raw records of a kind of the tenant of the key in the published schema of the kind
// 1. a role without the MCP tool records is forbidden
// 2. a kind outside the published index or one the server keeps no store for is not found
// 3. a limit, a content flag or a cursor the page cannot read is a bad request
func (h *serveHandler) records(w http.ResponseWriter, r *http.Request) {
	key, _ := r.Context().Value(keyContext{}).(serverKey)
	if !slices.Contains(key.Role.Tools(), "records") {
		http.Error(w, fmt.Sprintf("the role %s may not read records", key.Role), http.StatusForbidden)
		return
	}
	q, err := recordsQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	source, err := h.stores.records(key, h.now)
	if err != nil {
		h.internal(w, key, err)
		return
	}
	page, err := source.Page(r.Context(), q)
	switch {
	case errors.Is(err, schema.ErrKindUnknown), errors.Is(err, schema.ErrNotServed):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, schema.ErrCursorInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		h.internal(w, key, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	// The records go out as their stores keep them
	enc.SetEscapeHTML(false)
	_ = enc.Encode(page)
}

// The query of a records page from its path and its query string
func recordsQuery(r *http.Request) (schema.Query, error) {
	values := r.URL.Query()
	q := schema.Query{Kind: r.PathValue("kind"), Since: values.Get("since")}
	if v := values.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return schema.Query{}, fmt.Errorf("%w: limit %q is no number", errQueryInvalid, v)
		}
		q.Limit = n
	}
	if v := values.Get("content"); v != "" {
		content, err := strconv.ParseBool(v)
		if err != nil {
			return schema.Query{}, fmt.Errorf("%w: content %q is neither true nor false", errQueryInvalid, v)
		}
		q.Content = content
	}
	return q, nil
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
	return h.hostOf(key).server
}

// The host of the key made once so every call of the key serializes on it
func (h *serveHandler) hostOf(key serverKey) keyHost {
	h.mu.Lock()
	defer h.mu.Unlock()
	if kh, ok := h.hosts[key.Name]; ok {
		return kh
	}
	host := mcp.NewHost(h.stores.open(key, h.now, mcp.NewSession(h.now())), buildVersion(), "")
	kh := keyHost{host: host, server: host.Server(key.Role.Tools())}
	h.hosts[key.Name] = kh
	return kh
}

// What a payload the records cannot carry out names
// Each is answered 422 with the refusal and any other failure 500 without its cause
var refusals = []error{
	curate.ErrTargetInvalid, trace.ErrInterventionInvalid, knowledge.ErrApproverRequired, knowledge.ErrTransitionInvalid, knowledge.ErrNotFound,
	knowledge.ErrFolderFull, knowledge.ErrScopeWidened, knowledge.ErrVetoLifted, knowledge.ErrCandidateOutdated, knowledge.ErrScopeUnobserved,
	knowledge.ErrScopeInvalid, knowledge.ErrReverseStale, knowledge.ErrCoverageNotPassed, knowledge.ErrCompactionOutdated,
	knowledge.ErrCompactionInvalid, knowledge.ErrVersionUnapproved, knowledge.ErrNotCandidate, knowledge.ErrPersonDecides,
}

// One intervention of the write side under the key in the payload of MCP intervene
// 1. a role without the MCP tool intervene is forbidden
// 2. a body that is no payload is a bad request and a payload the records cannot carry out is unprocessable
// 3. under a key of role approver added with --person it is the person's judgment and under any other key an agent proposal
func (h *serveHandler) interventions(w http.ResponseWriter, r *http.Request) {
	key, _ := r.Context().Value(keyContext{}).(serverKey)
	if !slices.Contains(key.Role.Tools(), "intervene") {
		http.Error(w, fmt.Sprintf("the role %s may not intervene", key.Role), http.StatusForbidden)
		return
	}
	var req curate.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPayload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("%v: %v", errPayloadInvalid, err), http.StatusBadRequest)
		return
	}
	answer, err := h.hostOf(key).host.Intervene(r.Context(), req)
	switch {
	case slices.ContainsFunc(refusals, func(refusal error) bool { return errors.Is(err, refusal) }):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		h.internal(w, key, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(answer)
}

// The largest payload an intervention takes
const maxPayload = 1 << 16

// Where the server keeps the records of a tenant
type tenantStores interface {
	open(key serverKey, now func() time.Time, session string) mcp.Open
	// The stores the reports of the tenant read without the tools of a host
	reports(ctx context.Context, key serverKey, now func() time.Time) (loop.Stores, error)
	// The stores the raw record pages of the tenant read
	// No guard log since the server runs no guard
	records(key serverKey, now func() time.Time) (schema.Source, error)
	// The retainer over every store of the tenant that may cite a run
	retainer(tenant string, now func() time.Time) (*retain.Retainer, error)
	// Where the last prune of the tenant is kept
	// A file tenant without a directory fails with errNoRecords so a prune never creates it
	marks(tenant string) (retain.Marks, error)
}

// How often the pruner asks each tenant whether its daily prune is due
const pruneTick = time.Hour

// Prunes the records of every tenant once a day
// 1. the mark of each tenant is kept with its records so a restart and every replica read when the last prune ran
// 2. the first check runs at start so a prune that came due while the server was down runs at once and a restart loop prunes no more than once a day
// 3. a failure goes to the log with its tenant and the next day tries again
type pruner struct {
	stores  tenantStores
	tenants []string
	days    retain.Days
	now     func() time.Time
	log     io.Writer
}

func (p pruner) run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		p.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// The daily prune of each tenant on its own stores so no tenant ever loses a run of another
// A tenant without records is skipped
func (p pruner) once(ctx context.Context) {
	for _, tenant := range p.tenants {
		marks, err := p.stores.marks(tenant)
		if err == nil {
			open := func() (*retain.Retainer, error) { return p.stores.retainer(tenant, p.now) }
			_, err = retain.NewDaily(marks, p.days, open, p.now).Run(ctx)
		}
		if err != nil && !errors.Is(err, errNoRecords) {
			fmt.Fprintf(p.log, "nodloop-server: retention of tenant %s: %v\n", tenant, err)
		}
	}
}

// The start of the runs the reports of the tenant read
func window(ctx context.Context, policy mcp.Policy, now time.Time) (time.Time, error) {
	last, err := policy.Marks.Last(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return policy.Retention.Window(now, last.Cutoff), nil
}

// The record directory of each tenant under tenants of the base directory
type fileTenants struct {
	base   string
	policy mcp.Policy
}

// The record stores of one tenant directory
type tenantFiles struct {
	traces   *tracefile.Store
	verdicts *feedbackfile.Store
	outcomes *feedbackfile.OutcomeStore
	items    *knowledgefile.Store
	ledger   *knowledge.Ledger
}

func (t tenantFiles) source() schema.Source {
	return schema.Source{Traces: t.traces, Verdicts: t.verdicts, Outcomes: t.outcomes, Knowledge: t.items}
}

// No home so the config of the server user is not read for a tenant and no veto file is written under it
func (f fileTenants) files(tenant string, now func() time.Time) (tenantFiles, error) {
	dir := f.dir(tenant)
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
	return tenantFiles{traces: traces, verdicts: verdicts, outcomes: outcomes, items: items, ledger: ledger}, nil
}

// The policy of the server with the marks of the tenant
func (f fileTenants) policyOf(tenant string) mcp.Policy {
	p := f.policy
	p.Marks = retain.NewMarker(f.dir(tenant))
	return p
}

func (f fileTenants) dir(tenant string) string {
	return filepath.Join(f.base, "tenants", tenant)
}

func (f fileTenants) open(key serverKey, now func() time.Time, session string) mcp.Open {
	return func(context.Context) (*mcp.Server, error) {
		t, err := f.files(key.Tenant, now)
		if err != nil {
			return nil, err
		}
		compactor := compact.New(t.ledger, t.traces, t.verdicts, t.traces, now)
		extractor := extract.New(t.ledger, t.traces, t.verdicts, now)
		return mcp.New(
			t.traces, t.verdicts, t.outcomes, t.ledger, t.source(), compactor, extractor, now, session, cliName, "", key.caller(), f.policyOf(key.Tenant),
		), nil
	}
}

func (f fileTenants) records(key serverKey, now func() time.Time) (schema.Source, error) {
	t, err := f.files(key.Tenant, now)
	if err != nil {
		return schema.Source{}, err
	}
	return t.source(), nil
}

func (f fileTenants) reports(ctx context.Context, key serverKey, now func() time.Time) (loop.Stores, error) {
	t, err := f.files(key.Tenant, now)
	if err != nil {
		return loop.Stores{}, err
	}
	since, err := window(ctx, f.policyOf(key.Tenant), now())
	if err != nil {
		return loop.Stores{}, err
	}
	return loop.Stores{Traces: t.traces, Verdicts: t.verdicts, Outcomes: t.outcomes, Items: t.ledger, Since: since}, nil
}

// The directory of a tenant that has records
// A tenant whose directory does not exist yet fails with errNoRecords so a prune never creates it
func (f fileTenants) recorded(tenant string) (string, error) {
	dir := f.dir(tenant)
	_, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", errNoRecords
	}
	return dir, err
}

func (f fileTenants) marks(tenant string) (retain.Marks, error) {
	dir, err := f.recorded(tenant)
	if err != nil {
		return nil, err
	}
	return retain.NewMarker(dir), nil
}

func (f fileTenants) retainer(tenant string, now func() time.Time) (*retain.Retainer, error) {
	if _, err := f.recorded(tenant); err != nil {
		return nil, err
	}
	t, err := f.files(tenant, now)
	if err != nil {
		return nil, err
	}
	counter := loop.Stores{Traces: t.traces, Verdicts: t.verdicts, Outcomes: t.outcomes, Items: t.ledger}
	return retain.New(t.traces, t.verdicts, t.outcomes, t.ledger, counter.Rollup, now), nil
}

// The rows of each tenant in one database
type pgTenants struct {
	db     *pg.DB
	policy mcp.Policy
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
		return mcp.New(
			traces, verdicts, p.db.Outcomes(key.Tenant), ledger, p.source(key.Tenant), compactor, extractor, now, session, cliName, "", key.caller(),
			p.policyOf(key.Tenant),
		), nil
	}
}

func (p pgTenants) source(tenant string) schema.Source {
	return schema.Source{Traces: p.db.Traces(tenant), Verdicts: p.db.Feedback(tenant), Outcomes: p.db.Outcomes(tenant), Knowledge: p.db.Knowledge(tenant)}
}

func (p pgTenants) records(key serverKey, _ func() time.Time) (schema.Source, error) {
	return p.source(key.Tenant), nil
}

// The policy of the server with the marks of the tenant
func (p pgTenants) policyOf(tenant string) mcp.Policy {
	policy := p.policy
	policy.Marks = p.db.Prunes(tenant)
	return policy
}

func (p pgTenants) reports(ctx context.Context, key serverKey, now func() time.Time) (loop.Stores, error) {
	since, err := window(ctx, p.policyOf(key.Tenant), now())
	if err != nil {
		return loop.Stores{}, err
	}
	return loop.Stores{
		Traces: p.db.Traces(key.Tenant), Verdicts: p.db.Feedback(key.Tenant), Outcomes: p.db.Outcomes(key.Tenant), Items: p.ledger(key.Tenant, now),
		Since: since,
	}, nil
}

func (p pgTenants) marks(tenant string) (retain.Marks, error) {
	return p.db.Prunes(tenant), nil
}

func (p pgTenants) retainer(tenant string, now func() time.Time) (*retain.Retainer, error) {
	traces, verdicts, outcomes, ledger := p.db.Traces(tenant), p.db.Feedback(tenant), p.db.Outcomes(tenant), p.ledger(tenant, now)
	counter := loop.Stores{Traces: traces, Verdicts: verdicts, Outcomes: outcomes, Items: ledger}
	return retain.New(traces, verdicts, outcomes, ledger, counter.Rollup, now), nil
}

// The CLI a tool answer names for a check that runs a model such as knowledge check
const cliName = "nodloop"
