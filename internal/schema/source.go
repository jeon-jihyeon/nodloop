package schema

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// Traces as their store keeps them
type TraceLister interface {
	List(ctx context.Context, f record.TraceFilter) ([]record.Trace, error)
}

// Verdicts as their store keeps them
type VerdictLister interface {
	List(ctx context.Context, f record.FeedbackFilter) ([]record.Feedback, error)
}

// Outcomes as their store keeps them
// An empty trace id lists every outcome
type OutcomeLister interface {
	List(ctx context.Context, traceID string) ([]record.Outcome, error)
}

// Knowledge records as their store keeps them
type KnowledgeLister interface {
	List(ctx context.Context) ([]record.Knowledge, error)
}

// The guard log of a machine one line per entry as it was written
type GuardLister interface {
	List(ctx context.Context) ([]json.RawMessage, error)
}

// The stores the records of every kind are read from
// A nil store serves none of its kinds so a server without a guard log answers ErrNotServed for it
type Source struct {
	Traces    TraceLister
	Verdicts  VerdictLister
	Outcomes  OutcomeLister
	Knowledge KnowledgeLister
	Guard     GuardLister
}

const (
	// Records a page holds when the query names no limit
	DefaultLimit = 100
	// The most records one page holds
	MaxLimit = 1000
)

// One page asked of the records of one kind
// The MCP tool records, GET /v1/records/{kind} and the Go Records take it alike
type Query struct {
	Kind string
	// The cursor the page before answered and empty for the first page
	Since string
	// 0 for DefaultLimit and cut to MaxLimit
	Limit int
	// Whether the free text of the kind stays in the records
	Content bool
}

func (q Query) limit() int {
	switch {
	case q.Limit <= 0:
		return DefaultLimit
	case q.Limit > MaxLimit:
		return MaxLimit
	}
	return q.Limit
}

// One page of records in the published schema of their kind
type Page struct {
	Records []json.RawMessage `json:"records"`
	// The cursor to pass as Since next
	// It stays the cursor given while nothing newer is settled so a client that polls passes it again
	Next string `json:"next"`
}

// The page of the records of the kind after the cursor of the query
// 1. records by time then record_id and only those at least Settle old as Records Page holds them
// 2. the free text of the kind left out unless the query asks for content
// 3. an unknown kind fails with ErrKindUnknown, a kind of a store this source lacks with ErrNotServed and a cursor that names no record with ErrCursorInvalid
func (s Source) Page(ctx context.Context, q Query, now time.Time) (Page, error) {
	ix, err := Published.Index()
	if err != nil {
		return Page{}, err
	}
	k, err := ix.Kind(q.Kind)
	if err != nil {
		return Page{}, err
	}
	after, err := ParseCursor(q.Since)
	if err != nil {
		return Page{}, err
	}
	all, err := s.Records(ctx, k, after)
	if err != nil {
		return Page{}, err
	}
	page, next := all.Page(after, q.limit(), now)
	out := Page{Records: make([]json.RawMessage, 0, len(page)), Next: next.String()}
	for _, r := range page {
		if !q.Content {
			if r, err = k.Strip(r); err != nil {
				return Page{}, err
			}
		}
		out.Records = append(out.Records, r.JSON)
	}
	return out, nil
}

// Every stored record of the kind from a settle time before the cursor on in the order its store lists them
// The store reads back that far since an append may land in the file after a record of a later time
func (s Source) Records(ctx context.Context, k Kind, after Cursor) (Records, error) {
	var since time.Time
	if after != (Cursor{}) {
		since = after.Time.Add(-Settle)
	}
	switch k.Store {
	case StoreTraces:
		if s.Traces != nil {
			return transport(k, func() ([]record.Trace, error) {
				return s.Traces.List(ctx, record.TraceFilter{Name: k.Name, Since: since})
			})
		}
	case StoreVerdicts:
		if s.Verdicts != nil {
			return transport(k, func() ([]record.Feedback, error) { return s.Verdicts.List(ctx, record.FeedbackFilter{Since: since}) })
		}
	case StoreOutcomes:
		if s.Outcomes != nil {
			return transport(k, func() ([]record.Outcome, error) { return s.Outcomes.List(ctx, "") })
		}
	case StoreKnowledge:
		if s.Knowledge != nil {
			return transport(k, func() ([]record.Knowledge, error) { return s.Knowledge.List(ctx) })
		}
	case StoreGuard:
		if s.Guard != nil {
			return transport(k, func() ([]json.RawMessage, error) { return s.Guard.List(ctx) })
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrStoreUnknown, k.Store)
	}
	return nil, fmt.Errorf("%w: %s", ErrNotServed, k.Name)
}

// The transported form of every record a store listed
func transport[T any](k Kind, list func() ([]T, error)) (Records, error) {
	stored, err := list()
	if err != nil {
		return nil, err
	}
	out := make(Records, 0, len(stored))
	for _, r := range stored {
		rec, err := k.Record(r)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}
