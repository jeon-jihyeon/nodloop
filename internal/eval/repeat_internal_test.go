package eval

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExactMcNemar(t *testing.T) {
	type args struct {
		fixed, regressed int
	}
	tcs := []struct {
		name string
		args args
		want float64
	}{
		{"no discordant event", args{0, 0}, 1},
		{"two fixed of two as on the demo", args{2, 0}, 0.5},
		{"six fixed of six", args{6, 0}, 0.03125},
		{"a balanced split", args{3, 3}, 1},
		{"order of the counts does not matter", args{0, 6}, 0.03125},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.InDelta(t, tc.want, exactMcNemar(tc.args.fixed, tc.args.regressed), 1e-12)
		})
	}
}
