package analysis

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

// The version of the stdin and stdout envelopes a command analyzer reads and writes
// A public contract so a change of either envelope takes a new version
const ObservationVersion = 1

// How long a command analyzer may run when its spec names no timeout
const commandTimeout = 10 * time.Second

// Starts a command analyzer and returns its stdout
// The caller owns the process so analysis never starts one itself
type Runner interface {
	Run(argv []string, stdin []byte, timeout time.Duration) ([]byte, error)
}

// What a command analyzer reads on stdin
type commandInput struct {
	Version       int              `json:"observation_version"`
	EventID       string           `json:"event_id"`
	ChangeContext evidence.Context `json:"change_context"`
	Points        []commandPoint   `json:"points"`
}

type commandPoint struct {
	Time   time.Time         `json:"time"`
	Metric string            `json:"metric"`
	Value  float64           `json:"value"`
	Dims   map[string]string `json:"dims,omitempty"`
}

// What a command analyzer writes on stdout
type commandOutput struct {
	Version      *int                 `json:"observation_version"`
	Observations []commandObservation `json:"observations"`
}

// One observation as a command reports it
// Pointers tell a missing number from a zero so a forgotten one fails instead of reading 0
type commandObservation struct {
	Target   map[string]string `json:"target,omitempty"`
	Metric   *string           `json:"metric"`
	Window   Window            `json:"window"`
	Current  *float64          `json:"current"`
	Baseline *float64          `json:"baseline"`
	Change   *float64          `json:"change"`
	Severity *float64          `json:"severity"`
	Adequate *bool             `json:"adequate"`
	Summary  *string           `json:"summary"`
	Samples  int               `json:"samples,omitempty"`
	Missing  int               `json:"missing,omitempty"`
}

// The observations the command of spec reports for the event
// Any failure becomes one inadequate observation that names the analyzer so the review holds instead of the run failing
func (p Policy) command(spec RuleSpec, ev evidence.Event) []Observation {
	obs, err := p.runCommand(spec, ev)
	if err == nil {
		return obs
	}
	return []Observation{{
		Rule: RuleCommand, Window: series(ev.Points).window(), Ref: series(ev.Points).ref(ev.ID),
		Summary: fmt.Sprintf("command analyzer %s failed: %v", spec.Name, err),
	}}
}

func (p Policy) runCommand(spec RuleSpec, ev evidence.Event) ([]Observation, error) {
	in := commandInput{Version: ObservationVersion, EventID: ev.ID, ChangeContext: ev.ChangeContext, Points: []commandPoint{}}
	for _, pt := range ev.Points {
		in.Points = append(in.Points, commandPoint{Time: pt.Time, Metric: pt.Metric, Value: pt.Value, Dims: pt.Dims})
	}
	// Times and strings and finite values always encode
	stdin, _ := json.Marshal(in)
	stdout, err := p.run.Run(spec.Command, stdin, cmp.Or(spec.Timeout, commandTimeout))
	if err != nil {
		return nil, err
	}
	var out commandOutput
	dec := json.NewDecoder(bytes.NewReader(stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCommandOutput, err)
	}
	if out.Version == nil || *out.Version != ObservationVersion {
		return nil, fmt.Errorf("%w: observation_version must be %d", ErrCommandOutput, ObservationVersion)
	}
	obs := make([]Observation, 0, len(out.Observations))
	for i, o := range out.Observations {
		ob, err := o.observation(spec.Name, series(ev.Points).ref(ev.ID))
		if err != nil {
			return nil, fmt.Errorf("observations[%d]: %w", i, err)
		}
		obs = append(obs, ob)
	}
	return obs, nil
}

// The observation with the analyzer name leading its summary
// Fails naming the first required key the command left out or a severity outside 0 to 1
func (o commandObservation) observation(name string, ref Ref) (Observation, error) {
	required := []struct {
		key    string
		absent bool
	}{
		{"metric", o.Metric == nil}, {"current", o.Current == nil}, {"baseline", o.Baseline == nil}, {"change", o.Change == nil},
		{"severity", o.Severity == nil}, {"adequate", o.Adequate == nil}, {"summary", o.Summary == nil},
	}
	for _, r := range required {
		if r.absent {
			return Observation{}, fmt.Errorf("%w: %s is required", ErrCommandOutput, r.key)
		}
	}
	if *o.Severity < 0 || *o.Severity > 1 {
		return Observation{}, fmt.Errorf("%w: severity %v is outside 0 to 1", ErrCommandOutput, *o.Severity)
	}
	return Observation{
		Rule: RuleCommand, Target: o.Target, Metric: *o.Metric, Window: o.Window, Current: *o.Current, Baseline: *o.Baseline,
		Change: *o.Change, Severity: *o.Severity, Adequate: *o.Adequate, Detail: Detail{Samples: o.Samples, Missing: o.Missing},
		Ref: ref, Summary: name + ": " + *o.Summary,
	}, nil
}
