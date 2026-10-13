package knowledge_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

// Fixed draws so a change of the hash never moves a recorded turn to the other arm
// The fraction of each key is in the name so a reader sees why it lands on its side of each share
func TestHoldoutDraws(t *testing.T) {
	type want struct {
		tenth, half bool
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{"first turn of a session at 0.918", "0b9c2f1e-5d1a-4c55-9f0e-2b7f5c3a9d10:", want{false, false}},
		{"later turn at 0.823", "0b9c2f1e-5d1a-4c55-9f0e-2b7f5c3a9d10:019a1f2c3d4e5f60a1b2c3d4", want{false, false}},
		{"turn at 0.566", "7e4d1a90-3c2b-4f8e-a6d5-0c1b2a3f4e5d:019a1f2c3d4f7a8b9c0d1e2f", want{false, false}},
		{"short session at 0.404", "a:b", want{false, true}},
		{"turn at 0.036", "session-2:019a00011667", want{true, true}},
		{"turn at 0.025", "session-3:019a0002e668", want{true, true}},
		{"turn at 0.060", "session-4:019a0003dde0", want{true, true}},
		{"turn at 0.293", "session-1:019a00001eef", want{false, true}},
		{"turn at 0.494", "session-2:019a00003dde", want{false, true}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := want{knowledge.Holdout(0.1).Withholds(tc.args), knowledge.Holdout(0.5).Withholds(tc.args)}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestHoldoutWithholds(t *testing.T) {
	type args struct {
		share knowledge.Holdout
		key   string
	}
	tcs := []struct {
		name string
		args args
		want bool
	}{
		{"no share withholds nothing", args{0, "s1:"}, false},
		{"an empty key is never drawn", args{0.999, ""}, false},
		{"a share near 1 withholds the key", args{0.999, "s1:"}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.share.Withholds(tc.args.key))
		})
	}
}

// A producer key draws about the share and the same key always draws the same
func TestHoldoutWithholdsRun(t *testing.T) {
	drawn := 0
	for window := range 1000 {
		key := fmt.Sprintf("window-%d", window)
		if knowledge.Holdout(0.5).WithholdsRun("stream-bot", key) {
			drawn++
		}
		assert.Equal(t, knowledge.Holdout(0.5).WithholdsRun("stream-bot", key), knowledge.Holdout(0.5).Withholds("stream-bot:"+key))
	}
	assert.InDelta(t, 500, drawn, 50)
	assert.False(t, knowledge.Holdout(0.999).WithholdsRun("stream-bot", ""))
}

func TestHoldoutCheck(t *testing.T) {
	tcs := []struct {
		name string
		args knowledge.Holdout
		want error
	}{
		{"0 withholds nothing and passes", 0, nil},
		{"a share below 1 passes", 0.999, nil},
		{"1 leaves no run to apply the items", 1, knowledge.ErrHoldoutInvalid},
		{"a negative share is refused", -0.1, knowledge.ErrHoldoutInvalid},
		{"no number is refused", knowledge.Holdout(math.NaN()), knowledge.ErrHoldoutInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, tc.args.Check(), tc.want)
		})
	}
}

// Every run of one key shares its unit and the unit never holds the key
func TestHoldoutDrawnUnit(t *testing.T) {
	type args struct {
		producer, key string
	}
	type want struct {
		drawn bool
		unit  string
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a session key", args{"claude-code", "s1"}, want{true, "054a070bf4bb1f45"}},
		{"the same key of another producer is another unit", args{"stream-bot", "s1"}, want{true, "d8f9b987c7d2efd7"}},
		{"an empty key goes through no draw", args{"stream-bot", ""}, want{false, ""}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in, err := knowledge.NewRunInput(nil, nil, "")
			assert.NoError(t, err)

			first := knowledge.Holdout(0.1).Drawn(in, tc.args.producer, tc.args.key)
			second := knowledge.Holdout(0.1).Drawn(in, tc.args.producer, tc.args.key)

			assert.Equal(t, tc.want, want{first.Drawn, first.Unit})
			assert.Equal(t, first.Unit, second.Unit)
			assert.NotContains(t, first.Unit, tc.args.producer)
		})
	}
}
