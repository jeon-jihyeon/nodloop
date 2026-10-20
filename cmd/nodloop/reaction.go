package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// Time an endpoint of the reaction point may take
// The prompt hook waits for it before the conversation starts
const reactionTimeout = 5 * time.Second

const (
	// Runes of the previous answer the reaction point reads
	reactionAnswerRunes = 4000
	// Runes of the person's words a verdict of the reaction point or the capture keeps as its reason
	reasonRunes = 500
)

// The words the person typed after an answer
type personWords string

// The words as the reason of a verdict: redacted and cut at reasonRunes
// The judge reads them back through the reasons of the capture so both see the same text
func (w personWords) reason() string {
	reason := feedback.Redact(string(w))
	if utf8.RuneCountInString(reason) > reasonRunes {
		reason = string([]rune(reason)[:reasonRunes])
	}
	return reason
}

// The name the reaction point records its built in member under since that member is the conversation and no model
const conversationMember = "conversation"

// The conversation as the member a reaction setup names claude
// It always defers so the conversation judges the message when no endpoint was sure
type conversation struct{}

func (conversation) Classify(context.Context, classify.Request) (classify.Answers, error) {
	return nil, errDeferred
}

// One turn as the reaction point reads it
type turn struct {
	run     trace.Trace
	message string
}

// The user's message and the previous answer cut at reactionAnswerRunes
// The message comes first since an encoder endpoint truncates a long state from its end
func (t turn) state() string {
	var answer string
	if json.Unmarshal(t.run.Output, &answer) != nil {
		answer = string(t.run.Output)
	}
	if utf8.RuneCountInString(answer) > reactionAnswerRunes {
		answer = string([]rune(answer)[:reactionAnswerRunes]) + "\n[cut]"
	}
	return "## User message\n\n" + t.message + "\n\n## Previous answer\n\n" + answer
}

// The verdict the reaction point recorded on the newest answer of the session and empty when it recorded none
// 1. nothing is asked without a reaction setup, in a mode that infers nothing, on the first prompt or for an empty message
// 2. a deferral to the conversation is no failure since the conversation then judges as it would without the point
// 3. a reject keeps the user's message redacted and cut as its reason because no reason code can be told from it
func (c hookCommand) react(ctx context.Context, t turn) (feedback.Verdict, error) {
	if c.reaction == nil || !c.mode.infers() || t.run.ID == "" || strings.TrimSpace(t.message) == "" {
		return "", nil
	}
	answers, err := c.reaction.Classify(ctx, classify.Request{Ref: t.run.ID, State: t.state(), Questions: classify.ReactionQuestions})
	if errors.Is(err, errDeferred) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	v := answers.Verdict()
	if v == "" {
		return "", nil
	}
	code, reason := feedback.ReasonCode(""), ""
	if v == feedback.VerdictReject {
		code, reason = feedback.ReasonOther, personWords(t.message).reason()
	}
	fb, err := feedback.New(t.run.ID, v, code, reason, nil, feedback.ReviewerSession, c.app.now())
	if err != nil {
		return "", err
	}
	store, err := c.app.feedback()
	if err != nil {
		return "", err
	}
	return v, store.Append(ctx, fb.Record())
}
