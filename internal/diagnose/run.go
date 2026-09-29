package diagnose

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

// Reviews in flight at once when the caller names no count
const defaultParallel = 4

// One review of the batch path
type Job struct {
	EventID string
	Options BatchOptions
}

// Runs the jobs through Run with parallel reviews in flight
// Zero parallel means the default and a nil log means silent
// 1. a negative parallel is refused before any review
// 2. results come back in job order and hold only the jobs that recorded a trace
// 3. a review that failed after its context was built keeps its failed trace and the run goes on
// 4. the first error without a trace stops new reviews while those in flight finish so none is cut into a false model failure
// 5. the results finished so far are returned with the first error in job order
// 6. a context cancelled before every job started is the error when no job failed
// 7. a panic becomes the error of its job instead of taking the whole run down
// 8. every review logs one line and lines never interleave
func (d *Diagnoser) RunAll(ctx context.Context, jobs []Job, parallel int, log io.Writer) ([]Result, error) {
	if parallel < 0 {
		return nil, fmt.Errorf("%w: %d", ErrNegativeParallel, parallel)
	}
	progress := &lockedWriter{w: cmp.Or(log, io.Discard)}
	results := make([]Result, len(jobs))
	errs := make([]error, len(jobs))
	var failed atomic.Bool
	var wg sync.WaitGroup
	var stopped error
	slots := make(chan struct{}, cmp.Or(parallel, defaultParallel))
	for i, j := range jobs {
		slots <- struct{}{}
		if stopped = ctx.Err(); stopped != nil || failed.Load() {
			break
		}
		wg.Go(func() {
			defer func() {
				if p := recover(); p != nil {
					errs[i] = fmt.Errorf("%w: %s: %v", ErrReviewPanicked, j.EventID, p)
				}
				if errs[i] != nil {
					failed.Store(true)
				}
				<-slots
			}()
			results[i], errs[i] = d.runJob(ctx, j, progress)
		})
	}
	wg.Wait()
	out := make([]Result, 0, len(jobs))
	for i := range jobs {
		if results[i].TraceID != "" {
			out = append(out, results[i])
		}
	}
	for _, err := range errs {
		if err != nil {
			return out, err
		}
	}
	return out, stopped
}

// 1. a review that fails after its context was built comes back with the id of its failed trace
// 2. a cancelled context is the error of that job and never a model failure
// 3. a failure without a trace is a store failure or a review that never built its context
func (d *Diagnoser) runJob(ctx context.Context, j Job, log io.Writer) (Result, error) {
	label := strings.Join(j.Options.Session.Tags, ",")
	res, err := d.Run(ctx, j.EventID, j.Options)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return Result{}, ctx.Err()
		case res.TraceID == "":
			return Result{}, fmt.Errorf("%w: %s: %w", ErrNoFailedTrace, j.EventID, err)
		}
		fmt.Fprintf(log, "%s\t%s\tfailed: %v\n", j.EventID, label, err)
		return res, nil
	}
	tr, err := d.traces.Get(ctx, res.TraceID)
	if err != nil {
		return Result{}, err
	}
	fmt.Fprintf(log, "%s\t%s\t%s\tforced=%t\t$%.4f\t%dms\n",
		j.EventID, label, res.Diagnosis.Status, res.Forced, tr.Usage.CostUSD, tr.DurationMS)
	return res, nil
}

// Progress lines from several workers must not interleave inside one line
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
