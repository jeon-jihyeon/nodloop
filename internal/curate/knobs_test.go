package curate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// The item hold draws only once tier 1 carries out what it decides
func TestKnobsHold(t *testing.T) {
	tcs := []struct {
		name string
		args curate.Knobs
		want knowledge.ItemHold
	}{
		{"no knobs hold nothing", curate.Knobs{}, 0},
		{"tier 1 in shadow holds nothing at the start share", curate.Knobs{ItemHold: 0.1}, 0},
		{"tier 1 carrying out holds at its share", curate.Knobs{Activated: true, ItemHold: 0.1}, 0.1},
		{"a share of 0 holds nothing", curate.Knobs{Activated: true}, 0},
		{"a share Check refuses holds nothing", curate.Knobs{Activated: true, ItemHold: 1}, 0},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Hold())
		})
	}
}
