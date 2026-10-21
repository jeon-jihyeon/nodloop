package mcp_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

type recordsPage struct {
	Records []json.RawMessage `json:"records"`
	Next    string            `json:"next"`
}

// The ids of the records and whether each validates against the published schema of its kind
func (p recordsPage) checked(t *testing.T, kind string) []string {
	t.Helper()
	ix, err := schema.Published.Index()
	require.NoError(t, err)
	k, err := ix.Kind(kind)
	require.NoError(t, err)
	var ids []string
	for _, r := range p.Records {
		var head struct {
			ID string `json:"record_id"`
		}
		require.NoError(t, json.Unmarshal(r, &head))
		require.NoError(t, k.Validate(schema.Record{Kind: kind, ID: head.ID, JSON: r}))
		ids = append(ids, head.ID)
	}
	return ids
}

// Records of the stores a server wrote page across a cursor with none skipped or repeated
// A run recorded after an empty page comes on the next one
func TestServerRecords(t *testing.T) {
	st := testkit.Open(t)
	c := connect(t, st)
	first, second := recordRun(t, c, "nodloop"), recordRun(t, c, "nodloop")
	require.NoError(t, c.Run(t, "feedback", map[string]any{"trace_id": first, "verdict": "reject", "reason": "ran cd"}))
	require.NoError(t, c.Run(t, "propose", map[string]any{"id": "git-c", "kind": "judgment", "content": "use git -C", "from": first}))
	page := func(args map[string]any) recordsPage {
		var p recordsPage
		require.NoError(t, c.Call(t, "records", args, &p))
		return p
	}

	one := page(map[string]any{"kind": "run", "limit": 1})
	two := page(map[string]any{"kind": "run", "limit": 1, "since": one.Next})
	end := page(map[string]any{"kind": "run", "since": two.Next})
	later := recordRun(t, c, "nodloop")
	next := page(map[string]any{"kind": "run", "since": end.Next})
	shown := page(map[string]any{"kind": "run", "limit": 1, "content": true})
	verdicts := page(map[string]any{"kind": "verdict"})
	items := page(map[string]any{"kind": "knowledge"})

	assert.Equal(t, []string{first}, one.checked(t, "run"))
	assert.Equal(t, []string{second}, two.checked(t, "run"))
	assert.Empty(t, end.checked(t, "run"))
	assert.Equal(t, []string{later}, next.checked(t, "run"), "a run recorded since the empty page comes next")
	assert.NotContains(t, string(one.Records[0]), "git status", "the output is text")
	assert.Contains(t, string(shown.Records[0]), "git status", "content keeps the output")
	assert.Len(t, verdicts.checked(t, "verdict"), 1)
	assert.NotContains(t, string(verdicts.Records[0]), "ran cd")
	assert.Len(t, items.checked(t, "knowledge"), 1)
	assert.NotContains(t, string(items.Records[0]), "use git -C")
}

// A kind outside the published index is refused by the schema, a cursor that names no record and a kind the server keeps no store for by the page
func TestServerRecordsRefuses(t *testing.T) {
	c := connect(t, testkit.Open(t))
	tcs := []struct {
		name string
		args map[string]any
		want string
	}{
		{"a kind outside the index", map[string]any{"kind": "session"}, "kind"},
		{"a cursor that names no record", map[string]any{"kind": "run", "since": "%%%"}, schema.ErrCursorInvalid.Error()},
		{"the guard log of a server that holds none", map[string]any{"kind": "guard"}, schema.ErrNotServed.Error()},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Run(t, "records", tc.args)

			assert.ErrorContains(t, err, tc.want)
		})
	}
}
