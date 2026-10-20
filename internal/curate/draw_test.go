package curate_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The cycle the draws read their seed from
var drawn = curate.KnobEvent{CycleID: "2026-09-21", PolicyVersion: "0.10.0+1a2b3c4d", Seed: "5eed"}

// The audit draws on the decide id under the seed so one decide always draws alike and a share of p draws about p
// When the pass of the fixtures wrote its records
var written = time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)

func TestKnobEventAuditDraw(t *testing.T) {
	type args struct {
		event curate.KnobEvent
		ids   int
		p     float64
	}
	type want struct {
		low, high int
	}
	other := drawn
	other.Seed = "other"
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a share of 0.15 over 1000 decide ids", args{drawn, 1000, 0.15}, want{120, 180}},
		{"a share of 0.05 over 1000 decide ids", args{drawn, 1000, 0.05}, want{30, 70}},
		{"another seed draws its own share", args{other, 1000, 0.15}, want{120, 180}},
		{"p 0 draws none", args{drawn, 1000, 0}, want{0, 0}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			count, fixed := 0, true
			for i := range tc.args.ids {
				id := fmt.Sprintf("d-%d", i)
				d, again := tc.args.event.AuditDraw(id, tc.args.p), tc.args.event.AuditDraw(id, tc.args.p)
				fixed = fixed && d.DrawID == again.DrawID && d.Drawn == again.Drawn && d.Check(trace.SourcePush) == nil && d.Unit == id
				if d.Drawn {
					count++
				}
			}

			assert.True(t, fixed, "every draw is the same twice and checks as pushed")
			assert.GreaterOrEqual(t, count, tc.want.low)
			assert.LessOrEqual(t, count, tc.want.high)
		})
	}
}

// A stop picks one due shadow decision uniformly with the cycle seed and records the session and p_select
func TestMarkShadowPick(t *testing.T) {
	mark := curate.Mark{CycleID: drawn.CycleID, PolicyVersion: drawn.PolicyVersion, Seed: drawn.Seed}
	due := func(kind curate.QuestionKind, id string) curate.Due {
		return curate.Due{Kind: kind, Decide: id, Change: curate.ChangeAdd, Ref: knowledge.Ref{ID: "k-" + id, Version: 1}, At: written}
	}
	three := []curate.Due{
		due(curate.QuestionDirection, "d0"), due(curate.QuestionShadow, "d1"), due(curate.QuestionShadow, "d2"), due(curate.QuestionShadow, "d3"),
	}
	type want struct {
		picked map[string]int
		p      float64
		ok     bool
	}
	tcs := []struct {
		name string
		args []curate.Due
		want want
	}{
		{"three due shadow decisions share 900 sessions", three, want{map[string]int{"d1": 300, "d2": 300, "d3": 300}, 1.0 / 3, true}},
		{"one due shadow decision is picked every time", three[:2], want{map[string]int{"d1": 900}, 1, true}},
		{"no due shadow decision picks none", three[:1], want{map[string]int{}, 0, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := want{picked: map[string]int{}}
			fixed := true
			for i := range 900 {
				session := fmt.Sprintf("s-%d", i)
				d, draw, ok := mark.ShadowPick(session, tc.args)
				again, _, _ := mark.ShadowPick(session, tc.args)
				fixed = fixed && d == again && (!ok || draw.Unit == session && draw.Check(trace.SourcePush) == nil && draw.Drawn)
				if ok {
					got.picked[d.Decide]++
					got.p = *draw.P
				}
				got.ok = ok
			}

			assert.True(t, fixed, "a session always picks alike under one seed")
			assert.Equal(t, tc.want.ok, got.ok)
			assert.InDelta(t, tc.want.p, got.p, 1e-9)
			require.Len(t, got.picked, len(tc.want.picked))
			for id, n := range tc.want.picked {
				assert.InDelta(t, n, got.picked[id], 60, id)
			}
		})
	}
}

// The mark keeps the time of the last daily pass and a day after it the next one is due
func TestMarkDaily(t *testing.T) {
	at := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	tcs := []struct {
		name string
		args curate.Mark
		want bool
	}{
		{"no daily pass yet", curate.Mark{}, true},
		{"a pass an hour ago", curate.Mark{At: at.Add(-time.Hour)}, false},
		{"a pass a day ago", curate.Mark{At: at.Add(-24 * time.Hour)}, true},
		{"a pass in the future of a clock set back", curate.Mark{At: at.Add(time.Hour)}, true},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.args.Daily(at))
		})
	}
}
