package nodloop

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
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
	Kind       = knowledge.Kind
	Status     = knowledge.Status
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

	KindJudgment = knowledge.KindJudgment // what to do or not do
	KindMeaning  = knowledge.KindMeaning  // how to read something in this place
)

// One output of a producer to record
type Run struct {
	Producer string
	Labels   Labels
	// What the run was about in a few words
	Subject string
	// Groups the runs of one conversation or job
	SessionID string
	// The output as text or JSON
	// Keys, tokens and passwords in it are redacted before it is stored
	Output []byte
	// The knowledge versions the run received, as Items returned them
	Applied []Ref
}

// Records the run and returns the id a verdict cites
func (c *Client) Record(ctx context.Context, r Run) (string, error) {
	applied := r.Applied
	if applied == nil {
		applied = []Ref{}
	}
	input, err := json.Marshal(struct {
		Applied []Ref `json:"applied"`
	}{applied})
	if err != nil {
		return "", err
	}
	tr, err := trace.NewRun(r.Producer, r.Subject, r.Labels, input, []byte(feedback.Redact(string(r.Output))), c.now())
	if err != nil {
		return "", err
	}
	tr.SessionID = r.SessionID
	if err := c.traces.Append(ctx, tr); err != nil {
		return "", err
	}
	return tr.ID, nil
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
	// Who judged
	// session marks a verdict a conversation inferred from the person's words
	Reviewer string
}

// Records the verdict on a run that exists
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
	return c.verdicts.Append(ctx, fb)
}

func (c *Client) checkRun(ctx context.Context, id string) error {
	tr, err := c.traces.Get(ctx, id)
	if err != nil {
		return err
	}
	return tr.CheckRun()
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
		runs, err := c.traces.List(ctx, trace.Filter{Name: trace.NameRun})
		if err != nil {
			return Item{}, err
		}
		if err := draft.Run.Recorded(runs.Vocabulary(draft.Run.Producer)); err != nil {
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
	verdicts, err := c.verdicts.List(ctx, feedback.Filter{TraceID: id})
	if err != nil {
		return knowledge.Knowledge{}, err
	}
	return draft.From(tr, verdicts)
}

// Approves a candidate version on behalf of the named person
// A failed export of approved.md or of the veto file keeps the approval and returns the item with the error
func (c *Client) Approve(ctx context.Context, id string, version int, approver string) (Item, error) {
	k, err := c.ledger.Approve(ctx, id, version, approver)
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return Item{}, err
	}
	return newItems(knowledge.Set{k})[0], err
}

// Retires a version on behalf of the named person
func (c *Client) Retire(ctx context.Context, id string, version int, approver string) (Item, error) {
	k, err := c.ledger.Retire(ctx, id, version, approver)
	if err != nil {
		return Item{}, err
	}
	return newItems(knowledge.Set{k})[0], nil
}
