package classify

import (
	"context"
	"time"
)

// Every member of a point asked in order with no combined answer
// Disagreement between the members is the signal of the judge point so its caller combines the votes
type Panel struct {
	point   Point
	members []Member
	traces  TraceStore
	seal    Seal
	now     func() time.Time
}

func NewPanel(point Point, members []Member, traces TraceStore, seal Seal, now func() time.Time) *Panel {
	return &Panel{point: point, members: members, traces: traces, seal: seal, now: now}
}

// What one member of a panel answered
type Vote struct {
	Member string
	Reply
	// Why the member gave no answer
	Err error
}

// The votes of one panel call in the order of the members
type Ballot struct {
	// The classify trace that records the votes
	TraceID string
	Votes   []Vote
}

// Asks every member and records one trace with all of them
// A failing member stays in the ballot and the trace with its error and the others still answer
// The error is only that of the trace so a caller never reads a judgment that no record holds
func (p *Panel) Classify(ctx context.Context, req Request) (Ballot, error) {
	start := p.now()
	votes := make([]Vote, 0, len(p.members))
	all := make([]Asked, 0, len(p.members))
	for _, m := range p.members {
		reply, rec, err := m.ask(ctx, req, p.now)
		votes = append(votes, Vote{Member: m.Name, Reply: reply, Err: err})
		all = append(all, rec)
	}
	id, err := p.record(ctx, req, all, start)
	return Ballot{TraceID: id, Votes: votes}, err
}

// The trace of one call and its id with no top level answers since a panel combines none
func (p *Panel) record(ctx context.Context, req Request, all []Asked, start time.Time) (string, error) {
	tr := p.point.trace(req, p.seal, TraceOutput{Members: all}, start, p.now())
	if err := p.traces.Append(ctx, tr.Record()); err != nil {
		return "", err
	}
	return tr.ID, nil
}
