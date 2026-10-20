package main

import (
	"cmp"
	"context"
	"errors"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The one stop path that talks to the person after the run of the turn was recorded
// 1. a question curate prepared through the answer shape of the host after its expose is on record
// 2. otherwise a notice of what curate carried out in the place through Stop systemMessage on a host that shows one
// 3. a stop with neither due reads no more records and makes no key
func (c hookCommand) talk(ctx context.Context, sessionID string, place knowledge.RunScope, run string, all knowledge.Set) error {
	notices := c.host.notices() && curate.Noticeable(all, place)
	if len(c.mark.Due) == 0 && !notices {
		return nil
	}
	dir, err := c.app.makeRecordDir()
	if err != nil {
		return err
	}
	iv, err := c.app.intervener()
	if err != nil {
		return err
	}
	key, err := textkey.Load(dir)
	if err != nil {
		return err
	}
	a := curate.Asking{Session: sessionID, Run: run, Place: place, Pushed: c.mode.infers(), Mark: c.mark, Seal: key.Sum, Words: c.words}
	shown, err := iv.Ask(ctx, a)
	if err != nil || shown.Question.Key != "" {
		return errors.Join(err, c.ask(shown.Question))
	}
	if !notices {
		return nil
	}
	shown, err = iv.Notice(ctx, a)
	if err != nil || shown.Notice == "" {
		return err
	}
	return c.host.notice(c.out, shown.Notice)
}

// The question in the answer shape of the host and nothing for none
func (c hookCommand) ask(q curate.Question) error {
	if q.Key == "" {
		return nil
	}
	return c.host.ask(c.out, q.Block())
}

// The person's own words on the runs as the verdicts keep them
func (c hookCommand) words(ctx context.Context, runs []string) ([]string, error) {
	store, err := c.app.feedback()
	if err != nil {
		return nil, err
	}
	var verdicts feedback.Records
	for _, run := range runs {
		list, err := store.List(ctx, record.FeedbackFilter{TraceID: run})
		if err != nil {
			return nil, err
		}
		verdicts = append(verdicts, feedback.RestoreAll(list)...)
	}
	return verdicts.Words(runs), nil
}

// The answer the first word of the prompt gives to the question shown after the answer the person read
// 1. the word is recorded under approver of config.json or else person
// 2. the rest of the message stands
func (c hookCommand) reply(ctx context.Context, sessionID string, previous trace.Trace, message string) (curate.Replied, error) {
	words := strings.Fields(message)
	if sessionID == "" || previous.ID == "" || len(words) == 0 {
		return curate.Replied{}, nil
	}
	iv, err := c.app.intervener()
	if err != nil {
		return curate.Replied{}, err
	}
	return iv.Reply(ctx, curate.Reply{Session: sessionID, Run: previous.ID, Word: words[0], Person: cmp.Or(c.app.cfg.approver, unnamedPerson)})
}

// The reply word answered the question so the conversation records nothing of it
const replyNote = "nodloop: the user's first word %s answered the nodloop question and nodloop recorded it. " +
	"Record nothing about it and do not ask it again. The rest of the user's message stands.\n"
