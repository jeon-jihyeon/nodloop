package curate

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A correction younger than this is left to the extraction its own stop started
// The daily pass starts at a stop too and would otherwise draft the same run beside that extraction
const GapSettle = time.Hour

// The conclusion of an extraction whose model call failed so a later try may still draft it
const extractFailed = "failed"

// The corrected runs of the window the daily pass extracts in the order they were recorded
// 1. a run of any producer no extraction ever read and one whose only extraction failed so a model failure is tried once more
// 2. a failed run, a run a knowledge version cites and one whose reconciled verdict corrects nothing are left out
// A person's verdict wins over the inferred one as in every extraction
// 3. a correction younger than GapSettle is left to the extraction its own stop started
func (c *Curator) Gaps(ctx context.Context, since time.Time) ([]string, error) {
	runs, err := c.list(ctx, record.TraceFilter{Name: string(trace.NameRun), Since: since})
	if err != nil {
		return nil, err
	}
	extracts, err := c.list(ctx, record.TraceFilter{Name: string(trace.NameExtract), Since: since})
	if err != nil {
		return nil, err
	}
	list, err := c.verdicts.List(ctx, record.FeedbackFilter{Since: since})
	if err != nil {
		return nil, err
	}
	all, err := c.ledger.All(ctx)
	if err != nil {
		return nil, err
	}
	g := gaps{cited: all, tried: map[string][]string{}, verdicts: map[string]feedback.Feedback{}, settled: c.now().Add(-GapSettle)}
	for _, t := range extracts {
		// An extract trace that does not decode still read its run
		var out struct {
			Conclusion string `json:"conclusion"`
		}
		_ = json.Unmarshal(t.Output, &out)
		g.tried[t.Ref] = append(g.tried[t.Ref], out.Conclusion)
	}
	for _, v := range feedback.RestoreAll(list).Reconciled() {
		g.verdicts[v.TraceID] = v
	}
	var out []string
	for _, r := range slices.Backward(runs) {
		if g.open(r) {
			out = append(out, r.ID)
		}
	}
	return out, nil
}

// What the window holds that tells a gap
type gaps struct {
	cited knowledge.Set
	// The conclusions of the extractions of each run
	tried map[string][]string
	// The reconciled verdict of each run
	verdicts map[string]feedback.Feedback
	settled  time.Time
}

// Whether the run is a correction no extraction drafted yet
func (g gaps) open(r trace.Trace) bool {
	v, judged := g.verdicts[r.ID]
	read := g.tried[r.ID]
	if r.Error != "" || !judged || !v.Corrects() || v.Time.After(g.settled) || g.cited.Cites(r.ID) {
		return false
	}
	return len(read) == 0 || len(read) == 1 && read[0] == extractFailed
}
