package curate

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One grant of config.json a person issued at their terminal
type Delegation struct {
	ID    string
	Scope knowledge.RunScope
}

// The grants of the delegated versions a person retired in the order of their HMACs
// The ledger keeps every record so a later restore or keep never brings a grant back
func (p *pass) revoked() []string {
	grants := map[string]string{}
	for _, d := range p.decides {
		if d.Decision.Delegation != "" {
			grants[d.ID] = d.Decision.Delegation
		}
	}
	if len(grants) == 0 {
		return nil
	}
	carried, retired := map[knowledge.Ref]string{}, map[knowledge.Ref]bool{}
	for _, k := range p.all {
		ref := knowledge.Ref{ID: k.ID, Version: k.Version}
		if grant, ok := grants[k.Decision]; ok && k.Status == knowledge.StatusApproved {
			carried[ref] = grant
		}
		if k.Status == knowledge.StatusRetired && k.Reason == knowledge.ReasonPerson && k.Approver != knowledge.ApproverNodloop {
			retired[ref] = true
		}
	}
	var out []string
	for ref, grant := range carried {
		if retired[ref] && !slices.Contains(out, grant) {
			out = append(out, grant)
		}
	}
	slices.Sort(out)
	return out
}

// The first grant of the pass that covers the candidate with its HMAC and false for none
// 1. only the first version of a new id with an eligible preference is delegable
// 2. a grant on host or correlation labels names one machine or one conversation and covers nothing
// 3. the HMAC seals the policy, the grant id and the canonical scope so a stopped grant stays stopped until a new id
func (p *pass) delegation(k knowledge.Knowledge, t *Triage) (knowledge.RunScope, string, bool) {
	if t == nil || !t.eligible() || k.Run == nil || k.Version != 1 || k.Base != 0 {
		return knowledge.RunScope{}, "", false
	}
	for _, grant := range p.s.Delegations {
		limit := grant.Scope
		if grant.ID == "" || len(limit.Labels[trace.LabelHost]) > 0 || len(limit.Labels[trace.LabelCorrelation]) > 0 ||
			!k.Run.Within(limit) {
			continue
		}
		// A scope of strings and string maps always encodes
		b, _ := json.Marshal(limit)
		hash := p.seal(classify.TriagePolicy + ":" + grant.ID + ":" + string(b))
		if !slices.Contains(p.event.Knobs.DelegationPaused, hash) {
			return limit, hash, true
		}
	}
	return knowledge.RunScope{}, "", false
}

// Applies a delegated decide of this pass within the cap of its grant
// The ledger checks the candidate against the cap again under its lock
func (p *pass) delegated(ctx context.Context, d Decided) error {
	k, err := p.all.Version(d.Change.ID, d.Change.Version)
	if err != nil {
		return err
	}
	limit, hash, ok := p.delegation(k, d.Decision.Triage)
	if !ok || hash != d.Decision.Delegation {
		return fmt.Errorf("%w: delegation no longer covers the candidate", ErrDecideInvalid)
	}
	_, err = p.ledger.ApplyDelegated(ctx, k.ID, k.Version, d.ID, limit)
	return err
}

func (p *pass) anchorFiles(place AnchorPlace) bool {
	return len(place.Loaded) > 0 || len(p.s.Anchor.Designated()) > 0
}

// The decision triage alone settles before the code rules and the judge
// 1. a protected concern holds the candidate for the person with no judge call
// 2. a grant that covers a delegable candidate applies it unless a code rule or the judge holds it
// 3. anything else is left to the code rules and the judge
func (p *pass) triageDecision(ctx context.Context, k knowledge.Knowledge, place AnchorPlace, r *ruling) error {
	t, err := p.triaged(ctx, k, place)
	if err != nil {
		return err
	}
	r.decision = Decision{Tier: topTier, Action: ActionHold, Triage: t}
	if t.protected() {
		r.decision.RuleCode = RuleTriageProtected
		return nil
	}
	if _, hash, ok := p.delegation(k, t); ok {
		t.Grant = hash
		r.decision.Tier, r.decision.RuleCode, r.decision.Action, r.decision.Delegation = 1, RuleDelegated, ActionApply, hash
	}
	return nil
}

// Whether a grant the candidate was never checked under covers it now so the pass decides it once more
func (p *pass) newlyDelegated(k knowledge.Knowledge, d Decision) bool {
	_, hash, ok := p.delegation(k, d.Triage)
	return ok && hash != d.Triage.Grant
}
