package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type turnInput struct {
	// Absent on a turn of an earlier release so an empty context still reads as known
	Context *[]knowledge.Ref `json:"context,omitempty"`
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

// The item refs the context of the session holds before this prompt
// 1. a refill newer than the newest turn replaced the context with what it added
// 2. else the newest turn that recorded its context
// 3. else the answered runs tell it as before turns recorded their context
func (ts sessionTraces) contextRefs(all knowledge.Set, refill trace.Trace) []knowledge.Ref {
	for _, t := range ts {
		if t.Name != trace.NameTurn {
			continue
		}
		if refill.ID != "" && refill.Time.After(t.Time) {
			var in knowledge.RunInput
			if json.Unmarshal(refill.Input, &in) == nil {
				return in.Applied
			}
			return nil
		}
		var in turnInput
		if json.Unmarshal(t.Input, &in) == nil && in.Context != nil {
			return *in.Context
		}
	}
	return ts.runs().answered().inContext(all, refill)
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
