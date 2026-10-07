package classify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Probability every answer of the endpoint reaches for it to decide alone
const threshold = 0.8

type Member struct {
	Name       string
	Classifier Classifier
}

type TraceStore interface {
	Append(ctx context.Context, tr trace.Trace) error
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
	answers, all, err := p.ask(ctx, req, p.endpoint)
	if err != nil || !answers.confident(threshold) {
		endpointErr := err
		var more []asked
		answers, more, err = p.ask(ctx, req, p.builtin)
		all = append(all, more...)
		if err != nil {
			err = errors.Join(endpointErr, err)
		}
	}
	return answers, errors.Join(err, p.record(ctx, req, all, answers, err, start))
}

// One member and its answers checked against the questions
func (p *Plan) ask(ctx context.Context, req Request, m Member) (Answers, []asked, error) {
	start := p.now()
	answers, err := m.Classifier.Classify(ctx, req)
	ms := p.now().Sub(start).Milliseconds()
	if err == nil {
		err = answers.check(req.Questions)
	}
	if err != nil {
		return nil, []asked{{Name: m.Name, Error: err.Error(), MS: ms}}, fmt.Errorf("%s: %w", m.Name, err)
	}
	return answers, []asked{{Name: m.Name, Answers: answers, MS: ms}}, nil
}

// The trace of one request
// The state is kept because it holds the draft the members judged and no other record keeps a refused draft
func (p *Plan) record(ctx context.Context, req Request, all []asked, answers Answers, failed error, start time.Time) error {
	input, err := json.Marshal(struct {
		State     string    `json:"state"`
		Questions Questions `json:"questions"`
	}{req.State, req.Questions})
	if err != nil {
		return err
	}
	output, err := json.Marshal(struct {
		Members []asked `json:"members"`
		Answers Answers `json:"answers,omitempty"`
	}{all, answers})
	if err != nil {
		return err
	}
	now := p.now()
	tr := trace.Trace{
		ID: trace.NewID(now), Name: trace.NameClassify, Subject: string(p.point), Ref: req.Ref, Time: now,
		Input: input, Output: output, DurationMS: now.Sub(start).Milliseconds(),
	}
	if failed != nil {
		tr.Error = failed.Error()
	}
	return p.traces.Append(ctx, tr)
}
