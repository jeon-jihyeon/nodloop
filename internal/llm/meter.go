package llm

import (
	"context"
	"errors"
	"sync"

	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A Client that sums the usage of the calls it passes on
// One meter serves the drafter and the critic of an extraction so its trace carries what both cost
type Meter struct {
	client Client
	mu     sync.Mutex
	spent  Response
}

func NewMeter(client Client) *Meter {
	return &Meter{client: client}
}

// A failed call adds the cost a ResultError keeps since the backend charged it
func (m *Meter) Complete(ctx context.Context, req Request) (Response, error) {
	res, err := m.client.Complete(ctx, req)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spent = m.spent.add(res)
	var re *ResultError
	if errors.As(err, &re) {
		m.spent.CostUSD += re.CostUSD
	}
	return res, err
}

// The usage summed since the last Take
// The next sum starts at zero so a second extraction through the same meter never counts the first
func (m *Meter) Take() Response {
	m.mu.Lock()
	defer m.mu.Unlock()
	spent := m.spent
	m.spent = Response{}
	return spent
}

// The usage as a trace records it
func (r Response) Usage() trace.Usage {
	return trace.Usage{
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, ThinkingTokens: r.ThinkingTokens,
		CacheReadTokens: r.CacheRead, CacheCreateTokens: r.CacheCreate, CostUSD: r.CostUSD,
	}
}
