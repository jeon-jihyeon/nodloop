package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/textkey"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The one stop path that talks to the person after the run of the turn was recorded
// 1. a requested manual question prepared through Stop additionalContext without recording exposure
// 2. otherwise a notice of what curate carried out in the place through Stop systemMessage
// 3. unrequested candidates reach no question reader or key
// 4. a question is prepared only where the capture can read its tool call from the transcript while a notice needs none
func (c hookCommand) talk(ctx context.Context, sessionID string, place knowledge.RunScope, run string, all knowledge.Set, t transcript) error {
	asks, notices, err := c.showable(ctx, sessionID, place, all)
	if err != nil || !asks && !notices {
		return err
	}
	asks = asks && c.observable(t, sessionID)
	if !asks && !notices {
		return nil
	}
	iv, err := c.app.intervener()
	if err != nil {
		return err
	}
	// The record directory exists once the exposes were read
	key, err := textkey.Load(c.app.cfg.recordDir)
	if err != nil {
		return err
	}
	a := curate.Asking{Session: sessionID, Run: run, Place: place, Mark: c.mark, All: all, Seal: key.Sum, Words: c.words}
	if asks {
		asked, err := iv.Prepare(ctx, a)
		if err != nil || asked.Question.Key != "" {
			return errors.Join(err, c.ask(asked.Question))
		}
	}
	if !notices {
		return nil
	}
	noticed, err := iv.Notice(ctx, a)
	if err != nil || noticed.Notice == "" {
		return err
	}
	return c.host.notice(c.out, noticed.Notice)
}

// Whether the stop of the session may still show a question and a notice
// 1. a host whose replies code cannot read is shown nothing so every question stays due
// 2. curate reads every question and answer on record so curate.json and the exposes of the session decide first
// An unrequested candidate triggers no question read while a requested proposal stays due until observed
// 3. exposes that cannot be read show nothing and the stop reports why
func (c hookCommand) showable(ctx context.Context, sessionID string, place knowledge.RunScope, all knowledge.Set) (asks, notices bool, err error) {
	if !c.host.talks() {
		return false, false, nil
	}
	due := dueQuestions(c.mark.Due)
	proposed := due.proposed(sessionID)
	notices = curate.Noticeable(all, place)
	if !proposed && !notices {
		return false, false, nil
	}
	shown, err := c.app.exposes(ctx, sessionID, c.app.now().Add(-sessionWindow))
	if err != nil {
		return false, false, err
	}
	return proposed, notices && !shown.noticed(), nil
}

// Whether the capture can read a question tool call of the session from the transcript the host keeps
// A transcript is necessary for observing a question but never proves that its tool is available
func (c hookCommand) observable(t transcript, session string) bool {
	if t == "" || c.rules.config == "" || t.kept(filePath(filepath.Join(string(c.rules.config), "projects")), session) != nil {
		return false
	}
	info, err := os.Stat(string(t))
	return err == nil && info.Mode().IsRegular()
}

// The question as Stop additionalContext and nothing for none
// Additional context continues the turn without the error label of a blocking decision
func (c hookCommand) ask(q curate.Question) error {
	if q.Key == "" {
		return nil
	}
	return c.host.context(c.out, "Stop", q.Block())
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

// The choice the first word of the prompt names of the question shown after the answer the person read and empty for none
// 1. the hook only keeps that word from being judged and records no answer
// Any process can pipe a prompt into the hook so the capture records the answer from the transcript the host keeps
// 2. the rest of the message stands
// 3. a prompt after an answer whose stop asked no question reads no record of curate
// The question shown after that answer is recorded after it so the read stops at its time
func (c hookCommand) reply(ctx context.Context, sessionID string, previous trace.Trace, message string) (curate.Choice, error) {
	words := strings.Fields(message)
	if sessionID == "" || previous.ID == "" || len(words) == 0 {
		return "", nil
	}
	shown, err := c.app.exposes(ctx, sessionID, previous.Time)
	return shown.named(previous.ID, words[0]), err
}

// The reply word names a choice of the question so the conversation records nothing of it
const replyNote = "nodloop: the user's first word %s answers the nodloop question and nodloop records the answer after this turn. " +
	"Record nothing about it and do not ask it again. The rest of the user's message stands.\n"

// The questions curate.json leaves due as a stop reads them before curate does
type dueQuestions []curate.Due

// Whether one asks about a proposal the session made
// Its stop asks it whatever was asked before since the person asked for it
func (ds dueQuestions) proposed(session string) bool {
	return slices.ContainsFunc(ds, func(d curate.Due) bool { return d.Proposal != "" && d.Session == session })
}

// The exposes of one session newest first
type sessionExposes trace.Traces

// The exposes of the session since the time
// 1. only exposes of the session at or after the time are returned
// 2. a corrupt line returns the exposes found with the error
func (a app) exposes(ctx context.Context, session string, since time.Time) (sessionExposes, error) {
	store, err := a.traces()
	if err != nil {
		return nil, err
	}
	found, err := store.List(ctx, record.TraceFilter{Name: string(trace.NameExpose), SessionID: session, Since: since})
	return sessionExposes(trace.RestoreAll(found)), err
}

// The questions the stop after the run asked
func (xs sessionExposes) questions(run string) []curate.Exposure {
	var out []curate.Exposure
	for _, t := range xs {
		var x curate.Exposure
		if json.Unmarshal(t.Input, &x) == nil && x.Kind != curate.QuestionNotice && x.Run == run {
			out = append(out, x)
		}
	}
	return out
}

// The choice the word names of a question the stop asked after the run and empty for none
// curate names it so the hook and the capture read a reply as curate records it
func (xs sessionExposes) named(run, word string) curate.Choice {
	for _, x := range xs.questions(run) {
		if choice, ok := x.Names(word); ok {
			return choice
		}
	}
	return ""
}

// Whether curate pushed the one question of the session
func (xs sessionExposes) pushed() bool {
	return slices.ContainsFunc(xs, func(t trace.Trace) bool {
		return t.Source == trace.SourcePush && t.Subject != string(curate.QuestionNotice)
	})
}

// Whether the session was shown its one notice
func (xs sessionExposes) noticed() bool {
	return slices.ContainsFunc(xs, func(t trace.Trace) bool { return t.Subject == string(curate.QuestionNotice) })
}
