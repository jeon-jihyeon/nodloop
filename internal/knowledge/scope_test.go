package knowledge_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

func TestScopeString(t *testing.T) {
	tcs := []struct {
		name string
		args knowledge.Scope
		want string
	}{
		{"empty scope reads as any event", knowledge.Scope{}, "any event"},
		{
			"contexts and metrics are joined with or",
			knowledge.Scope{Scope: evidence.Scope{ChangeContexts: []evidence.Context{"a", "b"}, Metrics: []string{"m", "n"}}},
			"change contexts a or b. metrics m or n",
		},
		{
			"dims come sorted by key",
			knowledge.Scope{Dims: map[string]string{"source": "s1", "app": "a1"}},
			"app=a1. source=s1",
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.String())
		})
	}
}

// The embedded procedure scope keeps the JSON of every stored record
// The first two lines are demo records saved as they were stored
func TestScopeJSON(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "scope.jsonl"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	tcs := []struct {
		name string
		// Line of the fixture
		args int
	}{
		{"metrics only", 0},
		{"change contexts only", 1},
		{"dims beside the procedure scope", 2},
		{"an empty scope with exceptions", 3},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Greater(t, len(lines), tc.args)
			var k knowledge.Knowledge
			require.NoError(t, json.Unmarshal([]byte(lines[tc.args]), &k))

			got, err := json.Marshal(k)

			assert.NoError(t, err)
			assert.Equal(t, lines[tc.args], string(got))
		})
	}
}
