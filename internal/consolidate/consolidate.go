// Package consolidate records groups of waiting candidates that may say the same lesson in other words
package consolidate

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type KnowledgeStore interface {
	All(ctx context.Context) (knowledge.Set, error)
}

type TraceStore interface {
	Append(ctx context.Context, tr record.Trace) error
}

// Asks the model which waiting candidates say the same and records the groups as traces
// The ledger is only read so a run hides, merges or approves nothing
type Consolidator struct {
	items  KnowledgeStore
	traces TraceStore
	client llm.Client
	model  string
	now    func() time.Time
}

func New(items KnowledgeStore, traces TraceStore, client llm.Client, model string, now func() time.Time) *Consolidator {
	return &Consolidator{items: items, traces: traces, client: client, model: model, now: now}
}

// Waiting candidates that may say the same lesson
type Group struct {
	Members []knowledge.Ref `json:"members"`
	Why     string          `json:"why"`
}

// The members as one line such as `a v1, b v2`
func (g Group) String() string {
	parts := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		parts = append(parts, fmt.Sprintf("%s v%d", m.ID, m.Version))
	}
	return strings.Join(parts, ", ")
}

// What one run found among the waiting candidates of one producer
// The output of its consolidate trace
type Result struct {
	Producer string  `json:"-"`
	Groups   []Group `json:"groups"`
	// Waiting candidates read
	Candidates int `json:"candidates"`
	// Candidates in a cluster that was sent
	Clustered int `json:"clustered"`
}

// Candidates one run may carry together in the Waiting order
type cluster knowledge.Set

// Records one consolidate trace per producer with a cluster of two candidates or more
// 1. the waiting candidates of every label count with compaction candidates left out
// 2. a failed call is kept in the trace and returned and the other clusters still run
// 3. the results come in producer order
func (c *Consolidator) Run(ctx context.Context) ([]Result, error) {
	all, err := c.items.All(ctx)
	if err != nil {
		return nil, err
	}
	byProducer := map[string][]cluster{}
	for _, cl := range all.AllWaiting().Clusters() {
		producer := cl[0].Run.Producer
		byProducer[producer] = append(byProducer[producer], cluster(cl))
	}
	out := []Result{}
	var failed error
	for _, producer := range slices.Sorted(maps.Keys(byProducer)) {
		clusters := byProducer[producer]
		if !slices.ContainsFunc(clusters, cluster.sent) {
			continue
		}
		res, err := c.consolidate(ctx, producer, clusters)
		out = append(out, res)
		failed = errors.Join(failed, err)
	}
	return out, failed
}

// A cluster of one has nothing to compare
func (cl cluster) sent() bool {
	return len(cl) > 1
}

// The trace input
type input struct {
	Clusters [][]knowledge.Ref `json:"clusters"`
}

// One call per cluster that is sent through one meter so the trace carries what every call cost
// The calls that failed are returned with the error of the append
func (c *Consolidator) consolidate(ctx context.Context, producer string, clusters []cluster) (Result, error) {
	start := c.now()
	meter := llm.NewMeter(c.client)
	res := Result{Producer: producer, Groups: []Group{}}
	in := input{Clusters: [][]knowledge.Ref{}}
	var calls, dropped []error
	for _, cl := range clusters {
		res.Candidates += len(cl)
		if !cl.sent() {
			continue
		}
		res.Clustered += len(cl)
		in.Clusters = append(in.Clusters, knowledge.Set(cl).Refs())
		groups, err := cl.ask(ctx, meter, c.model)
		if err != nil {
			calls = append(calls, err)
			continue
		}
		kept, refused := cl.check(groups)
		res.Groups = append(res.Groups, kept...)
		dropped = append(dropped, refused...)
	}
	failed := errors.Join(calls...)
	return res, errors.Join(failed, c.record(ctx, producer, in, res, errors.Join(failed, errors.Join(dropped...)), meter.Take(), start))
}

// The groups the model found in the cluster
// A call that fails names the first member so the trace error says which cluster it was
func (cl cluster) ask(ctx context.Context, client llm.Client, model string) ([]Group, error) {
	res, err := client.Complete(ctx, llm.Request{System: Rules, Prompt: cl.prompt(), Schema: json.RawMessage(Schema), Model: model})
	if err != nil {
		return nil, fmt.Errorf("cluster of %s: %w", cl[0].ID, err)
	}
	var answer struct {
		Groups []Group `json:"groups"`
	}
	if err := json.Unmarshal(res.Output, &answer); err != nil {
		return nil, fmt.Errorf("cluster of %s: %w: %w", cl[0].ID, ErrAnswerInvalid, err)
	}
	return answer.Groups, nil
}

// The groups that pass the code checks and an error naming each one dropped
// A candidate a kept group holds is taken so a later group that names it again is dropped
func (cl cluster) check(groups []Group) ([]Group, []error) {
	kept := []Group{}
	var dropped []error
	taken := map[knowledge.Ref]bool{}
	for _, g := range groups {
		if err := cl.checkGroup(g, taken); err != nil {
			dropped = append(dropped, fmt.Errorf("group %s: %w", g, err))
			continue
		}
		for _, m := range g.Members {
			taken[m] = true
		}
		kept = append(kept, g)
	}
	return kept, dropped
}

// 1. every member is a candidate of the cluster at the version sent
// 2. no member is taken or named twice
// 3. two members or more
// 4. one kind
func (cl cluster) checkGroup(g Group, taken map[knowledge.Ref]bool) error {
	var kind knowledge.Kind
	named := map[knowledge.Ref]bool{}
	for _, m := range g.Members {
		i := slices.IndexFunc(cl, func(k knowledge.Knowledge) bool { return k.ID == m.ID && k.Version == m.Version })
		switch {
		case i < 0:
			return fmt.Errorf("%w: %s v%d", ErrMemberUnknown, m.ID, m.Version)
		case taken[m] || named[m]:
			return fmt.Errorf("%w: %s v%d", ErrMemberRepeated, m.ID, m.Version)
		case kind != "" && cl[i].Kind != kind:
			return fmt.Errorf("%w: %s and %s", ErrKindsDiffer, kind, cl[i].Kind)
		}
		named[m], kind = true, cl[i].Kind
	}
	if len(named) < 2 {
		return ErrGroupSmall
	}
	return nil
}

// Appends the consolidate trace of the producer
func (c *Consolidator) record(ctx context.Context, producer string, in input, res Result, failed error, spent llm.Response, start time.Time) error {
	input, err := json.Marshal(in)
	if err != nil {
		return err
	}
	output, err := json.Marshal(res)
	if err != nil {
		return err
	}
	now := c.now()
	tr := trace.Trace{
		ID: trace.NewID(now), Name: trace.NameConsolidate, Subject: producer, Time: now, Model: cmp.Or(spent.Model, c.model),
		Input: input, Output: output, Usage: spent.Usage(), DurationMS: now.Sub(start).Milliseconds(),
	}
	if failed != nil {
		tr.Error = failed.Error()
	}
	return c.traces.Append(ctx, tr.Record())
}
