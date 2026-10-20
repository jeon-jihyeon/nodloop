package classify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Probability every answer of the endpoint reaches for it to decide alone
const threshold = 0.8

type Member struct {
	Name       string
	Classifier Classifier
}

// The HMAC of a text under the key of the record directory
// The composition root passes it so classify never reads the key
type Seal func(text string) string

type TraceStore interface {
	Append(ctx context.Context, tr record.Trace) error
}

// The endpoint of a point and the built in member it falls back to
// A point asks a plan as it would ask one classifier
type Plan struct {
	point    Point
	endpoint Member
	builtin  Member
	traces   TraceStore
	seal     Seal
	now      func() time.Time
}

func NewPlan(point Point, endpoint, builtin Member, traces TraceStore, seal Seal, now func() time.Time) *Plan {
	return &Plan{point: point, endpoint: endpoint, builtin: builtin, traces: traces, seal: seal, now: now}
}

// What one member answered
type Asked struct {
	Name    string  `json:"name"`
	Model   string  `json:"model,omitempty"`
	Effort  string  `json:"effort,omitempty"`
	Answers Answers `json:"answers,omitempty"`
	Error   string  `json:"error,omitempty"`
	// Milliseconds the member took so the record shows what the fallback cost
	MS int64 `json:"ms,omitempty"`
}

// The input of a classify trace
type TraceInput struct {
	// The text the members read at the critic point and its HMAC at every other point
	State string `json:"state"`
	// The description of the published schema says what it holds since its type is open
	Questions any `json:"questions" jsonschema:"the instructions by name at the critic point and the names alone at every other point"`
}

// The output of a classify trace with every member asked in order and the endpoint first
type TraceOutput struct {
	Members []Asked `json:"members"`
	// The answers that stand
	// Absent when the deciding member failed and on a panel since it combines none
	Answers Answers `json:"answers,omitempty"`
}

// Answers the request and records every member asked in one classify trace
// 1. the endpoint answers alone when every answer reaches the threshold
// 2. the built in member answers when the endpoint fails or is unsure
// 3. a failure of both members returns both errors
func (p *Plan) Classify(ctx context.Context, req Request) (Answers, error) {
	start := p.now()
	reply, first, err := p.endpoint.ask(ctx, req, p.now)
	all := []Asked{first}
	var model string
	if err != nil || !reply.Answers.Sure() {
		endpointErr := err
		var fallback Asked
		reply, fallback, err = p.builtin.ask(ctx, req, p.now)
		all, model = append(all, fallback), fallback.Model
		if err != nil {
			err = errors.Join(endpointErr, err)
		}
	}
	tr := p.point.trace(req, p.seal, TraceOutput{Members: all, Answers: reply.Answers}, start, p.now())
	tr.Model = model
	return reply.Answers, errors.Join(err, p.record(ctx, tr, err))
}

// The reply of the member with its answers checked against the questions and the record of what it answered
// A failed reply keeps the model and the effort the member named so the trace shows what failed
func (m Member) ask(ctx context.Context, req Request, now func() time.Time) (Reply, Asked, error) {
	start := now()
	reply, err := m.reply(ctx, req)
	rec := Asked{Name: m.Name, Model: reply.Model, Effort: reply.Effort, MS: now().Sub(start).Milliseconds()}
	if err == nil {
		err = reply.Answers.check(req.Questions)
	}
	if err != nil {
		rec.Error = err.Error()
		return Reply{Model: reply.Model, Effort: reply.Effort}, rec, fmt.Errorf("%s: %w", m.Name, err)
	}
	rec.Answers = reply.Answers
	return reply, rec, nil
}

// A classifier that names no model replies with its answers alone
func (m Member) reply(ctx context.Context, req Request) (Reply, error) {
	if r, ok := m.Classifier.(Replier); ok {
		return r.Reply(ctx, req)
	}
	answers, err := m.Classifier.Classify(ctx, req)
	return Reply{Answers: answers}, err
}

// Appends the trace of one request with the error of the deciding member
func (p *Plan) record(ctx context.Context, tr trace.Trace, failed error) error {
	if failed != nil {
		tr.Error = failed.Error()
	}
	return p.traces.Append(ctx, tr.Record())
}

// The input a classify trace of the point keeps
// 1. the critic keeps the state as text since it holds the refused draft no other record keeps
// 2. every other point keeps the HMAC of the state and the questions by name
// since the state holds the person's words or the anchor and a question may name a lesson by its content
func (pt Point) input(req Request, seal Seal) TraceInput {
	if pt == PointCritic {
		return TraceInput{State: req.State, Questions: req.Questions}
	}
	return TraceInput{State: seal(req.State), Questions: req.Questions.names()}
}

// The classify trace of one call of the point that started at start
// Strings and answers that passed their check always encode since a NaN never passes
func (pt Point) trace(req Request, seal Seal, output TraceOutput, start, now time.Time) trace.Trace {
	in, _ := json.Marshal(pt.input(req, seal))
	out, _ := json.Marshal(output)
	return trace.Trace{
		ID: trace.NewID(now), Name: trace.NameClassify, Subject: string(pt), Ref: req.Ref, Time: now,
		Input: in, Output: out, DurationMS: now.Sub(start).Milliseconds(),
	}
}
