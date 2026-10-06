package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/loop"
)

type queueInput struct {
	Limit     *int     `json:"limit,omitempty" jsonschema:"runs to list including audit samples. 10 by default and 0 lists every run"`
	AuditRate *float64 `json:"audit_rate,omitempty" jsonschema:"share of the limit drawn at random from the rest of the order. 0.2 by default"`
	Seed      *int64   `json:"seed,omitempty" jsonschema:"the same seed draws the same audit samples. The clock by default"`
}

// The item order with the recorded output of each so the user can judge it in place
func (s *Server) queue(ctx context.Context, _ *sdk.CallToolRequest, in queueInput) (*sdk.CallToolResult, any, error) {
	opts := loop.QueueOptions{Limit: loop.QueueLimit, AuditRate: loop.QueueAuditRate, Seed: s.now().UnixNano()}
	if in.Limit != nil {
		opts.Limit = *in.Limit
	}
	if in.AuditRate != nil {
		opts.AuditRate = *in.AuditRate
	}
	if in.Seed != nil {
		opts.Seed = *in.Seed
	}
	h, err := loop.Load(ctx, s.traces, s.verdicts, s.outcomes, s.ledger)
	if err != nil {
		return nil, nil, err
	}
	items, err := h.Queue(opts)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.TraceID)
	}
	return nil, map[string]any{"seed": opts.Seed, "items": items, "outputs": h.Outputs(ids)}, nil
}

// Health rows and audit issues of every knowledge version
func (s *Server) knowledgeHealth(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
	h, err := loop.Load(ctx, s.traces, s.verdicts, s.outcomes, s.ledger)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"items": h.Health(s.now()), "issues": h.BrokenReferences()}, nil
}

type reaffirmInput struct {
	ID       string `json:"id" jsonschema:"the approved knowledge id"`
	Version  int    `json:"version" jsonschema:"the approved version the user rechecked"`
	Approver string `json:"approver" jsonschema:"the name the user gave. Never a default"`
}

func (s *Server) reaffirm(ctx context.Context, _ *sdk.CallToolRequest, in reaffirmInput) (*sdk.CallToolResult, any, error) {
	k, err := s.ledger.Reaffirm(ctx, in.ID, in.Version, s.approver(in.Approver))
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"id": k.ID, "version": k.Version, "approver": k.Approver, "reviewed_at": k.ReviewedAt}, nil
}
