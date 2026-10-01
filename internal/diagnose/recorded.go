package diagnose

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// What a diagnose trace says about the review it recorded
type Recorded struct {
	Mode          Mode
	ChangeContext evidence.Context
	// The metrics the observations moved
	// The measured metrics for a record written before moved metrics were recorded since they hold every moved one
	Moved []string
	// The knowledge that reached the model
	Knowledge []AppliedKnowledge
	Diagnosis Diagnosis
	// The citation gate turned the review into a hold
	Forced bool
}

// Reads a recorded review so readers of the records never decode the trace keys themselves
// Fails with ErrMalformed for
// 1. another trace name
// 2. an input or output that does not decode
// 3. an unknown status
func ReadRecorded(tr trace.Trace) (Recorded, error) {
	if tr.Name != trace.NameDiagnose {
		return Recorded{}, fmt.Errorf("%w: %s is a %s trace", ErrMalformed, tr.ID, tr.Name)
	}
	var in recordInput
	if err := json.Unmarshal(tr.Input, &in); err != nil {
		return Recorded{}, fmt.Errorf("%w: review %s input: %w", ErrMalformed, tr.ID, err)
	}
	var diag Diagnosis
	if err := json.Unmarshal(tr.Output, &diag); err != nil {
		return Recorded{}, fmt.Errorf("%w: review %s output: %w", ErrMalformed, tr.ID, err)
	}
	if !diag.Status.Valid() {
		return Recorded{}, fmt.Errorf("%w: review %s status %q", ErrMalformed, tr.ID, diag.Status)
	}
	if in.Moved == nil {
		in.Moved = in.Metrics
	}
	return Recorded{
		Mode: in.Mode, ChangeContext: in.ChangeContext, Moved: in.Moved, Knowledge: in.Knowledge, Diagnosis: diag,
		Forced: slices.Contains(tr.Tags, TagGateHold),
	}, nil
}

// Every paragraph id the causes and checks cite once each in first cited order
func (diag Diagnosis) Citations() []string {
	return diag.causeCitations().add(diag.Checks.Paragraphs()...)
}
