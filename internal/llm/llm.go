// Package llm turns a prompt and a JSON Schema into a structured result with its cost
package llm

//go:generate mockgen -source=llm.go -destination=llmmock/client.go -package=llmmock Client

import (
	"cmp"
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
	// Passed as `--effort` to claude and left out of a chat request
	// Empty passes nothing so the host default applies
	Effort string
}

type Response struct {
	Output       json.RawMessage
	CostUSD      float64
	InputTokens  int
	OutputTokens int
	// The share of OutputTokens the model spent thinking before it answered
	ThinkingTokens int
	CacheRead      int
	CacheCreate    int
	Duration       time.Duration
	// The model the backend answered with and empty when it named none
	// An alias such as haiku in the request comes back as the full model id
	Model string
}

// The usage and the duration of both summed
// The output stays the one of the receiver and so does its model unless it named none
func (r Response) add(other Response) Response {
	r.Model = cmp.Or(r.Model, other.Model)
	r.CostUSD += other.CostUSD
	r.InputTokens += other.InputTokens
	r.OutputTokens += other.OutputTokens
	r.ThinkingTokens += other.ThinkingTokens
	r.CacheRead += other.CacheRead
	r.CacheCreate += other.CacheCreate
	r.Duration += other.Duration
	return r
}

// Every consumer of a model takes this one interface and it lives here because mockgen generates llmmock from this file
// llmmock cannot import a consumer without an upward import
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
