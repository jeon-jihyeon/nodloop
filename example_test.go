package nodloop_test

import (
	"context"
	"fmt"
	"os"

	"github.com/jeon-jihyeon/nodloop"
)

// The loop of the README: a corrected run teaches an item the next run of the same tenant receives
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
	items, _ := c.Items(ctx, "support-bot", labels)
	id, _ := c.Record(ctx, nodloop.Run{Producer: "support-bot", Labels: labels, Output: []byte("Here are the refund steps"), Applied: items.Refs()})
	_ = c.Judge(ctx, nodloop.Judgment{Run: id, Verdict: nodloop.VerdictReject, ReasonCode: nodloop.ReasonScope, Reason: "the refund window was missing"})

	item, _ := c.Propose(ctx, nodloop.Proposal{Kind: nodloop.KindJudgment, Content: "Quote the refund window before the steps", From: id, ID: "refund-window"})
	_, _ = c.Approve(ctx, item.ID, item.Version, "ann")
	items, _ = c.Items(ctx, "support-bot", labels)
	fmt.Println(items.Refs())

	d, _ := c.CheckCall(ctx, "support-bot", "Bash", map[string]any{"command": "ls"})
	fmt.Println(d.Action)
	// Output:
	// [{refund-window 1}]
	// allow
}
