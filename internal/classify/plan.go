package classify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type Member struct {
	Name       string
	Classifier Classifier
}

type TraceStore interface {
	Append(ctx context.Context, tr trace.Trace) error
}

// The members of a point answering as its setup says
// A point asks a plan as it would ask one classifier so it never knows the mode
type Plan struct {
	point   Point
	setup   Setup
	members []Member
	traces  TraceStore
	now     func() time.Time
}

func NewPlan(point Point, setup Setup, members []Member, traces TraceStore, now func() time.Time) *Plan {
	return &Plan{point: point, setup: setup, members: members, traces: traces, now: now}
}

// What one member answered
type asked struct {
	Name    string  `json:"name"`
	Answers Answers `json:"answers,omitempty"`
	Error   string  `json:"error,omitempty"`
	// Milliseconds the member took so a cascade shows what each step cost
	MS int64 `json:"ms,omitempty"`
}

// Answers the request through the members and records every member asked in one classify trace
func (p *Plan) Classify(ctx context.Context, req Request) (Answers, error) {
	start := p.now()
	var answers Answers
	var err error
	var all asks
	switch p.setup.Mode {
	case ModeCascade:
		answers, all, err = p.cascade(ctx, req)
	case ModeParallel:
		answers, all, err = p.parallel(ctx, req)
	default:
		answers, all, err = p.ask(ctx, req, p.members[0])
	}
	return answers, errors.Join(err, p.record(ctx, req, all, answers, err, start))
}

// One member and its answers checked against the questions
func (p *Plan) ask(ctx context.Context, req Request, m Member) (Answers, asks, error) {
	start := p.now()
	answers, err := m.Classifier.Classify(ctx, req)
	ms := p.now().Sub(start).Milliseconds()
	if err == nil {
		err = answers.check(req.Questions)
	}
	if err != nil {
		return nil, asks{{Name: m.Name, Error: err.Error(), MS: ms}}, fmt.Errorf("%s: %w", m.Name, err)
	}
	return answers, asks{{Name: m.Name, Answers: answers, MS: ms}}, nil
}

// The first member that answers every question at the threshold
// The last member answers when none does
// A member that fails passes the request on like one below the threshold
func (p *Plan) cascade(ctx context.Context, req Request) (Answers, asks, error) {
	var all asks
	for i, m := range p.members {
		answers, one, err := p.ask(ctx, req, m)
		all = append(all, one...)
		last := i == len(p.members)-1
		if last || (err == nil && answers.confident(p.setup.Threshold)) {
			return answers, all, err
		}
	}
	return nil, all, nil
}

// Every member answers at once and the answers combine per question
// 1. each member writes its own slot so the record keeps the order of the setup
// 2. a failing member fails the plan because a combine of fewer members would not be the one the user set up
func (p *Plan) parallel(ctx context.Context, req Request) (Answers, asks, error) {
	slots := make([]asks, len(p.members))
	errs := make([]error, len(p.members))
	var wg sync.WaitGroup
	for i, m := range p.members {
		wg.Go(func() { _, slots[i], errs[i] = p.ask(ctx, req, m) })
	}
	wg.Wait()
	all := slices.Concat(slots...)
	if err := errors.Join(errs...); err != nil {
		return nil, all, err
	}
	return all.combine(req.Questions, p.setup.Combine), all, nil
}

// The trace of one request
// The state is kept because it holds the draft the members judged and no other record keeps a refused draft
func (p *Plan) record(ctx context.Context, req Request, all asks, answers Answers, failed error, start time.Time) error {
	input, err := json.Marshal(struct {
		State     string    `json:"state"`
		Questions Questions `json:"questions"`
		Setup     Setup     `json:"setup"`
	}{req.State, req.Questions, p.setup})
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

// The members asked in order
type asks []asked

// all keeps the lowest yes of the members and any the highest
// The reasons of the members that gave one are kept with their names
func (as asks) combine(qs Questions, c Combine) Answers {
	answers := Answers{}
	for _, name := range qs.names() {
		var combined Answer
		var reasons []string
		for i, a := range as {
			yes := a.Answers[name].Yes
			switch {
			case i == 0, c == CombineAll && yes < combined.Yes, c == CombineAny && yes > combined.Yes:
				combined.Yes = yes
			}
			if r := a.Answers[name].Reason; r != "" {
				reasons = append(reasons, a.Name+": "+r)
			}
		}
		combined.Reason = strings.Join(reasons, "; ")
		answers[name] = combined
	}
	return answers
}
