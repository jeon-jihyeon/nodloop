package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Runes of a corrected answer a review question quotes
const answerLine = 160

// The AskUserQuestion input that decides one waiting candidate
// The question carries what the user needs to decide so a review never names a candidate by a number the user cannot see
type draftQuestion struct {
	ID        string     `json:"id"`
	Version   int        `json:"version"`
	Questions []question `json:"questions"`
}

type question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	MultiSelect bool     `json:"multiSelect"`
	Options     []option `json:"options"`
}

type option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// What a person said on a run that taught a candidate and the answer they said it on
type teaching struct {
	reason string
	answer string
}

var draftOptions = []option{
	{Label: "Approve", Description: "The next prompt in this place receives it"},
	{Label: "Retire", Description: "Drop the lesson"},
	{Label: "Leave waiting", Description: "Ask again at a later start"},
	{Label: "Not a correction", Description: "Your words never judged the answer, so the verdict is withdrawn and the lesson retired"},
}

// The question of the candidate at position i of n
func newDraftQuestion(k knowledge.Knowledge, taught []teaching, i, n int) draftQuestion {
	var b strings.Builder
	fmt.Fprintf(&b, "Approve this lesson?\n%s\nScope: %s", k.Content, k.Run)
	for _, t := range taught {
		fmt.Fprintf(&b, "\nYou said %q on the answer %q", t.reason, t.answer)
	}
	return draftQuestion{ID: k.ID, Version: k.Version, Questions: []question{{
		Question: b.String(), Header: fmt.Sprintf("Draft %d/%d", i, n), Options: draftOptions,
	}}}
}

// One JSON line per candidate that would reach a run of the producer with these labels
func (c knowledgeCommand) ask(ctx context.Context, producer string, labels trace.Labels) error {
	if producer == "" {
		return fmt.Errorf("ask: --producer %w", errRequired)
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return err
	}
	waiting := all.Waiting(producer, labels)
	enc := json.NewEncoder(c.out)
	enc.SetEscapeHTML(false)
	for i, k := range waiting {
		taught, err := c.teachings(ctx, k.Evidence.FeedbackTraceIDs)
		if err != nil {
			return err
		}
		if err := enc.Encode(newDraftQuestion(k, taught, i+1, len(waiting))); err != nil {
			return err
		}
	}
	return nil
}

// The newest reason given on each run and the first line of its output
// A run without a reason is left out
func (c knowledgeCommand) teachings(ctx context.Context, runs []string) ([]teaching, error) {
	traces, err := c.app.traces()
	if err != nil {
		return nil, err
	}
	store, err := c.app.feedback()
	if err != nil {
		return nil, err
	}
	var taught []teaching
	for _, id := range runs {
		verdicts, err := store.List(ctx, feedback.Filter{TraceID: id})
		if err != nil {
			return nil, err
		}
		latest := feedback.Records(verdicts).Latest()
		if len(latest) == 0 || latest[0].Reason == "" {
			continue
		}
		run, err := traces.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		taught = append(taught, teaching{reason: latest[0].Reason, answer: firstLine(run.Output)})
	}
	return taught, nil
}

// The first line of a recorded output cut to answerLine runes
// Text is stored as a JSON string and anything else as its JSON
func firstLine(output json.RawMessage) string {
	text := string(output)
	var s string
	if json.Unmarshal(output, &s) == nil {
		text = s
	}
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if r := []rune(line); len(r) > answerLine {
		return string(r[:answerLine]) + "…"
	}
	return line
}
