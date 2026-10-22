package main

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
)

type capturedQuestion struct {
	question askQuestion
	at       time.Time
	offset   int64
}

func (calls askedQuestions) observed(at time.Time, offset int64) []capturedQuestion {
	var found []capturedQuestion
	for _, id := range slices.Sorted(maps.Keys(calls)) {
		for _, text := range slices.Sorted(maps.Keys(calls[id])) {
			q := calls[id][text]
			if strings.HasPrefix(q.Header, curate.AskHeader) {
				found = append(found, capturedQuestion{question: q, at: at, offset: offset})
			}
		}
	}
	return found
}

// Host tool calls establish exposure separately from the stop that prepared them
// A capture retry sees the existing exposure and spends no second round
func (c captureCommand) observe(ctx context.Context, session string, questions []capturedQuestion, window sessionTraces) (sessionExposes, error) {
	if len(questions) == 0 {
		return nil, nil
	}
	mark, err := c.app.mark()
	if err != nil {
		return nil, err
	}
	ledger, err := c.app.ledger()
	if err != nil {
		return nil, err
	}
	all, err := ledger.All(ctx)
	if err != nil {
		return nil, err
	}
	key, err := textkey.Load(c.app.cfg.recordDir)
	if err != nil {
		return nil, err
	}
	iv, err := c.app.intervener()
	if err != nil {
		return nil, err
	}
	a := curate.Asking{Session: session, Mark: mark, All: all, Seal: key.Sum, Words: (hookCommand{app: c.app}).words}
	var shown sessionExposes
	var errs []error
	for _, call := range questions {
		run := window.before(call.at)
		if run.ID == "" {
			continue
		}
		a.Run = run.ID
		q := call.question
		r := curate.Reply{Key: strings.TrimPrefix(q.Header, curate.AskHeader), Question: q.Question, Options: q.options(), Seal: key.Sum}
		observed, err := iv.Observe(ctx, a, r, call.at)
		if observed.Expose.ID != "" {
			shown = append(shown, observed.Expose)
		}
		errs = append(errs, err)
	}
	return shown, errors.Join(errs...)
}
