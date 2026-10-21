package schema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// Traces in the order their store appended them
type TraceReader interface {
	After(ctx context.Context, f record.TraceFilter, from string) ([]record.Trace, string, error)
}

// Verdicts in the order their store appended them
type VerdictReader interface {
	After(ctx context.Context, from string, limit int) ([]record.Feedback, string, error)
}

// Outcomes in the order their store appended them
type OutcomeReader interface {
	After(ctx context.Context, from string, limit int) ([]record.Outcome, string, error)
}

// Knowledge records in the order their store appended them
type KnowledgeReader interface {
	After(ctx context.Context, from string, limit int) ([]record.Knowledge, string, error)
}

// The guard log of a machine one line per entry as it was written in the order it was written
type GuardReader interface {
	After(ctx context.Context, from string, limit int) ([]json.RawMessage, string, error)
}

// The stores the records of every kind are read from
// A nil store serves none of its kinds so a server without a guard log answers ErrNotServed for it
type Source struct {
	Traces    TraceReader
	Verdicts  VerdictReader
	Outcomes  OutcomeReader
	Knowledge KnowledgeReader
	Guard     GuardReader
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
	// A page with nothing new still answers one so a client that polls passes it again
	Next string `json:"next"`
}

// The page of the records of the kind after the cursor of the query
// 1. records in the order their store appended them as Read reads them
// 2. the free text of the kind left out unless the query asks for content
// 3. an unknown kind fails with ErrKindUnknown, a kind of a store this source lacks with ErrNotServed and a cursor that names no record with ErrCursorInvalid
func (s Source) Page(ctx context.Context, q Query) (Page, error) {
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
	page, next, err := s.Read(ctx, k, after, q.limit())
	if err != nil {
		return Page{}, err
	}
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

// The records of the kind after the cursor in the order their store appended them and the cursor to pass next
// 1. a cursor of a position reads on from it and a limit of zero reads every record
// So a record appended late with an earlier time still reaches every reader once
// 2. a cursor of a time and a record_id such as a digest answers keeps the records after them by time
// It reads every record of the kind once and answers a cursor of a position
// 3. a position the store no longer holds fails with ErrCursorInvalid so a client starts over and dedups by record_id
func (s Source) Read(ctx context.Context, k Kind, after Cursor, limit int) (Records, Cursor, error) {
	if after.Time.IsZero() {
		return s.appended(ctx, k, after.Position, limit)
	}
	all, end, err := s.appended(ctx, k, "", 0)
	if err != nil {
		return nil, Cursor{}, err
	}
	var later Records
	var counts []int
	for i, r := range all {
		if r.after(after) {
			later, counts = append(later, r), append(counts, i+1)
		}
	}
	if limit <= 0 || len(later) <= limit {
		return later, end, nil
	}
	// The position after the last record of the page is where a read of the records up to it ends
	_, next, err := s.appended(ctx, k, "", counts[limit-1])
	return later[:limit], next, err
}

// The stored records of the kind after the position in the order its store appended them
func (s Source) appended(ctx context.Context, k Kind, from string, limit int) (Records, Cursor, error) {
	switch k.Store {
	case StoreTraces:
		if s.Traces != nil {
			return transport(k, func() ([]record.Trace, string, error) {
				return s.Traces.After(ctx, record.TraceFilter{Name: k.Name, Limit: limit}, from)
			})
		}
	case StoreVerdicts:
		if s.Verdicts != nil {
			return transport(k, func() ([]record.Feedback, string, error) { return s.Verdicts.After(ctx, from, limit) })
		}
	case StoreOutcomes:
		if s.Outcomes != nil {
			return transport(k, func() ([]record.Outcome, string, error) { return s.Outcomes.After(ctx, from, limit) })
		}
	case StoreKnowledge:
		if s.Knowledge != nil {
			return transport(k, func() ([]record.Knowledge, string, error) { return s.Knowledge.After(ctx, from, limit) })
		}
	case StoreGuard:
		if s.Guard != nil {
			return transport(k, func() ([]json.RawMessage, string, error) { return s.Guard.After(ctx, from, limit) })
		}
	default:
		return nil, Cursor{}, fmt.Errorf("%w: %q", ErrStoreUnknown, k.Store)
	}
	return nil, Cursor{}, fmt.Errorf("%w: %s", ErrNotServed, k.Name)
}

// The transported form of every record a store read and the cursor of the position it answered
func transport[T any](k Kind, read func() ([]T, string, error)) (Records, Cursor, error) {
	stored, next, err := read()
	if errors.Is(err, record.ErrPositionGone) {
		return nil, Cursor{}, fmt.Errorf("%w: %w", ErrCursorInvalid, err)
	}
	if err != nil {
		return nil, Cursor{}, err
	}
	out := make(Records, 0, len(stored))
	for _, r := range stored {
		rec, err := k.Record(r)
		if err != nil {
			return nil, Cursor{}, err
		}
		out = append(out, rec)
	}
	return out, Cursor{Position: next}, nil
}
