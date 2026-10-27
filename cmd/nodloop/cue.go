package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	knowledgefile "github.com/jeon-jihyeon/nodloop/internal/knowledge/file"
	"github.com/jeon-jihyeon/nodloop/internal/markfile"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	tracefile "github.com/jeon-jihyeon/nodloop/internal/trace/file"
)

// The lock the tool hooks of a record directory take in turn once a call has cued items to record
// So of two calls in parallel the second reads what the first recorded
const cueLock = "cue.lock"

// The marker a line cut to the room left ends with
const cutMark = " [cut by nodloop]\n"

// The input of a cue trace
type cueInput struct {
	// The versions this call received
	Context []knowledge.Ref `json:"context"`
	// The cued versions this call fitted with the chance its session includes each as the item hold drew it at the call
	// The stop reads them as observed so a later approval or reaffirm never changes what the call met
	Eligible []knowledge.Eligible `json:"eligible"`
	// The eligible versions the item hold kept from the session at the call
	Held []knowledge.Ref `json:"held,omitempty"`
}

// The cued items one context holds, those its calls made eligible and those the item hold kept back at them
type cueState struct {
	delivered []knowledge.Ref
	eligible  []knowledge.Eligible
	held      []knowledge.Ref
}

// The state with the later one added
// 1. a later version of an id replaces the earlier one
// 2. a later observation of an id replaces whether the hold kept it
// So a version a later call delivered never stays held
func (s cueState) with(later cueState) cueState {
	observed := eligibleRefs(later.eligible)
	held := slices.DeleteFunc(slices.Clone(s.held), func(r knowledge.Ref) bool {
		return slices.ContainsFunc(observed, func(o knowledge.Ref) bool { return o.ID == r.ID })
	})
	return cueState{
		delivered: joinRefs(s.delivered, later.delivered), eligible: joinEligible(s.eligible, later.eligible), held: joinRefs(held, later.held),
	}
}

// The eligible versions of one id once each with the later ones winning in id order
// The versions come back as they are when nothing is added
func joinEligible(eligible, added []knowledge.Eligible) []knowledge.Eligible {
	if len(added) == 0 {
		return eligible
	}
	byID := map[string]knowledge.Eligible{}
	for _, list := range [][]knowledge.Eligible{eligible, added} {
		for _, e := range list {
			byID[e.ID] = e
		}
	}
	out := []knowledge.Eligible{}
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		out = append(out, byID[id])
	}
	return out
}

// The refs of the eligible versions
func eligibleRefs(eligible []knowledge.Eligible) []knowledge.Ref {
	out := make([]knowledge.Ref, 0, len(eligible))
	for _, e := range eligible {
		out = append(out, e.Ref)
	}
	return out
}

// The input of a run with the cued versions the calls of its conversation met
// 1. a held version is held and a version a refusal delivered is applied
// A version the cut left due is eligible and never applied
// 2. the chances join the eligible list only while the item hold draws as for every other version
func (s cueState) into(in knowledge.RunInput) knowledge.RunInput {
	drawn := in.Eligible != nil
	for _, e := range s.eligible {
		switch {
		case slices.Contains(s.held, e.Ref):
			in.Held = append(in.Held, e.Ref)
		case slices.Contains(s.delivered, e.Ref):
			in.Applied = append(in.Applied, e.Ref)
		}
		if drawn {
			in.Eligible = append(in.Eligible, e)
		}
	}
	return in
}

// The input of a run of a withheld session with every cued version its calls met as withheld with no chance of receiving it
func (s cueState) withheldInto(in knowledge.RunInput) knowledge.RunInput {
	drawn := in.Eligible != nil
	for _, e := range s.eligible {
		in.Withheld = append(in.Withheld, e.Ref)
		if drawn {
			in.Eligible = append(in.Eligible, knowledge.Eligible{Ref: e.Ref})
		}
	}
	return in
}

// The refs with the added ones in id order and one version per id
// The refs come back as they are when nothing is added
func joinRefs(refs, added []knowledge.Ref) []knowledge.Ref {
	if len(added) == 0 {
		return refs
	}
	byID := map[string]knowledge.Ref{}
	for _, list := range [][]knowledge.Ref{refs, added} {
		for _, ref := range list {
			byID[ref.ID] = ref
		}
	}
	out := []knowledge.Ref{}
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		out = append(out, byID[id])
	}
	return out
}

// What the cue traces of one agent after since recorded oldest first
// The conversation is the empty agent
func (ts sessionTraces) cues(agent string, since time.Time) cueState {
	var out cueState
	for _, t := range slices.Backward(trace.Traces(ts)) {
		if t.Name != trace.NameCue || t.Subject != agent || !t.Time.After(since) {
			continue
		}
		var in cueInput
		if json.Unmarshal(t.Input, &in) == nil {
			out = out.with(cueState{delivered: in.Context, eligible: in.Eligible, held: in.Held})
		}
	}
	return out
}

// The context of the agent a call runs in
// 1. a subagent holds only what its own calls received so its lessons never count for the conversation
// Its cue traces are read apart since the runs that bound a read of the conversation say nothing of how long it has run
// 2. the conversation holds its newest checkpoint and the cue traces after it
func (c hookCommand) agentContext(ctx context.Context, in hookInput, all knowledge.Set) (cueState, error) {
	if in.AgentID != "" {
		store, err := c.app.traces()
		if err != nil {
			return cueState{}, err
		}
		found, err := store.List(ctx, record.TraceFilter{
			Name: string(trace.NameCue), SessionID: in.SessionID, Subject: in.AgentID, Since: c.app.now().Add(-sessionWindow),
		})
		return sessionTraces(trace.RestoreAll(found)).cues(in.AgentID, time.Time{}), err
	}
	recent, err := c.recent(ctx, in.SessionID, trace.NameRefill, trace.NameTurn, trace.NameCue)
	return recent.contextRefs(all, recent.newest(trace.NameRefill, time.Time{})), err
}

// The cued items a tool call fits refused once with their lines so the model reads them before it calls again
// 1. a hook without cues runs for a plugin that adds them with the prompt so it does nothing
// 2. the items are the cued items of ForAt whose cue fits a shape of the call
// The item hold keeps its versions out as from the prompts
// 3. a version the context of the agent lacks is due
// 4. a withheld session refuses nothing and records what its calls fitted so the stop counts it withheld
// 5. a call some cued item fits reads its context and takes the lock only when it has something to record
// Under the lock it reads the context again so the second of two calls in parallel finds what the first recorded and passes
// 6. the refusal comes only after the cue trace landed so a failed record lets the call through and never refuses it again
func (c hookCommand) tool(ctx context.Context, in hookInput, labels trace.Labels) error {
	if !c.cues || in.SessionID == "" {
		return nil
	}
	shapes := call.Of(in.ToolName, in.ToolInput)
	if len(shapes) == 0 {
		return nil
	}
	all, err := c.all(ctx)
	if err != nil && !errors.Is(err, knowledgefile.ErrCorrupt) {
		return err
	}
	eligible := all.ForAt(sessionProducer, labels, c.app.now()).Fitting(shapes)
	if len(eligible) == 0 {
		return err
	}
	observed := c.observe(in.SessionID, eligible)
	pending, readErr := c.pending(ctx, in, all, eligible, observed)
	if readErr != nil || pending.empty() {
		return errors.Join(err, readErr)
	}
	dir, lockErr := c.app.makeRecordDir()
	if lockErr != nil {
		return errors.Join(err, lockErr)
	}
	held, lockErr := markfile.Wait[struct{}](filepath.Join(dir, cueLock))
	if lockErr != nil {
		return errors.Join(err, lockErr)
	}
	defer func() { _ = held.Release() }()
	if pending, readErr = c.pending(ctx, in, all, eligible, observed); readErr != nil || pending.empty() {
		return errors.Join(err, readErr)
	}
	shown, text := cueText(pending.due)
	observed.Context = shown.Refs()
	if recordErr := c.recordCue(ctx, in, observed); recordErr != nil {
		return errors.Join(err, recordErr)
	}
	if len(shown) == 0 {
		return err
	}
	return errors.Join(err, c.host.deny(c.out, text))
}

// What one call has left to deliver and to record
type cuePending struct {
	due knowledge.Set
	// The eligible versions no cue trace of the context names yet
	unnamed []knowledge.Ref
}

func (p cuePending) empty() bool {
	return len(p.due) == 0 && len(p.unnamed) == 0
}

// What the call met: every eligible version with the chance its session includes it and those the item hold keeps back
// A share of 0 draws nothing so every version has a chance of 1 and none is held
func (c hookCommand) observe(sessionID string, eligible knowledge.Set) cueInput {
	drawn := c.hold.Drawn(knowledge.RunInput{Applied: eligible.Refs()}, eligible, sessionProducer, sessionID)
	out := cueInput{Eligible: drawn.Eligible, Held: drawn.Held}
	if out.Eligible == nil {
		for _, ref := range eligible.Refs() {
			out.Eligible = append(out.Eligible, knowledge.Eligible{Ref: ref, PInclude: 1})
		}
	}
	return out
}

// The due items and the eligible versions the context of the agent of the call has not recorded
func (c hookCommand) pending(ctx context.Context, in hookInput, all, eligible knowledge.Set, observed cueInput) (cuePending, error) {
	state, err := c.agentContext(ctx, in, all)
	if err != nil && !errors.Is(err, tracefile.ErrCorrupt) {
		return cuePending{}, err
	}
	var out cuePending
	named := eligibleRefs(state.eligible)
	for _, ref := range eligible.Refs() {
		if !slices.Contains(named, ref) {
			out.unnamed = append(out.unnamed, ref)
		}
	}
	if c.holdout.WithholdsRun(sessionProducer, in.SessionID) {
		return out, nil
	}
	for _, k := range eligible {
		ref := knowledge.Ref{ID: k.ID, Version: k.Version}
		if !slices.Contains(state.delivered, ref) && !slices.Contains(observed.Held, ref) {
			out.due = append(out.due, k)
		}
	}
	return out, nil
}

// The cue trace of the call
func (c hookCommand) recordCue(ctx context.Context, in hookInput, cue cueInput) error {
	store, err := c.app.traces()
	if err != nil {
		return err
	}
	// Item refs always encode
	input, _ := json.Marshal(cue)
	now := c.app.now()
	tr := trace.Trace{ID: trace.NewID(now), Name: trace.NameCue, SessionID: in.SessionID, Subject: in.AgentID, Time: now, Input: input}
	return store.Append(ctx, tr.Record())
}

// The due items that fit the refusal and its text
// 1. the lead, every line, a count of the items left and the closing line stay within contextRunes
// The room keeps a count line for every due item so it never shrinks below the line the cut prints
// 2. when not even the first line fits it is cut to the room left and marked so one long item never refuses forever
func cueText(due knowledge.Set) (knowledge.Set, string) {
	if len(due) == 0 {
		return nil, ""
	}
	countLine := func(n int) string {
		if n == 0 {
			return ""
		}
		return fmt.Sprintf("- %d more items come at the next call\n", n)
	}
	room := contextRunes - utf8.RuneCountInString(knowledge.CueLead+knowledge.CueClose+countLine(len(due)))
	var shown knowledge.Set
	var b strings.Builder
	b.WriteString(knowledge.CueLead)
	used := 0
	for _, k := range due {
		line := k.PromptLine()
		if used+utf8.RuneCountInString(line) > room {
			break
		}
		used += utf8.RuneCountInString(line)
		b.WriteString(line)
		shown = append(shown, k)
	}
	if len(shown) == 0 {
		line := []rune(due[0].PromptLine())
		b.WriteString(string(line[:max(0, room-utf8.RuneCountInString(cutMark))]) + cutMark)
		shown = due[:1]
	}
	b.WriteString(countLine(len(due) - len(shown)))
	b.WriteString(knowledge.CueClose)
	return shown, b.String()
}
