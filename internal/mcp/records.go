package mcp

import (
	"context"
	"encoding/json"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// One kind of the published index
// The input schema lists every kind so a client refuses a typo before the call
type recordKind string

type recordsInput struct {
	Kind    recordKind `json:"kind" jsonschema:"the record kind to page such as run, verdict, knowledge, decide or intervention"`
	Since   string     `json:"since,omitempty" jsonschema:"the next cursor the page before answered. Empty for the first page"`
	Limit   int        `json:"limit,omitempty" jsonschema:"records per page. 100 by default and at most 1000"`
	Content bool       `json:"content,omitempty" jsonschema:"true to keep the free text of the kind such as outputs, reasons and lesson content"`
}

type recordsAnswer struct {
	// Each record in the published schema of its kind
	Records []json.RawMessage `json:"records"`
	Next    string            `json:"next"`
}

// One page of the raw records of a kind
// The same page as GET /v1/records/{kind} of nodloop-server
func (s *Server) records(ctx context.Context, _ *sdk.CallToolRequest, in recordsInput) (*sdk.CallToolResult, recordsAnswer, error) {
	page, err := s.source.Page(ctx, schema.Query{Kind: string(in.Kind), Since: in.Since, Limit: in.Limit, Content: in.Content})
	if err != nil {
		return nil, recordsAnswer{}, err
	}
	return nil, recordsAnswer{Records: page.Records, Next: page.Next}, nil
}

// The published index the binary embeds read once by the first server that needs it
var publishedIndex = sync.OnceValue(func() schema.Index {
	ix, err := schema.Published.Index()
	if err != nil {
		// The embedded index always decodes since a test reads it
		panic(err)
	}
	return ix
})

// The kinds of the published index the binary embeds
func recordKinds() []any {
	kinds := publishedIndex().Kinds
	out := make([]any, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, k.Name)
	}
	return out
}

// The trace in the published schema of its kind
// The embedded index holds every trace name since a test checks each has its file
// A trace a store accepted has an id and always encodes
func published(t trace.Trace) json.RawMessage {
	k, err := publishedIndex().Kind(string(t.Name))
	if err != nil {
		panic(err)
	}
	rec, _ := k.Record(t.Record())
	return rec.JSON
}
