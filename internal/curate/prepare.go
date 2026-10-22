package curate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A prepared question spends no exposure or retry round
// Only a proposal explicitly requested in this session interrupts its work
func (iv *Intervener) Prepare(ctx context.Context, a Asking) (Shown, error) {
	if len(a.Mark.Due) == 0 || a.Session == "" {
		return Shown{}, nil
	}
	s, err := iv.readOver(ctx, a.All)
	if err != nil {
		return Shown{}, err
	}
	a.Mark = a.Mark.inForce(s.knobs, iv.now())
	due, ok := s.proposalDue(s.open(a.Mark.Due), a.Session)
	if !ok {
		return Shown{}, nil
	}
	draw := trace.Draw{CycleID: a.Mark.CycleID, PolicyVersion: a.Mark.PolicyVersion}
	return iv.prepared(ctx, a, s, due, draw, trace.SourceManual)
}

// The capture calls this only for an AskUserQuestion call in the host transcript
// A changed question or a repeated call records no exposure
func (iv *Intervener) Observe(ctx context.Context, a Asking, r Reply, at time.Time) (Shown, error) {
	if at.IsZero() {
		return Shown{}, fmt.Errorf("%w: an observed question needs its host timestamp", ErrExposeInvalid)
	}
	release, err := iv.turn()
	if err != nil {
		return Shown{}, err
	}
	defer release()
	shown, err := iv.Prepare(ctx, a)
	if err != nil || shown.Question.Key == "" || shown.Question.Key != r.Key {
		return Shown{}, err
	}
	var x Exposure
	if json.Unmarshal(shown.Expose.Input, &x) != nil || !x.shows(r) {
		return Shown{}, nil
	}
	shown.Expose.Time = at
	if err := iv.traces.Append(ctx, shown.Expose.Record()); err != nil {
		return Shown{}, err
	}
	return shown, nil
}
