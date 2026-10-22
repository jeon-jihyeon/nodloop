package classify_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
)

func TestTriageLabeled(t *testing.T) {
	good := classify.Triage{Nature: classify.NaturePreference, Reversibility: classify.ReversibilityH, Protected: classify.ProtectionNone}
	with := func(change func(*classify.Triage)) classify.Triage { copy := good; change(&copy); return copy }
	tcs := []struct {
		name string
		args classify.Triage
		want bool
	}{
		{"every label in its set", good, true},
		{"an unknown nature", with(func(v *classify.Triage) { v.Nature = "other" }), false},
		{"an unknown reversibility", with(func(v *classify.Triage) { v.Reversibility = "other" }), false},
		{"an unknown protection", with(func(v *classify.Triage) { v.Protected = "other" }), false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Labeled())
		})
	}
}
