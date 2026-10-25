package nodloop

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The situation of a run as label keys with their values such as tenant or task
// correlation preserves an external conversation id without entering inferred knowledge scope
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
	Actor      = feedback.Actor
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

	ActorPerson  = feedback.ActorPerson  // a person's check
	ActorCode    = feedback.ActorCode    // a deterministic check
	ActorModel   = feedback.ActorModel   // a model's evaluation
	ActorUnknown = feedback.ActorUnknown // no evaluator recorded

	KindJudgment = knowledge.KindJudgment // what to do or not do
	KindMeaning  = knowledge.KindMeaning  // how to read something in this place

	InterventionJudge    = trace.ActionJudge    // keep, undo or scope one knowledge version
	InterventionAnswer   = trace.ActionAnswer   // one choice of an open question
	InterventionDismiss  = trace.ActionDismiss  // close an open question with no decision
	InterventionPause    = trace.ActionPause    // send every change of a kind or a scope to the person
	InterventionResume   = trace.ActionResume   // end a pause
	InterventionKnob     = trace.ActionKnob     // set one knob within its range
	InterventionReanchor = trace.ActionReanchor // take the versions nodloop approved in a place as the person's own
	InterventionDigest   = trace.ActionDigest   // the cursor since the person's last decision
	InterventionReverse  = trace.ActionReverse  // take back an earlier intervention

	ChoiceKeep     = trace.ChoiceKeep     // approve a candidate, reaffirm an approved version or restore a retired one
	ChoiceUndo     = trace.ChoiceUndo     // retire a version or reverse what curate carried out
	ChoiceScope    = trace.ChoiceScope    // approve a candidate in the labels picked
	ChoiceDrop     = trace.ChoiceDrop     // retire the candidate a question names
	ChoiceMerge    = trace.ChoiceMerge    // apply the prepared compaction and then the change
	ChoiceSeparate = trace.ChoiceSeparate // leave the items and the change as they are
	ChoiceMine     = trace.ChoiceMine     // take the versions nodloop approved in the place as the person's own
	ChoiceReview   = trace.ChoiceReview   // look at the digest instead
	ChoiceYes      = trace.ChoiceYes      // open the kind, end the shadow or carry out a proposal
	ChoiceNotYet   = trace.ChoiceNotYet   // keep it as it is
	ChoiceSame     = trace.ChoiceSame     // keep what curate carried out

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
	// What evaluated the check and empty when the caller does not say
	// A person actor proves no person so the outcome carries no proof
	Actor Actor
}

// Records the outcome of a run that exists
func (c *Client) Outcome(ctx context.Context, o Outcome) error {
	if err := c.checkRun(ctx, o.Run); err != nil {
		return err
	}
	out, err := feedback.NewOutcome(o.Run, o.Result, o.ConfirmedCause, o.Note, o.Reviewer, o.Actor, c.now())
	if err != nil {
		return err
	}
	return c.outcomes.Append(ctx, out.Record())
}

// One page asked of the raw records of one kind
// Kind is a kind of the published index such as run, verdict, knowledge or decide
// Since is the Next of the page before and empty for the first page
// Limit is 100 when 0 and cut to 1000
// Content keeps the free text of the kind such as outputs, reasons and lesson content
type Query = schema.Query

// One page of raw records of one kind in the order the records were appended in the published schema of the kind
// Pass Next back as Since for the following page
type Page = schema.Page

// One page of the raw records of a kind as the MCP tool records pages them
// 1. a record another process appends late with an earlier time still comes on a later page once
// 2. an unknown kind fails with ErrKindUnknown, a Since no page answered with ErrCursorInvalid and the guard log with ErrNotServed since the package reads one record directory
func (c *Client) Records(ctx context.Context, q Query) (Page, error) {
	return c.source.Page(ctx, q)
}

// One report of the records as JSON in the shape the MCP tool report and nodloop report --json answer
//
// Deprecated: Records carries the published contract so page the raw records instead
// Report is removed in the next release
//
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
	Producer   string
	Labels     Labels
	Except     Labels
	ValidFrom  time.Time
	ValidUntil time.Time
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
			item.Except, item.ValidFrom, item.ValidUntil = k.Run.Except, k.Run.ValidFrom, k.Run.ValidUntil
		}
		out = append(out, item)
	}
	return out
}

// The approved items a run of the producer with the labels receives
// Put them in the prompt of the run and pass their refs as Applied when recording it
func (c *Client) Items(ctx context.Context, producer string, labels Labels) (Items, error) {
	labels, err := labels.ForRun()
	if err != nil {
		return nil, err
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return nil, err
	}
	return newItems(all.ForAt(producer, labels, c.now())), nil
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
	labels, err := labels.ForRun()
	if err != nil {
		return Draw{}, err
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return Draw{}, err
	}
	items := all.ForAt(producer, labels, c.now())
	if !c.holdout.WithholdsRun(producer, key) {
		return Draw{Items: newItems(items)}, nil
	}
	return Draw{Items: Items{}, Withheld: items.Refs()}, nil
}

// The candidates a run of the producer with the labels would receive once a person approves them
func (c *Client) Waiting(ctx context.Context, producer string, labels Labels) (Items, error) {
	labels, err := labels.ForRun()
	if err != nil {
		return nil, err
	}
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
	Except     Labels
	ValidFrom  time.Time
	ValidUntil time.Time
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
	draft := knowledge.Knowledge{
		ID: p.ID, Kind: p.Kind, Content: p.Content, Author: cmp.Or(p.Author, knowledge.DefaultAuthor), NewLabels: p.NewLabels, Veto: p.Veto,
		Run: &knowledge.RunScope{Producer: p.Producer, Labels: p.Labels, Except: p.Except,
			ValidFrom: p.ValidFrom, ValidUntil: p.ValidUntil},
		Evidence: knowledge.Evidence{FeedbackTraceIDs: p.Runs},
	}
	for _, id := range p.Runs {
		found, err := c.traces.Get(ctx, id)
		if err != nil {
			return Item{}, err
		}
		run := trace.Restore(found)
		if err := run.CheckRun(); err != nil {
			return Item{}, err
		}
		draft.Evidence = draft.Evidence.Join(id, run.EvidenceUnit())
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
// 1. a call in process proves no person so the version stays as it is until the person decides it at a terminal or under a person key of nodloop-server
// 2. an empty name fails with ErrApproverRequired and the reserved name with ErrApproverReserved
// 3. an unknown version fails with ErrNotFound
func (c *Client) Approve(ctx context.Context, id string, version int, approver string) (Intervention, error) {
	return c.proposal(ctx, id, version, trace.ChoiceKeep, nil, approver)
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
	return c.proposal(ctx, id, version, trace.ChoiceScope, labels, approver)
}

// Records the retire of a version as an agent proposal and returns it
func (c *Client) Retire(ctx context.Context, id string, version int, approver string) (Intervention, error) {
	return c.proposal(ctx, id, version, trace.ChoiceUndo, nil, approver)
}

// One intervention of the write side as a caller submits it and as the records keep it
// The package records every intervention but a digest as an agent proposal since a call in process proves no person
type Intervention struct {
	// The trace id of the intervention record
	// Set on what Intervene answers
	ID     string
	Action InterventionAction
	// What it acts on as Target names it: an item version as id:version, a compaction, the decide of a question, the change kind of a pause,
	// the pause a resume ends, a knob, the producer of the place of a reanchor or a digest, or the intervention a reverse takes back
	Target string
	// The version it acts on, set on what Intervene answers when it names one
	Ref    Ref
	Choice Choice
	// The labels of a scope choice, a pause, a reanchor or a digest
	Labels Labels
	// The value of a knob
	Value *float64
	// The session a proposal is asked in
	Session string
	// The cursor a digest answers to pass as Query Since
	Since     string
	Authority Authority
	Proof     Proof
}

// Records any action of the write side as an agent proposal the person is asked about by the next curate pass over the directory
// 1. a digest needs no proof and answers the cursor since the person's last decision in Since
// 2. a target the records do not hold or a knob value outside its range fails and records nothing
func (c *Client) Intervene(ctx context.Context, in Intervention) (Intervention, error) {
	r := curate.Request{Action: in.Action, Target: in.Target, Choice: in.Choice, Labels: in.Labels, Value: in.Value, Session: in.Session}
	res, err := c.intervener.Intervene(ctx, r, curate.Submitter{Proof: trace.ProofNone, Source: trace.SourceManual})
	if err != nil {
		return Intervention{}, err
	}
	var rec trace.Intervention
	// A recorded intervention always decodes
	_ = json.Unmarshal(res.Trace.Input, &rec)
	in.ID, in.Ref, in.Since, in.Authority, in.Proof = res.Trace.ID, Ref{ID: rec.ID, Version: rec.Version}, res.Since, rec.Authority, rec.Proof
	return in, nil
}

// The judgment of one version recorded as an agent proposal once the name passes
func (c *Client) proposal(ctx context.Context, id string, version int, choice Choice, labels Labels, approver string) (Intervention, error) {
	if err := knowledge.CheckApprover(approver); err != nil {
		return Intervention{}, err
	}
	target := fmt.Sprintf("%s:%d", id, version)
	return c.Intervene(ctx, Intervention{Action: trace.ActionJudge, Target: target, Choice: choice, Labels: labels})
}
