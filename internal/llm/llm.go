// Package llm turns a prompt and a JSON Schema into a structured result with its cost
package llm

//go:generate mockgen -source=llm.go -destination=llmmock/client.go -package=llmmock Client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Request struct {
	// Passed as `--append-system-prompt`
	// Omitted when empty
	System string
	// Passed on stdin
	Prompt string
	// Passed as `--json-schema`
	// Required
	Schema json.RawMessage
	// Empty means the default model
	Model string
}

type Response struct {
	Output       json.RawMessage
	CostUSD      float64
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheCreate  int
	Duration     time.Duration
}

// The consumer is diagnose but the interface lives here because mockgen generates llmmock from this file
// llmmock cannot import diagnose without an upward import
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// `claude -p` printed a result JSON whose subtype is not success
// CostUSD is kept so the caller can charge the failed run
type ResultError struct {
	Subtype string
	Errors  []string
	CostUSD float64
}

func (e *ResultError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("claude result %s", e.Subtype)
	}
	return fmt.Sprintf("claude result %s: %s", e.Subtype, strings.Join(e.Errors, "; "))
}
