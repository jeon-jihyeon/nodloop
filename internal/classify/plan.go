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
	now      func() time.Time
}

func NewPlan(point Point, endpoint, builtin Member, traces TraceStore, now func() time.Time) *Plan {
	return &Plan{point: point, endpoint: endpoint, builtin: builtin, traces: traces, now: now}
}

// What one member answered
type asked struct {
	Name    string  `json:"name"`
	Model   string  `json:"model,omitempty"`
	Effort  string  `json:"effort,omitempty"`
	Answers Answers `json:"answers,omitempty"`
	Error   string  `json:"error,omitempty"`
	// Milliseconds the member took so the record shows what the fallback cost
	MS int64 `json:"ms,omitempty"`
}

// Answers the request and records every member asked in one classify trace
// 1. the endpoint answers alone when every answer reaches the threshold
// 2. the built in member answers when the endpoint fails or is unsure
// 3. a failure of both members returns both errors
func (p *Plan) Classify(ctx context.Context, req Request) (Answers, error) {
	start := p.now()
	reply, first, err := p.endpoint.ask(ctx, req, p.now)
	all := []asked{first}
	if err != nil || !reply.Answers.Sure() {
		endpointErr := err
		var fallback asked
		reply, fallback, err = p.builtin.ask(ctx, req, p.now)
		all = append(all, fallback)
		if err != nil {
			err = errors.Join(endpointErr, err)
		}
	}
	return reply.Answers, errors.Join(err, p.record(ctx, req, all, reply.Answers, err, start))
}

// The reply of the member with its answers checked against the questions and the record of what it answered
// A failed reply keeps the model and the effort the member named so the trace shows what failed
func (m Member) ask(ctx context.Context, req Request, now func() time.Time) (Reply, asked, error) {
	start := now()
	reply, err := m.reply(ctx, req)
	rec := asked{Name: m.Name, Model: reply.Model, Effort: reply.Effort, MS: now().Sub(start).Milliseconds()}
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

// The trace of one request
// The state is kept because it holds the draft the members judged and no other record keeps a refused draft
func (p *Plan) record(ctx context.Context, req Request, all []asked, answers Answers, failed error, start time.Time) error {
	tr, err := p.point.trace(req.Ref, struct {
		State     string    `json:"state"`
		Questions Questions `json:"questions"`
	}{req.State, req.Questions}, struct {
		Members []asked `json:"members"`
		Answers Answers `json:"answers,omitempty"`
	}{all, answers}, start, p.now())
	if err != nil {
		return err
	}
	if failed != nil {
		tr.Error = failed.Error()
	}
	return p.traces.Append(ctx, tr.Record())
}

// The classify trace of one call of the point that started at start
func (pt Point) trace(ref string, input, output any, start, now time.Time) (trace.Trace, error) {
	in, err := json.Marshal(input)
	if err != nil {
		return trace.Trace{}, err
	}
	out, err := json.Marshal(output)
	if err != nil {
		return trace.Trace{}, err
	}
	return trace.Trace{
		ID: trace.NewID(now), Name: trace.NameClassify, Subject: string(pt), Ref: ref, Time: now,
		Input: in, Output: out, DurationMS: now.Sub(start).Milliseconds(),
	}, nil
}
