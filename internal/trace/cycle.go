package trace

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"time"
)

const week = 7 * 24 * time.Hour

// One cycle of curate within which the knobs, the seed and the policy version stay fixed
type Cycle struct {
	// The date of the Monday the cycle began
	ID    string
	Start time.Time
	// Where the next cycle starts
	End time.Time
}

// Whether the time lies in the cycle
func (c Cycle) Holds(t time.Time) bool {
	return !t.Before(c.Start) && t.Before(c.End)
}

// Cycles in time order with no gap between two of them
type Cycles []Cycle

// The first cycle the knob traces mark and false before the first knob trace
// The cycles up to the one that holds the zero time are the first alone
func (ts Traces) FirstCycle() (Cycle, bool) {
	cs := ts.Cycles(time.Time{})
	if len(cs) == 0 {
		return Cycle{}, false
	}
	return cs[0], true
}

// The first cycle that has not ended by now
func (cs Cycles) Open(now time.Time) (Cycle, bool) {
	for _, c := range cs {
		if c.End.After(now) {
			return c, true
		}
	}
	return Cycle{}, false
}

// The fields of a knob trace that mark a cycle
// curate writes them at the boundary of each cycle and at a narrowing inside one
type knobMark struct {
	CycleID string    `json:"cycle_id"`
	Start   time.Time `json:"start"`
	Knobs   struct {
		CycleWeeks int `json:"cycle_weeks"`
	} `json:"knobs"`
	// The trace the mark was read from
	at time.Time
	id string
}

// Whether the mark comes before the other one of its cycle
func (m knobMark) earlier(other knobMark) bool {
	return m.at.Before(other.at) || m.at.Equal(other.at) && m.id < other.id
}

// The cycles from the start of the mark up to the next marked start or up to the cycle that holds now
// Reports whether the cycle that holds now was reached
func (m knobMark) cycles(next, now time.Time) (Cycles, bool) {
	length := time.Duration(m.Knobs.CycleWeeks) * week
	var out Cycles
	id, start := m.CycleID, m.Start.UTC()
	for {
		end := start.Add(length)
		if !next.IsZero() && end.After(next) {
			end = next
		}
		if end.After(start) {
			out = append(out, Cycle{ID: id, Start: start, End: end})
			if end.After(now) {
				return out, true
			}
		}
		if !next.IsZero() && !end.Before(next) {
			return out, false
		}
		id, start = end.Format(CycleDate), end
	}
}

// The cycles the knob traces mark from the first one up to the cycle that holds now
// 1. a cycle starts at the boundary its first knob trace records and lasts the weeks that trace records
// 2. the next one starts where it ends with the same length until a knob trace records another
// So a cycle closes by its length even when no later pass started the next one
// 3. a cycle ends early where a later knob trace starts one
// 4. a trace of another name, a knob trace that does not decode and one without a start or a positive length mark nothing
// So there is no cycle before the first knob trace and none from year one on
func (ts Traces) Cycles(now time.Time) Cycles {
	marks := map[string]knobMark{}
	for _, t := range ts {
		m := knobMark{at: t.Time, id: t.ID}
		if t.Name != NameKnob || json.Unmarshal(t.Input, &m) != nil || m.CycleID == "" || m.Start.IsZero() || m.Knobs.CycleWeeks <= 0 {
			continue
		}
		if old, ok := marks[m.CycleID]; ok && !m.earlier(old) {
			continue
		}
		marks[m.CycleID] = m
	}
	ordered := slices.SortedFunc(maps.Values(marks), func(a, b knobMark) int {
		return cmp.Or(a.Start.Compare(b.Start), cmp.Compare(a.CycleID, b.CycleID))
	})
	var out Cycles
	for i, m := range ordered {
		var next time.Time
		if i+1 < len(ordered) {
			next = ordered[i+1].Start.UTC()
		}
		cs, open := m.cycles(next, now)
		out = append(out, cs...)
		if open {
			break
		}
	}
	return out
}
