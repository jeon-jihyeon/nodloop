package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/pg"
	vetofile "github.com/jeon-jihyeon/nodloop/internal/veto/file"
)

// One key a service calls the server with
// Only the SHA-256 of the key is saved so config.json never holds a key
type serverKey struct {
	// The person or service the key names
	// Approvals through the key are recorded under it
	Name   string   `json:"name"`
	Tenant string   `json:"tenant"`
	Role   mcp.Role `json:"role"`
	SHA256 string   `json:"sha256"`
}

// What the server section of config.json holds
type serverConfig struct {
	Keys []serverKey `json:"keys,omitempty"`
}

// A tenant names a directory under the record directory so it is one path element
var tenantName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// The key whose hash matches the token compared in constant time
func (c serverConfig) match(token string) (serverKey, bool) {
	sum := sha256.Sum256([]byte(token))
	got := hex.EncodeToString(sum[:])
	for _, k := range c.Keys {
		if subtle.ConstantTimeCompare([]byte(k.SHA256), []byte(got)) == 1 {
			return k, true
		}
	}
	return serverKey{}, false
}

// server key add, list and remove manage the keys and serve runs the tools over HTTP
func runServer(args []string, getenv func(string) string, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return fail(stderr, "server", errNoAction)
	}
	h := homeDir(getenv("HOME"))
	if h == "" {
		return fail(stderr, "server", errHomeUnknown)
	}
	if args[0] == "serve" {
		return runServe(args[1:], getenv, now, stderr)
	}
	if args[0] != "key" || len(args) < 2 {
		return fail(stderr, "server", fmt.Errorf("%w %q", errUnknownAction, strings.Join(args, " ")))
	}
	fs := newFlagSet("server key "+args[1], stderr)
	tenant := fs.String("tenant", "", "add: the tenant whose records the key reads and writes")
	role := fs.String("role", "", "add: producer, reviewer or approver")
	name, err := parseID(fs, args[2:])
	if err != nil {
		return parseFailed(err)
	}
	uc, err := h.readConfig()
	if err != nil {
		return fail(stderr, "server", err)
	}
	cmd := serverCommand{home: h, keys: uc.Server, out: stdout}
	switch args[1] {
	case "add":
		err = cmd.add(serverKey{Name: name, Tenant: *tenant, Role: mcp.Role(*role)})
	case "list":
		cmd.list()
	case "remove":
		err = cmd.remove(name)
	default:
		err = fmt.Errorf("%w %q", errUnknownAction, args[1])
	}
	if err != nil {
		return fail(stderr, "server", err)
	}
	return 0
}

type serverCommand struct {
	home homeDir
	keys serverConfig
	out  io.Writer
}

// Prints the new key once and saves its hash
func (c serverCommand) add(k serverKey) error {
	switch {
	case k.Name == "":
		return fmt.Errorf("add: a key name %w", errRequired)
	case slices.ContainsFunc(c.keys.Keys, func(o serverKey) bool { return o.Name == k.Name }):
		return fmt.Errorf("%w: %s", errKeyExists, k.Name)
	case !tenantName.MatchString(k.Tenant):
		return fmt.Errorf("%w: %q. Use lower case letters, digits, - and _", errTenantInvalid, k.Tenant)
	case !k.Role.Valid():
		return fmt.Errorf("%w: %q. Use one of %v", errRoleInvalid, k.Role, mcp.Roles())
	}
	var secret [32]byte
	// crypto rand Read never returns an error
	_, _ = rand.Read(secret[:])
	key := "nl_" + hex.EncodeToString(secret[:])
	sum := sha256.Sum256([]byte(key))
	k.SHA256 = hex.EncodeToString(sum[:])
	keys := c.keys
	keys.Keys = append(slices.Clone(keys.Keys), k)
	if err := c.home.save("server", keys); err != nil {
		return err
	}
	fmt.Fprintln(c.out, key)
	return nil
}

func (c serverCommand) list() {
	for _, k := range c.keys.Keys {
		fmt.Fprintf(c.out, "%s\t%s\t%s\n", k.Name, k.Tenant, k.Role)
	}
}

func (c serverCommand) remove(name string) error {
	keys := c.keys
	keys.Keys = slices.DeleteFunc(slices.Clone(keys.Keys), func(k serverKey) bool { return k.Name == name })
	if len(keys.Keys) == len(c.keys.Keys) {
		return fmt.Errorf("%w: %s", errKeyUnknown, name)
	}
	return c.home.save("server", keys)
}

// Serves the MCP tools over streamable HTTP until the process ends
func runServe(args []string, getenv func(string) string, now func() time.Time, stderr io.Writer) int {
	fs := newFlagSet("server serve", stderr)
	var records recordFlags
	records.bind(fs)
	addr := fs.String("addr", "127.0.0.1:8787", "the address to listen on")
	dsn := fs.String("postgres", getenv(envPostgres), "a PostgreSQL URL to keep the records of every tenant in. "+envPostgres+" when empty")
	if err := fs.Parse(args); err != nil {
		return parseFailed(err)
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "server", err)
	}
	uc, err := a.cfg.home.readConfig()
	if err != nil {
		return fail(stderr, "server", err)
	}
	if len(uc.Server.Keys) == 0 {
		return fail(stderr, "server", fmt.Errorf("%w. Add one with nodloop server key add", errNoKeys))
	}
	stores := tenantStores(fileTenants{base: a.cfg.recordDir})
	where := "records under " + a.cfg.recordDir
	if *dsn != "" {
		db, err := pg.Open(context.Background(), *dsn)
		if err != nil {
			return fail(stderr, "server", err)
		}
		defer db.Close()
		stores, where = pgTenants{db: db}, "records in PostgreSQL"
	}
	fmt.Fprintf(stderr, "nodloop server: %d keys, %s, listening on http://%s/mcp with reports at /v1/reports/{name}\n", len(uc.Server.Keys), where, *addr)
	srv := &http.Server{Addr: *addr, Handler: newServeHandler(stores, uc.Server, now), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fail(stderr, "server", err)
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
	return mcpOpen{
		flags:  recordFlags{recordDir: filepath.Join(f.base, "tenants", key.Tenant)},
		getenv: func(string) string { return "" }, now: now, session: session, person: key.Name,
	}.open
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
		ledger := knowledge.NewLedger(p.db.Knowledge(key.Tenant), vetofile.NewApprovedFile("", ""), now, app{now: now}.newID)
		compactor := compact.New(ledger, traces, verdicts, traces, now)
		extractor := extract.New(ledger, traces, verdicts, now)
		return mcp.New(traces, verdicts, p.db.Outcomes(key.Tenant), ledger, compactor, extractor, now, session, executable(), "", key.Name), nil
	}
}
