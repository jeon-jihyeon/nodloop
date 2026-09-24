package knowledge_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
			knowledge.Scope{ChangeContexts: []evidence.Context{"a", "b"}, Metrics: []string{"m", "n"}},
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
