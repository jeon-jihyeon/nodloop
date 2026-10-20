package curate

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The label of the working directory of a run
const labelDir = "dir"

// The temporary directories of the OS as absolute paths
type TempDirs []string

// Whether every dir label of the scope lies below one of them or holds a path segment scratchpad
// 1. no later session works there so a lesson scoped to it can never teach
// 2. a tmp folder inside a repository is a place like any other
// 3. a scope without a dir label lies nowhere in particular
func (ts TempDirs) Hold(scope knowledge.RunScope) bool {
	dirs := scope.Labels[labelDir]
	return len(dirs) > 0 && !slices.ContainsFunc(dirs, func(d string) bool { return !ts.holds(d) })
}

func (ts TempDirs) holds(dir string) bool {
	clean := filepath.ToSlash(filepath.Clean(dir))
	if slices.Contains(strings.Split(clean, "/"), "scratchpad") {
		return true
	}
	return slices.ContainsFunc(ts, func(t string) bool {
		root := filepath.ToSlash(filepath.Clean(t))
		return filepath.IsAbs(t) && (clean == root || strings.HasPrefix(clean, strings.TrimSuffix(root, "/")+"/"))
	})
}

// The text of a lesson as the tier 0 rules compare it: lower case, one space between words and no trailing period
func normalized(content string) string {
	return strings.TrimSuffix(strings.Join(strings.Fields(strings.ToLower(content)), " "), ".")
}

// The tier 0 decision of a candidate the record already settles with the older candidate a join adds its evidence to
// The rules run in a fixed order so a candidate meets the first that holds and a pass over the same records decides alike
func (p *pass) settled(ctx context.Context, k knowledge.Knowledge) (Decision, *knowledge.Ref, bool, error) {
	text := normalized(k.Content)
	same := func(o knowledge.Knowledge) bool { return normalized(o.Content) == text }
	if slices.ContainsFunc(p.all.Approved().Reaching(*k.Run), same) {
		d := Decision{RuleCode: RuleSameAsItem, Action: ActionDrop, Reason: knowledge.ReasonDuplicate, Failure: FailureHard, Reopen: ReopenPersonOnly}
		return d, nil, true, nil
	}
	if older, ok := p.older(k, same); ok {
		return Decision{RuleCode: RuleSameAsCandidate, Action: ActionJoin, Reason: knowledge.ReasonDuplicate}, &older, true, nil
	}
	corrected, err := p.corrected(ctx, k)
	if err != nil {
		return Decision{}, nil, false, err
	}
	if !corrected {
		d := Decision{RuleCode: RuleNoEvidence, Action: ActionDrop, Reason: knowledge.ReasonNoEvidence, Failure: FailureUnderexplored, Reopen: ReopenOtherSession}
		return d, nil, true, nil
	}
	if p.s.Temp.Hold(*k.Run) {
		d := Decision{RuleCode: RuleTempScope, Action: ActionDrop, Reason: knowledge.ReasonTempScope, Failure: FailureHard, Reopen: ReopenPermanentPlace}
		return d, nil, true, nil
	}
	return Decision{}, nil, false, nil
}

// The older waiting candidate of another id that says the same and reaches every run the candidate reaches
// Older by its first record and then by id so two candidates of one moment join one way
func (p *pass) older(k knowledge.Knowledge, same func(knowledge.Knowledge) bool) (knowledge.Ref, bool) {
	mine := p.all.ProposedAt(knowledge.Ref{ID: k.ID, Version: k.Version})
	for _, o := range p.all.AllWaiting().Reaching(*k.Run) {
		ref := knowledge.Ref{ID: o.ID, Version: o.Version}
		theirs := p.all.ProposedAt(ref)
		if o.ID != k.ID && same(o) && (theirs.Before(mine) || theirs.Equal(mine) && o.ID < k.ID) {
			return ref, true
		}
	}
	return knowledge.Ref{}, false
}

// Whether a run the evidence of the candidate cites still carries a correction as its reconciled verdict
// A candidate with no feedback run is no case of the rule since a person or a program proposed it on other evidence
func (p *pass) corrected(ctx context.Context, k knowledge.Knowledge) (bool, error) {
	if len(k.Evidence.FeedbackTraceIDs) == 0 {
		return true, nil
	}
	for _, run := range k.Evidence.FeedbackTraceIDs {
		verdicts, err := p.verdictsOf(ctx, run)
		if err != nil {
			return false, err
		}
		if len(verdicts) > 0 && verdicts[0].Corrects() {
			return true, nil
		}
	}
	return false, nil
}

// The tier 2 rule of a candidate whose change only the person decides
// Checked before any judge call since the hardest judgment is whether an earlier approval covers the change
// 1. person_item when it replaces a version a person approved or was built on one a person decided
// 2. scope_wider when no single version a person approved reaches every run its scope reaches
// 3. veto when it or the version it replaces carries a veto
// 4. room when its approval needs a compaction so its one question carries the prepared compaction
// 5. drift when versions nodloop approved in the place reach the lessons a person approved there
// 6. paused when a pause of the person covers its kind or its scope so no judge runs on it
func (p *pass) asks(k knowledge.Knowledge, run trace.Trace, place AnchorPlace) (RuleCode, bool, error) {
	cur, replaces := p.all.Approved().Find(k.ID)
	switch {
	case replaces && cur.Approver != knowledge.ApproverNodloop || k.Base > 0 && p.personDecided(k.ID, k.Base):
		return RulePersonItem, true, nil
	case !p.all.PersonReaches(*k.Run):
		return RuleScopeWider, true, nil
	case k.Veto != nil || replaces && cur.Veto != nil:
		return RuleVeto, true, nil
	}
	room, err := p.all.Room(k.ID, k.Version)
	switch {
	case err != nil:
		return "", false, err
	case room.Chars > 0:
		return RuleRoom, true, nil
	case p.drifted(run, place):
		return RuleDrift, true, nil
	case p.ints.Paused(p.kindOf(k), *k.Run):
		return RulePaused, true, nil
	}
	return "", false, nil
}

// The kind of change a waiting candidate makes
func (p *pass) kindOf(k knowledge.Knowledge) ChangeKind {
	if _, ok := p.all.Approved().Find(k.ID); ok {
		return ChangeUpdate
	}
	return ChangeAdd
}

// Whether a person decided the version last under a name of their own
func (p *pass) personDecided(id string, version int) bool {
	k, err := p.all.Version(id, version)
	return err == nil && k.Approver != "" && k.Approver != knowledge.ApproverNodloop
}

// Whether the versions nodloop approved in the place of the run since the anchor version reach the lessons a person approved there
// So the changes carried out never outweigh the person's own and a reanchor question comes first
func (p *pass) drifted(run trace.Trace, place AnchorPlace) bool {
	if run.ID == "" {
		return false
	}
	carried := p.all.ApprovedBy(knowledge.ApproverNodloop, p.s.Since, run.Producer, run.Labels)
	return len(carried) > 0 && len(carried) >= len(place.Items)
}

// The tier, the rule and the action of a change the judge found the outcome on
// 1. only a unanimous conform stays in tier 1 and every other outcome asks the person
// 2. a conform of a kind or a bin not yet open asks the person too so the answer feeds the bound that may open it
// 3. tier 1 decides and records in shadow and carries out nothing until the person ends the shadow
func (k Knobs) tier(kind ChangeKind, outcome Outcome, bin Bin) (int, RuleCode, Action) {
	switch {
	case outcome == OutcomeAgainst:
		return topTier, RuleJudgeAgainst, ActionAsk
	case outcome == OutcomeUndecided:
		return topTier, RuleJudgeUndecided, ActionAsk
	case outcome != OutcomeConform:
		return topTier, RuleJudgeSplit, ActionAsk
	case !k.Opens(Opening{Kind: kind, Bin: bin}):
		return topTier, RuleKindClosed, ActionAsk
	case !k.Activated:
		return 1, "", ActionShadow
	}
	return 1, "", ActionApply
}

// How a withdrawal on a signal that names a version nodloop approved is decided
// It asks no judge since it only returns the place to what the person confirmed
// So it has no bin
func (k Knobs) withdrawal() Decision {
	switch {
	case !k.Opens(Opening{Kind: ChangeWithdraw}):
		return Decision{Tier: topTier, RuleCode: RuleKindClosed, Action: ActionAsk}
	case !k.Activated:
		return Decision{Tier: 1, Action: ActionShadow}
	}
	return Decision{Tier: 1, Action: ActionWithdraw, Reason: knowledge.ReasonConflict, Failure: FailureUnderexplored, Reopen: ReopenOtherSession}
}

// What the judge reads of the change in its place
// 1. the change as the lesson it adds or the one it replaces with its scope
// 2. the lessons a person approved that reach the place and the text of each file the place reads now
// A file that cannot be read now counts as absent and its lines decide nothing
// 3. the person's own words on the runs the version cites since a lesson drafted from a fresh correction is in no anchor line yet
// 4. every change nodloop carried out in the place since the anchor version
func (p *pass) caseOf(ctx context.Context, k knowledge.Knowledge, kind ChangeKind, run trace.Trace, place AnchorPlace) (Case, error) {
	change := fmt.Sprintf("add [%s] %s\nscope: %s", k.Kind, k.Content, k.Run)
	if cur, ok := p.all.Approved().Find(k.ID); ok && kind == ChangeUpdate {
		change = fmt.Sprintf("replace [%s] %s\nwith [%s] %s\nscope: %s", cur.Kind, cur.Content, k.Kind, k.Content, k.Run)
	}
	anchor := []string{}
	for _, item := range place.Items {
		anchor = append(anchor, fmt.Sprintf("[%s] %s", item.Kind, item.Content))
	}
	for _, f := range slices.Concat(p.s.Anchor.Designated(), place.Loaded) {
		if text, err := p.s.Read(f.Path); err == nil {
			anchor = append(anchor, fmt.Sprintf("file %s\n%s", f.Path, text))
		}
	}
	var verdicts feedback.Records
	for _, id := range k.Evidence.FeedbackTraceIDs {
		list, err := p.verdicts.List(ctx, record.FeedbackFilter{TraceID: id})
		if err != nil {
			return Case{}, err
		}
		verdicts = append(verdicts, feedback.RestoreAll(list)...)
	}
	return Case{Change: change, Anchor: anchor, Words: verdicts.Words(k.Evidence.FeedbackTraceIDs), Carried: p.carried(run)}, nil
}

// The changes nodloop carried out since the anchor version that reach the run in the order they were made
func (p *pass) carried(run trace.Trace) []string {
	out := []string{}
	for _, k := range slices.Backward(p.all) {
		if k.Approver != knowledge.ApproverNodloop || !k.Decided() || k.Time.Before(p.s.Since) || k.Run == nil || !k.Run.Admits(run.Producer, run.Labels) {
			continue
		}
		verb := "approved"
		if k.Status == knowledge.StatusRetired {
			verb = "retired"
		}
		out = append(out, fmt.Sprintf("%s [%s] %s", verb, k.Kind, k.Content))
	}
	return out
}
