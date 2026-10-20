package nodloop

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The situation of a run as label keys with their values such as tenant or task
type Labels = trace.Labels

// One knowledge version
type Ref = knowledge.Ref

// A tool call a judgment forbids: the tool, the conditions on its input and an example input it must block
type VetoRule = knowledge.Veto

// One condition of a veto rule on a field of the tool input
type VetoCondition = knowledge.VetoCondition

type (
	Verdict    = feedback.Verdict
	ReasonCode = feedback.ReasonCode
	Result     = feedback.Result
	Kind       = knowledge.Kind
	Status     = knowledge.Status
	// What an intervention does as against the Action of a vetted tool call
	InterventionAction = trace.Action
	Choice             = trace.Choice
	Authority          = trace.Authority
	Proof              = trace.Proof
)

const (
	VerdictApprove  = feedback.VerdictApprove  // the output was right
	VerdictEdit     = feedback.VerdictEdit     // the output with the corrected output in full
	VerdictReject   = feedback.VerdictReject   // the output was wrong and the reason says what
	VerdictWithdraw = feedback.VerdictWithdraw // takes back the verdict before it so the run has none

	ReasonFact     = feedback.ReasonFact     // something it stated was wrong
	ReasonApproach = feedback.ReasonApproach // the way it worked was wrong
	ReasonScope    = feedback.ReasonScope    // it did too much or too little
	ReasonForm     = feedback.ReasonForm     // the content was right and the shape wrong
	ReasonOther    = feedback.ReasonOther    // none of the others

	ResultConfirmed    = feedback.ResultConfirmed    // the check held the output
	ResultRefuted      = feedback.ResultRefuted      // the check showed the output wrong
	ResultInconclusive = feedback.ResultInconclusive // the check settled nothing

	KindJudgment = knowledge.KindJudgment // what to do or not do
	KindMeaning  = knowledge.KindMeaning  // how to read something in this place

	InterventionJudge = trace.ActionJudge // keep, undo or scope one knowledge version

	ChoiceKeep  = trace.ChoiceKeep  // approve a candidate or reaffirm an approved version
	ChoiceUndo  = trace.ChoiceUndo  // retire a version
	ChoiceScope = trace.ChoiceScope // approve a candidate in the labels picked

	AuthorityPerson = trace.AuthorityPerson // a proof shows the person made the call so it decided
	AuthorityAgent  = trace.AuthorityAgent  // no proof so the call is a proposal the person is asked about

	ProofKey         = trace.ProofKey         // a nodloop-server key of role approver added with --person
	ProofTerminal    = trace.ProofTerminal    // a CLI call from an interactive terminal outside an agent session
	ProofElicitation = trace.ProofElicitation // an MCP form the person accepted
	ProofReply       = trace.ProofReply       // the person's own click or reply word
	ProofNone        = trace.ProofNone        // any other call such as every call of this package
)

// One output of a producer to record
type Run struct {
	Producer string
	Labels   Labels
	// What the run was about in a few words
	Subject string
	// Groups the runs of one conversation or job
	SessionID string
	// The one model the producer runs and empty when it may switch models
	// The OTLP export names it as the requested model of the span
	Model string
	// The output as text or JSON
	// Keys, tokens and passwords in it are redacted before it is stored
	Output []byte
	// The knowledge versions the run received as Items or Draw returned them
	Applied []Ref
	// The knowledge versions a holdout kept from the run as Draw returned them
	// Never together with Applied
	Withheld []Ref
	// The key the run passed to Draw
	// Record draws on it again and marks the run drawn so report effect counts it in its arm
	// A run recorded without one counts in neither arm
	HoldoutKey string
	// How long the producer took for the run up to Record
	// The OTLP export starts its span that long before the record and zero leaves a span of no length
	Duration time.Duration
}

// Records the run and returns the id a verdict cites
// 1. with WithRetention a due daily prune starts in a goroutine so Record never waits for a rewrite of the records
// 2. its failure goes to WithPruneError since the run is recorded
// 3. the prune keeps running when ctx ends and WaitPrune waits for it
func (c *Client) Record(ctx context.Context, r Run) (string, error) {
	in, err := knowledge.NewRunInput(r.Applied, r.Withheld, "")
	if err != nil {
		return "", err
	}
	input, err := json.Marshal(c.holdout.Drawn(in, r.Producer, r.HoldoutKey))
	if err != nil {
		return "", err
	}
	tr, err := trace.NewRun(r.Producer, r.Subject, r.Labels, input, []byte(feedback.Redact(string(r.Output))), c.now())
	if err != nil {
		return "", err
	}
	tr.SessionID, tr.Model = r.SessionID, r.Model
	if tr, err = tr.Took(r.Duration); err != nil {
		return "", err
	}
	if err := c.traces.Append(ctx, tr.Record()); err != nil {
		return "", err
	}
	c.startPrune(context.WithoutCancel(ctx))
	return tr.ID, nil
}

// Starts the daily prune when the mark says one is due
// Most runs find the mark fresh so they start no goroutine
func (c *Client) startPrune(ctx context.Context) {
	if c.prune == nil {
		return
	}
	due, err := c.prune.Due(ctx)
	if err != nil || !due {
		c.reportPrune(err)
		return
	}
	c.pruning.Go(func() {
		_, err := c.prune.Run(ctx)
		c.reportPrune(err)
	})
}

func (c *Client) reportPrune(err error) {
	if err != nil && c.pruneFailed != nil {
		c.pruneFailed(err)
	}
}

// Waits for every daily prune Record started
// Call it before the process exits or the directory is removed
// A prune that stops halfway leaves the records whole and runs again a day later
func (c *Client) WaitPrune() {
	c.pruning.Wait()
}

// A person's verdict on a recorded run
type Judgment struct {
	Run     string
	Verdict Verdict
	// What the output got wrong for an edit or a reject
	ReasonCode ReasonCode
	// Why in the person's words
	Reason string
	// The corrected output in full for an edit
	// JSON is kept as JSON and any other text as a JSON string
	Edited []byte
	// Who judged, required so no verdict without a name reads as a person's
	// session marks a verdict a conversation inferred from the person's words
	// nodloop is reserved for the capture of the session
	Reviewer string
}

// Records the verdict on a run that exists
// 1. a judgment without a reviewer fails with ErrReviewerRequired
// 2. one under the reserved name nodloop fails with ErrReviewerReserved
// A call in process proves no person so the verdict carries proof none and counts apart from a proven person's
func (c *Client) Judge(ctx context.Context, j Judgment) error {
	if err := c.checkRun(ctx, j.Run); err != nil {
		return err
	}
	edited := json.RawMessage(j.Edited)
	if len(j.Edited) > 0 && !json.Valid(j.Edited) {
		var err error
		if edited, err = json.Marshal(string(j.Edited)); err != nil {
			return err
		}
	}
	fb, err := feedback.New(j.Run, j.Verdict, j.ReasonCode, j.Reason, edited, j.Reviewer, c.now())
	if err != nil {
		return err
	}
	return c.verdicts.Append(ctx, fb.WithProof(trace.ProofNone).Record())
}

// What a real check found for a recorded run apart from the verdict on its output
type Outcome struct {
	Run    string
	Result Result
	// The cause that held in the reviewer's words
	ConfirmedCause string
	// What the check showed beyond the result
	Note string
	// Who checked, required like the reviewer of a judgment
	Reviewer string
}

// Records the outcome of a run that exists
func (c *Client) Outcome(ctx context.Context, o Outcome) error {
	if err := c.checkRun(ctx, o.Run); err != nil {
		return err
	}
	out, err := feedback.NewOutcome(o.Run, o.Result, o.ConfirmedCause, o.Note, o.Reviewer, c.now())
	if err != nil {
		return err
	}
	return c.outcomes.Append(ctx, out.Record())
}

// One report of the records as JSON in the shape the MCP tool report and nodloop report --json answer
// 1. a name that names no report fails with ErrReportUnknown
// 2. the runs are read from the later of the WithRetention cutoff and the cutoff of the last prune of the directory
func (c *Client) Report(ctx context.Context, name string) (json.RawMessage, error) {
	last, err := c.marks.Last(ctx)
	if err != nil {
		return nil, err
	}
	stores := loop.Stores{
		Traces: c.traces, Verdicts: c.verdicts, Outcomes: c.outcomes, Items: c.ledger, Since: c.days.Window(c.now(), last.Cutoff),
	}
	return stores.Report(ctx, loop.ReportName(name), c.now())
}

func (c *Client) checkRun(ctx context.Context, id string) error {
	found, err := c.traces.Get(ctx, id)
	if err != nil {
		return err
	}
	return trace.Restore(found).CheckRun()
}

// The fields of one knowledge version a producer reads
type Item struct {
	ID      string
	Version int
	Kind    Kind
	Content string
	Status  Status
	// The producer and labels a run must carry to receive it
	Producer string
	Labels   Labels
}

// The version to pass in Applied for a run that received the item
func (i Item) Ref() Ref {
	return Ref{ID: i.ID, Version: i.Version}
}

// Items in the order a run receives them
// The most specific come first and among those the latest approved or reaffirmed
type Items []Item

// The versions to pass in Applied for a run that received the items
func (items Items) Refs() []Ref {
	out := make([]Ref, 0, len(items))
	for _, it := range items {
		out = append(out, it.Ref())
	}
	return out
}

func newItems(set knowledge.Set) Items {
	out := make(Items, 0, len(set))
	for _, k := range set {
		item := Item{ID: k.ID, Version: k.Version, Kind: k.Kind, Content: k.Content, Status: k.Status}
		if k.Run != nil {
			item.Producer, item.Labels = k.Run.Producer, k.Run.Labels
		}
		out = append(out, item)
	}
	return out
}

// The approved items a run of the producer with the labels receives
// Put them in the prompt of the run and pass their refs as Applied when recording it
func (c *Client) Items(ctx context.Context, producer string, labels Labels) (Items, error) {
	all, err := c.ledger.All(ctx)
	if err != nil {
		return nil, err
	}
	return newItems(all.For(producer, labels)), nil
}

// What one run receives under the holdout
// Exactly one of them holds the refs when the producer has items in the labels
// Pass the same key as Run HoldoutKey when recording the run
type Draw struct {
	// The items to put in the prompt and pass as Applied
	Items Items
	// The refs the holdout kept from the run to pass as Withheld
	Withheld []Ref
}

// The items a run of the producer with the labels receives unless the holdout withholds the key
// 1. the key is what the producer keeps for one run such as a window id and the draw hashes the producer with it
// 2. an empty key or no WithHoldout never withholds so Items equals what Items returns
func (c *Client) Draw(ctx context.Context, producer string, labels Labels, key string) (Draw, error) {
	all, err := c.ledger.All(ctx)
	if err != nil {
		return Draw{}, err
	}
	items := all.For(producer, labels)
	if !c.holdout.WithholdsRun(producer, key) {
		return Draw{Items: newItems(items)}, nil
	}
	return Draw{Items: Items{}, Withheld: items.Refs()}, nil
}

// The candidates a run of the producer with the labels would receive once a person approves them
func (c *Client) Waiting(ctx context.Context, producer string, labels Labels) (Items, error) {
	all, err := c.ledger.All(ctx)
	if err != nil {
		return nil, err
	}
	return newItems(all.Waiting(producer, labels)), nil
}

// A candidate to propose
type Proposal struct {
	Kind    Kind
	Content string
	// A corrected run whose producer, labels and feedback the candidate takes
	From string
	// Win over those of From
	Producer string
	Labels   Labels
	// Labels a run must not carry
	Except Labels
	// Recorded runs whose verdicts taught it
	// At least one is needed when From names none
	Runs []string
	// A tool call the judgment forbids
	// Once approved CheckCall blocks a call it matches
	Veto *VetoRule
	// Empty for a generated id
	ID string
	// Who proposed and author when empty
	Author string
	// Lets the scope name label values no recorded run carries yet such as a new tenant
	// The candidate keeps that it was allowed
	NewLabels bool
}

// Proposes a candidate
// 1. From must name a run whose latest verdict corrects it
// 2. every label value must be one a recorded run of the producer carries unless NewLabels allows new ones
func (c *Client) Propose(ctx context.Context, p Proposal) (Item, error) {
	for _, id := range p.Runs {
		if err := c.checkRun(ctx, id); err != nil {
			return Item{}, err
		}
	}
	draft := knowledge.Knowledge{
		ID: p.ID, Kind: p.Kind, Content: p.Content, Author: cmp.Or(p.Author, feedback.ReviewerAuthor), NewLabels: p.NewLabels, Veto: p.Veto,
		Run:      &knowledge.RunScope{Producer: p.Producer, Labels: p.Labels, Except: p.Except},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: p.Runs},
	}
	if p.From != "" {
		var err error
		if draft, err = c.fromRun(ctx, p.From, draft); err != nil {
			return Item{}, err
		}
	}
	if draft.Run.Producer == "" {
		return Item{}, fmt.Errorf("%w: a proposal names a producer or a run it comes from", knowledge.ErrScopeInvalid)
	}
	if !p.NewLabels {
		runs, err := c.traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun)})
		if err != nil {
			return Item{}, err
		}
		if err := draft.Run.Recorded(trace.RestoreAll(runs).Vocabulary(draft.Run.Producer)); err != nil {
			return Item{}, err
		}
	}
	k, _, err := c.ledger.Propose(ctx, draft)
	if err != nil {
		return Item{}, err
	}
	return newItems(knowledge.Set{k})[0], nil
}

// The draft with the producer, labels and evidence of a run a person corrected
func (c *Client) fromRun(ctx context.Context, id string, draft knowledge.Knowledge) (knowledge.Knowledge, error) {
	tr, err := c.traces.Get(ctx, id)
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	verdicts, err := c.verdicts.List(ctx, record.FeedbackFilter{TraceID: id})
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	return draft.From(trace.Restore(tr), feedback.RestoreAll(verdicts))
}

// Records the approval of a candidate version as an agent proposal and returns it
// 1. a call in process proves no person so the version stays as it is until the person decides it
// from a terminal or under a person key of nodloop-server
// 2. an empty name fails with ErrApproverRequired and the reserved name with ErrApproverReserved
// 3. an unknown version fails with ErrNotFound
func (c *Client) Approve(ctx context.Context, id string, version int, approver string) (Intervention, error) {
	return c.proposal(ctx, trace.Intervention{ID: id, Version: version, Choice: trace.ChoiceKeep}, approver)
}

// Records the approval of a candidate in the labels the person picked as an agent proposal and returns it
// 1. empty labels reach every run of its producer once the person decides it
// 2. every label value must be one a recorded run of the producer carries
func (c *Client) ApproveIn(ctx context.Context, id string, version int, labels Labels, approver string) (Intervention, error) {
	scope, err := c.ledger.ScopeIn(ctx, id, version, labels)
	if err != nil {
		return Intervention{}, err
	}
	runs, err := c.traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun)})
	if err != nil {
		return Intervention{}, err
	}
	if err := scope.Recorded(trace.RestoreAll(runs).Vocabulary(scope.Producer)); err != nil {
		return Intervention{}, err
	}
	return c.proposal(ctx, trace.Intervention{ID: id, Version: version, Choice: trace.ChoiceScope, Labels: labels}, approver)
}

// Records the retire of a version as an agent proposal and returns it
func (c *Client) Retire(ctx context.Context, id string, version int, approver string) (Intervention, error) {
	return c.proposal(ctx, trace.Intervention{ID: id, Version: version, Choice: trace.ChoiceUndo}, approver)
}

// One judgment on a knowledge version as the records keep it
// The package records every judgment as an agent proposal since a call in process proves no person
type Intervention struct {
	// The trace id of the intervention record
	ID     string
	Action InterventionAction
	// The version it acts on
	Ref    Ref
	Choice Choice
	// Where a scope choice approves the candidate
	Labels    Labels
	Authority Authority
	Proof     Proof
}

// The intervention of the target recorded once the name passes and the version is found
func (c *Client) proposal(ctx context.Context, target trace.Intervention, approver string) (Intervention, error) {
	if err := knowledge.CheckApprover(approver); err != nil {
		return Intervention{}, err
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return Intervention{}, err
	}
	if _, err := all.Version(target.ID, target.Version); err != nil {
		return Intervention{}, err
	}
	target.Action, target.Proof = trace.ActionJudge, trace.ProofNone
	tr, err := target.Trace(nil, "", c.now())
	if err != nil {
		return Intervention{}, err
	}
	if err := c.traces.Append(ctx, tr.Record()); err != nil {
		return Intervention{}, err
	}
	return Intervention{
		ID: tr.ID, Action: target.Action, Ref: Ref{ID: target.ID, Version: target.Version}, Choice: target.Choice, Labels: target.Labels,
		Authority: target.Proof.Authority(), Proof: target.Proof,
	}, nil
}
