package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The part of a transcript one capture read
// The next capture of the session reads on from To so no message is read twice
type captureInput struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// What one capture read and wrote with no text of the transcript
type captureOutput struct {
	// The user messages read and how many became verdicts, answers and signals
	Messages int `json:"messages"`
	Verdicts int `json:"verdicts"`
	Answers  int `json:"answers"`
	Signals  int `json:"signals"`
	// The run each message read judged in the order of the messages
	Runs []string `json:"runs"`
	// The record_id of each verdict, answer and signal it wrote
	Records []string `json:"records"`
	// The classify traces it made
	Classify []string `json:"classify"`
}

// The capture trace of the pass over the transcript of the session
// 1. the session is named and the offsets run forward from 0
// 2. no message makes more verdicts, answers and signals than there were messages
func (in captureInput) trace(session string, out captureOutput, now time.Time) (trace.Trace, error) {
	switch {
	case session == "" || in.From < 0 || in.To < in.From:
		return trace.Trace{}, fmt.Errorf("%w: a capture names its session and the offsets %d to %d", errSessionTrace, in.From, in.To)
	case out.Verdicts+out.Answers+out.Signals > out.Messages:
		return trace.Trace{}, fmt.Errorf("%w: %d messages made more records than they hold", errSessionTrace, out.Messages)
	}
	// Offsets, counts and ids always encode
	input, _ := json.Marshal(in)
	output, _ := json.Marshal(out.listed())
	return trace.Trace{ID: trace.NewID(now), Name: trace.NameCapture, SessionID: session, Time: now, Input: input, Output: output}, nil
}

// The output with an empty list for none so a reader never meets null
func (out captureOutput) listed() captureOutput {
	for _, ids := range []*[]string{&out.Runs, &out.Records, &out.Classify} {
		if *ids == nil {
			*ids = []string{}
		}
	}
	return out
}
