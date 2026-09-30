package knowledge_test

import (
	"encoding/json"
	"errors"
	"fmt"
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

func TestScopeObserved(t *testing.T) {
	metrics := []string{"click_count", "conversion_count"}
	dims := knowledge.Dims{"source": {"source-a": {}}}
	tcs := []struct {
		name string
		args knowledge.Scope
		want error
	}{
		{"an empty scope names nothing to check", knowledge.Scope{}, nil},
		{
			"a metric and a dim value the events carry pass",
			knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"click_count"}}, Dims: map[string]string{"source": "source-a"}},
			nil,
		},
		{
			"a metric no event carries is named",
			knowledge.Scope{Scope: evidence.Scope{Metrics: []string{"conversions"}}},
			fmt.Errorf("%w: metric conversions", knowledge.ErrScopeUnobserved),
		},
		{
			"a dim value and a dim key no event carries are named in key order",
			knowledge.Scope{Dims: map[string]string{"source": "source_a", "platform": "ios"}},
			fmt.Errorf("%w: dim platform=ios, dim source=source_a", knowledge.ErrScopeUnobserved),
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.args.Observed(metrics, dims)
			assert.ErrorIs(t, err, errors.Unwrap(tc.want))
			assert.Equal(t, fmt.Sprint(tc.want), fmt.Sprint(err))
		})
	}
}

func TestDimsNames(t *testing.T) {
	tcs := []struct {
		name string
		args knowledge.Dims
		want []string
	}{
		{"names come sorted", knowledge.Dims{"topic": {"t": {}}, "source": {"s": {}}, "Source": {"s": {}}}, []string{"Source", "source", "topic"}},
		{"no dimensions is an empty list", knowledge.Dims{}, []string{}},
		{"a nil set is an empty list", nil, []string{}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.args.Names()
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got)
		})
	}
}
