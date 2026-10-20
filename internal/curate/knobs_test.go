package curate_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The knob event of the cycle that started on Monday 2026-09-21 with two weeks and the start knobs
func startKnobs() curate.KnobEvent {
	return curate.KnobEvent{
		CycleID: "2026-09-21", Start: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), Seed: "5eed",
		Knobs: curate.Knobs{ItemHold: 0.1, CycleWeeks: 2}, Cause: curate.KnobStart,
		Judge: curate.JudgeConfig{Model: "claude-sonnet-5-5", Effort: curate.JudgeEffort, Prompt: curate.PromptVersion},
	}
}

func TestKnobEventTrace(t *testing.T) {
	with := func(change func(*curate.KnobEvent)) curate.KnobEvent {
		k := startKnobs()
		change(&k)
		return k
	}
	tcs := []struct {
		name string
		args curate.KnobEvent
		want error
	}{
		{"the knobs of a cycle start", startKnobs(), nil},
		{"a narrowing keeps the cycle and names the event before", with(func(k *curate.KnobEvent) { k.Cause, k.Before = curate.KnobNarrow, "k-1" }), nil},
		{"a start on another day is refused", with(func(k *curate.KnobEvent) { k.Start = k.Start.Add(24 * time.Hour) }), curate.ErrKnobInvalid},
		{"a start after midnight is refused", with(func(k *curate.KnobEvent) { k.Start = k.Start.Add(time.Hour) }), curate.ErrKnobInvalid},
		{"an id of another date is refused", with(func(k *curate.KnobEvent) { k.CycleID = "2026-09-28" }), curate.ErrKnobInvalid},
		{"a cycle of no week is refused", with(func(k *curate.KnobEvent) { k.Knobs.CycleWeeks = 0 }), curate.ErrKnobInvalid},
		{"a cycle of five weeks is refused", with(func(k *curate.KnobEvent) { k.Knobs.CycleWeeks = 5 }), curate.ErrKnobInvalid},
		{"an item hold Check refuses is refused", with(func(k *curate.KnobEvent) { k.Knobs.ItemHold = 1 }), knowledge.ErrItemHoldInvalid},
		{"no seed is refused", with(func(k *curate.KnobEvent) { k.Seed = "" }), curate.ErrKnobInvalid},
		{"an unknown cause is refused", with(func(k *curate.KnobEvent) { k.Cause = "widen" }), curate.ErrKnobInvalid},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.Trace("0.10.0", at)

			assert.ErrorIs(t, err, tc.want)
			assert.Equal(t, tc.want == nil, got.Name == trace.NameKnob)
		})
	}
}

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
