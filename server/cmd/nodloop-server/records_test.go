package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

// A clock the test moves while the server reads it
type movingClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *movingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *movingClock) move(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// The status and the body of a GET of the path under the key and no key when empty
func get(t *testing.T, url, key string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	if key != "" {
		req.Header.Set("Authorization", "Bearer key-"+key)
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	_ = res.Body.Close()
	return res.StatusCode, string(body)
}

// A page of the records of a kind as the server answers it with the record_id of each record
type httpPage struct {
	schema.Page
	ids []string
}

func pageOf(t *testing.T, body string) httpPage {
	t.Helper()
	var p httpPage
	require.NoError(t, json.Unmarshal([]byte(body), &p.Page))
	p.ids = []string{}
	for _, r := range p.Records {
		var head struct {
			Kind string `json:"record_kind"`
			ID   string `json:"record_id"`
		}
		require.NoError(t, json.Unmarshal(r, &head))
		require.NoError(t, schema.Validate(head.Kind, r))
		p.ids = append(p.ids, head.ID)
	}
	return p
}

// Keys of a role that reads records page the records of their tenant oldest first across a cursor and with text only when asked
func TestServeHandlerRecords(t *testing.T) {
	for name, stores := range backends(t) {
		t.Run(name, func(t *testing.T) {
			recordsScenario(t, stores(t))
		})
	}
}

func recordsScenario(t *testing.T, stores tenantStores) {
	clock := &movingClock{at: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	config := serverConfig{Keys: []serverKey{
		hashed("bot", "acme", mcp.RoleProducer), hashed("rita", "acme", mcp.RoleReviewer), held(hashed("ann", "acme", mcp.RoleApprover)),
		hashed("gus", "globex", mcp.RoleReviewer),
	}}
	srv := httptest.NewServer(newServeHandler(stores, config, clock.now, io.Discard))
	t.Cleanup(srv.Close)
	bot := dial(t, srv.URL+"/mcp", "bot")
	first, _ := call(t, bot, "run", map[string]any{"producer": "support-bot", "output": "refund steps"})["trace_id"].(string)
	clock.move(time.Millisecond)
	second, _ := call(t, bot, "run", map[string]any{"producer": "support-bot", "output": "the window is 30 days"})["trace_id"].(string)
	call(t, bot, "feedback", map[string]any{"trace_id": first, "verdict": "reject", "reason": "the window was missing"})
	clock.move(time.Minute)
	runs := srv.URL + "/v1/records/run"

	_, stripped := get(t, runs+"?limit=1", "rita")
	one := pageOf(t, stripped)
	_, next := get(t, runs+"?since="+one.Next, "ann")
	_, shown := get(t, runs+"?limit=1&content=true", "rita")
	_, verdicts := get(t, srv.URL+"/v1/records/verdict", "rita")
	_, globex := get(t, runs, "gus")

	assert.Equal(t, []string{first}, one.ids)
	assert.Equal(t, []string{second}, pageOf(t, next).ids)
	assert.NotContains(t, stripped, "refund steps")
	assert.Contains(t, shown, "refund steps")
	assert.Len(t, pageOf(t, verdicts).ids, 1)
	assert.NotContains(t, verdicts, "the window was missing")
	assert.Empty(t, pageOf(t, globex).ids, "another tenant reads its own records")
}

// A records page refuses a key without the role, a kind it does not keep and a query it cannot read
func TestServeHandlerRecordsRefuses(t *testing.T) {
	config := serverConfig{Keys: []serverKey{hashed("bot", "acme", mcp.RoleProducer), hashed("rita", "acme", mcp.RoleReviewer)}}
	srv := httptest.NewServer(newServeHandler(fileTenants{base: t.TempDir()}, config, time.Now, io.Discard))
	t.Cleanup(srv.Close)
	type want struct {
		status int
		body   string
	}
	tcs := []struct {
		name string
		path string
		key  string
		want want
	}{
		{"a page needs a key", "/v1/records/run", "", want{http.StatusUnauthorized, "a nodloop server key is required\n"}},
		{"a producer may not read records", "/v1/records/run", "bot", want{http.StatusForbidden, "the role producer may not read records\n"}},
		{"an unknown kind is not found", "/v1/records/session", "rita", want{http.StatusNotFound, schema.ErrKindUnknown.Error()}},
		{"the guard log is not kept by the server", "/v1/records/guard", "rita", want{http.StatusNotFound, schema.ErrNotServed.Error()}},
		{"a limit that is no number", "/v1/records/run?limit=ten", "rita", want{http.StatusBadRequest, `invalid query: limit "ten" is no number`}},
		{"a content flag that is no boolean", "/v1/records/run?content=maybe", "rita", want{http.StatusBadRequest, `invalid query: content "maybe" is neither true nor false`}},
		{"a cursor no page answered", "/v1/records/run?since=%25%25", "rita", want{http.StatusBadRequest, schema.ErrCursorInvalid.Error()}},
		{"an empty page of a reviewer", "/v1/records/run", "rita", want{http.StatusOK, `{"records":[],"next":""}`}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			status, body := get(t, srv.URL+tc.path, tc.key)

			assert.Equal(t, tc.want.status, status, body)
			assert.Contains(t, body, tc.want.body)
		})
	}
}

// A failure of the stores answers a records page without its cause
func TestServeHandlerRecordsInternalError(t *testing.T) {
	srv := httptest.NewServer(newServeHandler(unopened{}, serverConfig{Keys: []serverKey{hashed("ann", "acme", mcp.RoleApprover)}}, time.Now, io.Discard))
	t.Cleanup(srv.Close)

	status, body := get(t, srv.URL+"/v1/records/run", "ann")

	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, "internal error\n", body)
}

// The OpenAPI document answers without a key and holds the published schema of every kind the server keeps and every route
func TestServeHandlerOpenAPI(t *testing.T) {
	srv := httptest.NewServer(newServeHandler(fileTenants{base: t.TempDir()}, serverConfig{}, time.Now, io.Discard))
	t.Cleanup(srv.Close)
	ix, err := schema.Published.Index()
	require.NoError(t, err)
	want := map[string]string{}
	var names []string
	for _, k := range kinds(ix.Kinds).served() {
		b, err := k.File(k.Version)
		require.NoError(t, err)
		want[k.Name], names = canonical(t, b), append(names, k.Name)
	}

	status, body := get(t, srv.URL+"/v1/openapi.json", "")

	require.Equal(t, http.StatusOK, status)
	var doc struct {
		OpenAPI    string                     `json:"openapi"`
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &doc))
	var records struct {
		Get struct {
			Parameters []struct {
				Schema struct {
					Enum []string `json:"enum"`
				} `json:"schema"`
			} `json:"parameters"`
		} `json:"get"`
	}
	require.NoError(t, json.Unmarshal(doc.Paths["/v1/records/{kind}"], &records))
	got := map[string]string{}
	for name, s := range doc.Components.Schemas {
		got[name] = canonical(t, s)
	}
	page, payload, answer := got["page"], got["intervene"], got["intervened"]
	delete(got, "page")
	delete(got, "intervene")
	delete(got, "intervened")
	assert.Equal(t, openAPIVersion, doc.OpenAPI)
	assert.ElementsMatch(t, []string{"/healthz", "/mcp", "/v1/openapi.json", "/v1/records/{kind}", "/v1/reports/{name}", "/v1/interventions"},
		slices.Collect(maps.Keys(doc.Paths)))
	assert.Equal(t, names, records.Get.Parameters[0].Schema.Enum)
	assert.NotContains(t, names, "guard")
	assert.Equal(t, want, got)
	assert.Contains(t, page, `"#/components/schemas/intervention"`)
	assert.Contains(t, payload, `"enum":["judge","answer","dismiss","pause","resume","knob","reanchor","digest","reverse"]`)
	assert.Contains(t, answer, `"#/components/schemas/intervention"`)
}

// Published files that lack the index or the file of a kind answer no document and log why
func TestOpenAPIHandlerRefuses(t *testing.T) {
	index := `{"kinds":[{"kind":"verdict","version":1,"versions":[1],"store":"feedback.jsonl"}]}`
	tcs := []struct {
		name string
		args fstest.MapFS
		want string
	}{
		{"no index", fstest.MapFS{}, schema.ErrIndexInvalid.Error()},
		{"no file of a kind", fstest.MapFS{schema.IndexFile: {Data: []byte(index)}}, schema.ErrIndexInvalid.Error()},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var log bytes.Buffer
			rec := httptest.NewRecorder()

			openAPIHandler{files: schema.NewFiles(tc.args), version: "dev", log: &log}.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/openapi.json", nil))

			assert.Equal(t, http.StatusInternalServerError, rec.Code)
			assert.Equal(t, "internal error\n", rec.Body.String())
			assert.Contains(t, log.String(), tc.want)
		})
	}
}

// The JSON with its keys sorted so two encodings of one value compare equal
func canonical(t *testing.T, b []byte) string {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(b, &v))
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return string(out)
}
