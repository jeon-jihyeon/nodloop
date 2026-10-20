package mcp

import (
	"context"
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/schema"
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
	page, err := s.source.Page(ctx, schema.Query{Kind: string(in.Kind), Since: in.Since, Limit: in.Limit, Content: in.Content}, s.now())
	if err != nil {
		return nil, recordsAnswer{}, err
	}
	return nil, recordsAnswer{Records: page.Records, Next: page.Next}, nil
}

// The kinds of the published index the binary embeds
func recordKinds() []any {
	ix, err := schema.Published.Index()
	if err != nil {
		// The embedded index always decodes since a test reads it
		panic(err)
	}
	out := make([]any, 0, len(ix.Kinds))
	for _, k := range ix.Kinds {
		out = append(out, k.Name)
	}
	return out
}
