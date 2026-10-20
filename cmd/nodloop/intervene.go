package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One intervention of the write side from the command line in the payload of MCP intervene and POST /v1/interventions
// 1. --payload reads the payload from a file or stdin and the action and the flags given win over it
// 2. with the terminal proof it is the person's judgment under config approver or person
// Otherwise it is an agent proposal the person is asked about at the next stop of a session over the same records
func runIntervene(
	args []string, getenv func(string) string, now func() time.Time, proof trace.Proof, stdin io.Reader, stdout, stderr io.Writer,
) int {
	fs := newFlagSet("intervene", stderr)
	var records recordFlags
	records.bind(fs)
	var r curate.Request
	var labels labelFlag
	payload := fs.String("payload", "", "a file that holds the payload as JSON, or - for stdin")
	fs.StringVar(&r.Target, "target", "", "what it acts on: id:version, a compaction, a question, a change kind, a pause, a knob, a producer or an intervention")
	choice := fs.String("choice", "", "keep, undo or scope for judge, or one of the choices of the question for answer")
	fs.Var(&labels, "label", "a label key=value of a scope choice, a pause, a reanchor or a digest, repeatable")
	fs.Func("value", "the value of a knob within its range", func(v string) error {
		f, err := strconv.ParseFloat(v, 64)
		r.Value = &f
		return err
	})
	action, err := parseID(fs, args)
	if err != nil {
		return parseFailed(err)
	}
	a, err := records.app(getenv, now)
	if err != nil {
		return fail(stderr, "intervene", err)
	}
	given := r
	if r, err = readPayload(*payload, stdin); err != nil {
		return fail(stderr, "intervene", err)
	}
	r.Action, r.Target, r.Choice = cmp.Or(trace.Action(action), r.Action), cmp.Or(given.Target, r.Target), cmp.Or(trace.Choice(*choice), r.Choice)
	r.Value = cmp.Or(given.Value, r.Value)
	if len(labels) > 0 {
		r.Labels = trace.Labels(labels)
	}
	if err := (interveneCommand{app: a, proof: proof, out: stdout}).run(context.Background(), r); err != nil {
		return fail(stderr, "intervene", err)
	}
	return 0
}

// The payload of a file or of stdin for -, and the zero payload without one
func readPayload(path string, stdin io.Reader) (curate.Request, error) {
	var r curate.Request
	var b []byte
	var err error
	switch path {
	case "":
		return r, nil
	case "-":
		b, err = io.ReadAll(stdin)
	default:
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return r, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("%w: %w", errPayload, err)
	}
	return r, nil
}

type interveneCommand struct {
	app app
	// The terminal proof of the process
	proof trace.Proof
	out   io.Writer
}

// Records the intervention and prints what it recorded
// A command of the person is its source whatever the proof, and a failed export prints the records before the error
func (c interveneCommand) run(ctx context.Context, r curate.Request) error {
	iv, err := c.app.intervener()
	if err != nil {
		return err
	}
	by := curate.Submitter{Proof: c.proof, Person: cmp.Or(c.app.cfg.approver, unnamedPerson), Source: trace.SourceCommand}
	res, err := iv.Intervene(ctx, r, by)
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return err
	}
	fmt.Fprintf(c.out, "intervention\t%s\t%s\t%s\t%s\n", res.Trace.ID, r.Action, by.Proof.Authority(), by.Proof)
	for _, w := range res.Wrote.Written() {
		fmt.Fprintf(c.out, "wrote\t%s\tv%d\t%s\n", w.ID, w.Version, w.Status)
	}
	switch {
	case res.Answer != "":
		fmt.Fprintf(c.out, "answer\t%s\n", res.Answer)
	case r.Action == trace.ActionDigest:
		fmt.Fprintf(c.out, "since\t%s\tpage the records of answer, intervention, decide, expose and knowledge from it\n", cmp.Or(res.Since, "-"))
	case by.Proof.Authority() == trace.AuthorityAgent:
		fmt.Fprintln(c.out, "proposal\trecorded as a proposal and nothing changed. "+
			"Only a person at an interactive terminal outside an agent session decides, so the person is asked about it")
	}
	return err
}

// The intervener over the stores of the record directory
// curate.json is at hand so a proposal is due at the next stop at once
func (a app) intervener() (*curate.Intervener, error) {
	ledger, err := a.ledger()
	if err != nil {
		return nil, err
	}
	traces, err := a.traces()
	if err != nil {
		return nil, err
	}
	compactor, err := a.compactor(ledger)
	if err != nil {
		return nil, err
	}
	dir, err := a.makeRecordDir()
	if err != nil {
		return nil, err
	}
	return curate.NewIntervener(ledger, traces, curate.InterveneSetting{
		Build: buildVersion(), Coverage: compactor.Coverage, Marks: curate.NewMarker(dir),
		Cursor: schema.CursorAt,
	}, a.now), nil
}
