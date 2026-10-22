package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
	"github.com/jeon-jihyeon/nodloop/internal/userconfig"
)

func runDelegation(args []string, getenv func(string) string, proof trace.Proof, stdout, stderr io.Writer) int {
	action := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	fs := newFlagSet("config delegation", stderr)
	var records recordFlags
	records.bind(fs)
	producer := fs.String("producer", "", "the producer whose new answer preferences may be delegated")
	everywhere := fs.Bool("everywhere", false, "explicitly include every place of this producer")
	var labels labelFlag
	fs.Var(&labels, "label", "one recorded scope label key=value, repeatable")
	if err := fs.Parse(args); err != nil {
		return parseFailed(err)
	}
	if fs.NArg() != 0 {
		return fail(stderr, "config delegation", fmt.Errorf("%w: unexpected argument %q", errUnknownAction, fs.Arg(0)))
	}
	a, err := records.app(getenv, time.Now)
	if err == nil {
		err = (delegationCommand{app: a, proof: proof, out: stdout}).run(context.Background(), action,
			userconfig.Delegation{Producer: *producer, Labels: labels, Everywhere: *everywhere})
	}
	if err != nil {
		return fail(stderr, "config delegation", err)
	}
	return 0
}

type delegationCommand struct {
	app   app
	proof trace.Proof
	out   io.Writer
}

func (c delegationCommand) run(ctx context.Context, action string, d userconfig.Delegation) error {
	if action == "" {
		return json.NewEncoder(c.out).Encode(append([]userconfig.Delegation{}, c.app.cfg.curation.Delegations...))
	}
	if c.proof != trace.ProofTerminal {
		return errPersonOnly
	}
	if action != "add" && action != "remove" {
		return fmt.Errorf("%w %q", errUnknownAction, action)
	}
	scope, err := delegationScope(d)
	if err != nil {
		return err
	}
	d.Labels = scope.Labels
	if action == "add" {
		if err := c.app.recorded(ctx, scope); err != nil {
			return err
		}
	}
	return c.save(action, d)
}

// The grants of config.json as a pass reads them
// 1. a grant without its terminal issued id or with a malformed scope fails the pass rather than grant anything
// 2. the recorded labels are checked only when the person adds a grant since retention may prune the runs that recorded them
func (c config) delegations() ([]curate.Delegation, error) {
	var grants []curate.Delegation
	for _, d := range c.curation.Delegations {
		if d.ID == "" {
			return nil, fmt.Errorf("%w: a delegation needs its terminal-issued grant", errRequired)
		}
		scope, err := delegationScope(d)
		if err != nil {
			return nil, err
		}
		grants = append(grants, curate.Delegation{ID: d.ID, Scope: scope})
	}
	return grants, nil
}

func (c delegationCommand) save(action string, d userconfig.Delegation) error {
	if c.app.cfg.home == "" {
		return errHomeUnknown
	}
	if action == "add" {
		d.ID = trace.NewID(c.app.now())
	}
	err := c.app.cfg.home.config().Update(func(read func(string, any) error) (map[string]any, error) {
		var saved userconfig.Curation
		if err := read("curation", &saved); err != nil {
			return nil, err
		}
		i := slices.IndexFunc(saved.Delegations, func(existing userconfig.Delegation) bool {
			return existing.Producer == d.Producer && existing.Everywhere == d.Everywhere && reflect.DeepEqual(existing.Labels, d.Labels)
		})
		switch {
		case action == "add" && i < 0:
			saved.Delegations = append(saved.Delegations, d)
		case action == "add":
			saved.Delegations[i] = d
		case action == "remove" && i >= 0:
			saved.Delegations = slices.Delete(saved.Delegations, i, i+1)
		case action == "remove":
			return nil, fmt.Errorf("%w: no saved delegation matches this scope", errRequired)
		}
		return map[string]any{"curation": saved}, nil
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(c.out).Encode(d)
}

// The scope of a grant with its labels normalized
// A grant names a producer and either labels or the explicit everywhere so no empty scope widens by accident
// Host and correlation name one machine or one conversation and never a place
func delegationScope(d userconfig.Delegation) (knowledge.RunScope, error) {
	if d.Producer == "" || len(d.Labels) == 0 != d.Everywhere {
		return knowledge.RunScope{}, fmt.Errorf("%w: name a producer and choose recorded labels or explicit everywhere", errRequired)
	}
	labels, err := trace.Labels(d.Labels).Normalized()
	if err != nil {
		return knowledge.RunScope{}, err
	}
	for key := range labels {
		if key == trace.LabelHost || key == trace.LabelCorrelation {
			return knowledge.RunScope{}, fmt.Errorf("%w: %s is not a delegation scope", errLabelFlag, key)
		}
	}
	return knowledge.RunScope{Producer: d.Producer, Labels: labels}, nil
}

// Whether a run of the producer recorded every label of the scope
func (a app) recorded(ctx context.Context, scope knowledge.RunScope) error {
	store, err := a.traces()
	if err != nil {
		return err
	}
	found, err := store.List(ctx, record.TraceFilter{Name: string(trace.NameRun), WithoutOutput: true})
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(found, func(run record.Trace) bool { return run.Producer == scope.Producer }) {
		return fmt.Errorf("%w: no recorded run names producer %q", errRequired, scope.Producer)
	}
	return scope.Recorded(trace.RestoreAll(found).Vocabulary(scope.Producer))
}
