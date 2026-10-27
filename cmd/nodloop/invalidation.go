package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type turnInput struct {
	// Absent on a turn of an earlier release so an empty context still reads as known
	Context *[]knowledge.Ref `json:"context,omitempty"`
	// The cued versions a tool call of the conversation made eligible since the context was last emptied with their chances
	// Carried from turn to turn like the context so the stop of every later turn still counts them as the calls met them
	Eligible []knowledge.Eligible `json:"eligible,omitempty"`
	// The eligible versions the item hold kept back at those calls
	Held []knowledge.Ref `json:"held,omitempty"`
}

type promptRefs []knowledge.Ref

// Retirement changes future injection and never erases a lesson already in the conversation
func (refs promptRefs) invalidated(retired map[knowledge.Ref]bool) string {
	var names []string
	for _, ref := range refs {
		if retired[ref] {
			names = append(names, fmt.Sprintf("%s v%d", ref.ID, ref.Version))
		}
	}
	if len(names) == 0 {
		return ""
	}
	text := "nodloop: stop following inactive items " + strings.Join(names, ", ") +
		". Their text may remain until /compact or /clear. Prior outputs need separate correction.\n"
	if utf8.RuneCountInString(text) > contextRunes-promptRunes {
		return "nodloop: earlier items became inactive. Check current knowledge before following earlier nodloop lessons. " +
			"Their text remains until /compact or /clear. Prior outputs need separate correction.\n"
	}
	return text
}

// The item refs the context of the conversation holds and the cued versions its calls made eligible
// 1. a refill newer than the newest turn replaced the context with what it added and made nothing eligible
// 2. else the newest turn that recorded its context
// 3. else the answered runs tell it as before turns recorded their context
// 4. the cue traces of the conversation after that checkpoint add the versions its calls received and made eligible
func (ts sessionTraces) contextRefs(all knowledge.Set, refill trace.Trace) cueState {
	base, since := ts.checkpoint(all, refill)
	return base.with(ts.cues("", since))
}

// The context the newest checkpoint of the conversation recorded and when
func (ts sessionTraces) checkpoint(all knowledge.Set, refill trace.Trace) (cueState, time.Time) {
	for _, t := range ts {
		if t.Name != trace.NameTurn {
			continue
		}
		if refill.ID != "" && refill.Time.After(t.Time) {
			var in knowledge.RunInput
			if json.Unmarshal(refill.Input, &in) == nil {
				return cueState{delivered: in.Applied}, refill.Time
			}
			return cueState{}, refill.Time
		}
		var in turnInput
		if json.Unmarshal(t.Input, &in) == nil && in.Context != nil {
			return cueState{delivered: *in.Context, eligible: in.Eligible, held: in.Held}, t.Time
		}
	}
	return cueState{delivered: ts.runs().answered().inContext(all, refill)}, refill.Time
}

// The refs the context holds after this prompt with one version per id
// A retired ref leaves since the notice told the session to stop following it
func (refs promptRefs) continued(retired map[knowledge.Ref]bool, added []knowledge.Ref) []knowledge.Ref {
	byID := map[string]knowledge.Ref{}
	for _, ref := range refs {
		if !retired[ref] {
			byID[ref.ID] = ref
		}
	}
	for _, ref := range added {
		byID[ref.ID] = ref
	}
	out := []knowledge.Ref{}
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		out = append(out, byID[id])
	}
	return out
}

// Whether the newest record of each ref retired it
func (refs promptRefs) retired(all knowledge.Set) map[knowledge.Ref]bool {
	if len(refs) == 0 {
		return nil
	}
	statuses := make(map[knowledge.Ref]bool, len(refs))
	for _, ref := range refs {
		statuses[ref] = false
	}
	for _, k := range slices.Backward(all) {
		ref := knowledge.Ref{ID: k.ID, Version: k.Version}
		if _, known := statuses[ref]; known {
			statuses[ref] = k.Status == knowledge.StatusRetired
		}
	}
	return statuses
}
