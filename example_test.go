package nodloop_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jeon-jihyeon/nodloop"
)

// The loop of the README: a corrected run teaches a candidate
// A call in process proves no person so its approval is a proposal until the person decides it from a terminal
// or under a person key of nodloop-server
func Example() {
	dir, err := os.MkdirTemp("", "nodloop")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ctx := context.Background()
	c, err := nodloop.Open(dir)
	if err != nil {
		panic(err)
	}
	labels := nodloop.Labels{"tenant": {"acme"}, "task": {"refund"}}
	items := must(c.Items(ctx, "support-bot", labels))
	id := must(c.Record(ctx, nodloop.Run{Producer: "support-bot", Labels: labels, Output: []byte("Here are the refund steps"), Applied: items.Refs()}))
	err = c.Judge(ctx, nodloop.Judgment{
		Run: id, Verdict: nodloop.VerdictReject, ReasonCode: nodloop.ReasonScope, Reason: "the refund window was missing", Reviewer: "ann",
	})
	if err != nil {
		panic(err)
	}

	item := must(c.Propose(ctx, nodloop.Proposal{Kind: nodloop.KindJudgment, Content: "Quote the refund window before the steps", From: id, ID: "refund-window"}))
	proposal := must(c.Approve(ctx, item.ID, item.Version, "ann"))
	fmt.Println(proposal.Authority, proposal.Ref)
	waiting := must(c.Waiting(ctx, "support-bot", labels))
	fmt.Println(waiting.Refs())

	d := must(c.CheckCall(ctx, "support-bot", "Bash", map[string]any{"command": "ls"}))
	fmt.Println(d.Action)
	// Output:
	// agent {refund-window 1}
	// [{refund-window 1}]
	// allow
}

// A stream producer under a holdout with retention
// 1. each one hour block of a service is one draw so a run of the block gets every item or none
// 2. Record takes the same key so report effect counts the run in its arm
// 3. an outcome of a real check sits beside the verdicts in report effect
func Example_holdout() {
	dir, err := os.MkdirTemp("", "nodloop")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ctx := context.Background()
	c, err := nodloop.Open(dir, nodloop.WithHoldout(0.1), nodloop.WithRetention(30))
	if err != nil {
		panic(err)
	}
	defer c.WaitPrune()
	labels := nodloop.Labels{"service": {"checkout"}}
	key := "checkout:" + time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC).Format("2006-01-02T15")
	d := must(c.Draw(ctx, "stream-bot", labels, key))
	id := must(c.Record(ctx, nodloop.Run{
		Producer: "stream-bot", Labels: labels, Output: []byte("lag rose on partition 3"),
		Applied: d.Items.Refs(), Withheld: d.Withheld, HoldoutKey: key,
	}))
	err = c.Outcome(ctx, nodloop.Outcome{Run: id, Result: nodloop.ResultConfirmed, ConfirmedCause: "a slow consumer", Reviewer: "ann"})
	if err != nil {
		panic(err)
	}

	report := must(c.Report(ctx, "effect"))
	fmt.Println(json.Valid(report))
	// Output:
	// true
}

// Stops the example on an error a real caller would handle
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
