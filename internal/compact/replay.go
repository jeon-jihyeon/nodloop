package compact

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/evidence"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

const TagReplay = "replay"

type ReplayOptions struct {
	// Empty means every replay event
	Events []string
	// Zero means the pool default
	Parallel int
	// Empty means the llm default
	Model string
	// Nil means silent
	Log io.Writer
}

// The replay events of a compaction with their expected status
func (c *Compactor) Expectations(ctx context.Context, id string) ([]Expectation, error) {
	compaction, err := c.ledger.Compaction(ctx, id)
	if err != nil {
		return nil, err
	}
	reviews, err := c.evidence(ctx, compaction.Replaced)
	if err != nil {
		return nil, err
	}
	expected, _, err := c.expectations(ctx, reviews)
	return expected, err
}

// Reviews every replay event again through d and returns the result
// 1. d reads the preview of the compaction and writes the replay store so traces.jsonl never sees a replay
// 2. knowledge is selected by scope and no example is given so the new items alone must carry every correction
// 3. the session id is the compaction id and the tag is replay
// 4. an event outside the replay is refused before any review
func (c *Compactor) Replay(ctx context.Context, d *diagnose.Diagnoser, id string, opts ReplayOptions) (knowledge.Replay, error) {
	expected, err := c.Expectations(ctx, id)
	if err != nil {
		return knowledge.Replay{}, err
	}
	events := make([]string, 0, len(expected))
	for _, e := range expected {
		events = append(events, e.EventID)
	}
	for _, event := range opts.Events {
		if !slices.Contains(events, event) {
			return knowledge.Replay{}, fmt.Errorf("%w: %s", ErrEventOutsideReplay, event)
		}
	}
	var jobs []diagnose.Job
	for _, event := range events {
		if len(opts.Events) > 0 && !slices.Contains(opts.Events, event) {
			continue
		}
		jobs = append(jobs, diagnose.Job{EventID: event, Options: diagnose.BatchOptions{
			Knowledge: diagnose.KnowledgeSelected, Model: opts.Model,
			Session: diagnose.Session{ID: id, Tags: []string{TagReplay}},
		}})
	}
	if _, err := d.RunAll(ctx, jobs, opts.Parallel, opts.Log); err != nil {
		return knowledge.Replay{}, err
	}
	return c.Result(ctx, id)
}

// The newest replay review per event against the expectations as they read now
// A verdict that changed after the replay fails the event until it is replayed again
func (c *Compactor) Result(ctx context.Context, id string) (knowledge.Replay, error) {
	expected, err := c.Expectations(ctx, id)
	if err != nil {
		return knowledge.Replay{}, err
	}
	replays, err := c.replays.List(ctx, trace.Filter{Name: trace.NameDiagnose, SessionID: id})
	if err != nil {
		return knowledge.Replay{}, err
	}
	r := knowledge.Replay{Compaction: id, Events: make([]knowledge.ReplayEvent, 0, len(expected))}
	for _, e := range expected {
		event := knowledge.ReplayEvent{EventID: e.EventID, Expected: e.Expected}
		// A failed review keeps no status
		var review struct {
			Status evidence.Status `json:"status"`
		}
		if i := slices.IndexFunc(replays, trace.Filter{Subject: e.EventID}.Matches); i >= 0 {
			event.TraceID = replays[i].ID
			if replays[i].Error == "" && json.Unmarshal(replays[i].Output, &review) == nil {
				event.Got = review.Status
			}
		}
		r.Events = append(r.Events, event)
	}
	return r, nil
}
