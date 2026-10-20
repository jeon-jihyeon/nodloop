package mcp

import (
	"context"
	"encoding/json"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type queueInput struct {
	Limit     *int     `json:"limit,omitempty" jsonschema:"runs to list including audit samples. 10 by default and 0 lists every run"`
	AuditRate *float64 `json:"audit_rate,omitempty" jsonschema:"share of the limit drawn at random from the rest of the order. 0.2 by default"`
	Seed      *int64   `json:"seed,omitempty" jsonschema:"the same seed draws the same audit samples. The clock by default"`
}

type queueAnswer struct {
	Seed  int64            `json:"seed"`
	Items []loop.QueueItem `json:"items"`
	// The recorded output per trace id as the run recorded it
	Outputs map[string]json.RawMessage `json:"outputs"`
}

// The item order with the recorded output of each so the user can judge it in place
func (s *Server) queue(ctx context.Context, _ *sdk.CallToolRequest, in queueInput) (*sdk.CallToolResult, queueAnswer, error) {
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
	since, err := s.since(ctx)
	if err != nil {
		return nil, queueAnswer{}, err
	}
	h, err := loop.Load(ctx, s.traces, s.verdicts, s.outcomes, s.ledger, since)
	if err != nil {
		return nil, queueAnswer{}, err
	}
	items, err := h.Queue(opts)
	if err != nil {
		return nil, queueAnswer{}, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.TraceID)
	}
	return nil, queueAnswer{Seed: opts.Seed, Items: items, Outputs: h.Outputs(ids)}, nil
}

type healthAnswer struct {
	Items  []loop.Health `json:"items"`
	Issues []loop.Issue  `json:"issues"`
}

// Health rows and audit issues of every knowledge version
func (s *Server) knowledgeHealth(ctx context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, healthAnswer, error) {
	since, err := s.since(ctx)
	if err != nil {
		return nil, healthAnswer{}, err
	}
	h, err := loop.Load(ctx, s.traces, s.verdicts, s.outcomes, s.ledger, since)
	if err != nil {
		return nil, healthAnswer{}, err
	}
	return nil, healthAnswer{Items: h.Health(s.now()), Issues: h.BrokenReferences()}, nil
}

type reportInput struct {
	Name loop.ReportName `json:"name" jsonschema:"the report to read"`
}

type reportAnswer struct {
	Name   loop.ReportName `json:"name"`
	Report json.RawMessage `json:"report"`
	// The start of the runs the report read
	// Left out when it read every run
	Since time.Time `json:"since,omitzero"`
}

// The report under its name so the answer is an object whatever shape the report has
func (s *Server) report(ctx context.Context, _ *sdk.CallToolRequest, in reportInput) (*sdk.CallToolResult, reportAnswer, error) {
	since, err := s.since(ctx)
	if err != nil {
		return nil, reportAnswer{}, err
	}
	stores := loop.Stores{Traces: s.traces, Verdicts: s.verdicts, Outcomes: s.outcomes, Items: s.ledger, Since: since}
	report, err := stores.Report(ctx, in.Name, s.now())
	if err != nil {
		return nil, reportAnswer{}, err
	}
	return nil, reportAnswer{Name: in.Name, Report: report, Since: since}, nil
}

type reaffirmInput struct {
	ID       string `json:"id" jsonschema:"the approved knowledge id"`
	Version  int    `json:"version" jsonschema:"the approved version the user rechecked"`
	Approver string `json:"approver" jsonschema:"the name the user gave. Never a default"`
}

type reaffirmAnswer struct {
	ID         string    `json:"id"`
	Version    int       `json:"version"`
	Approver   string    `json:"approver"`
	ReviewedAt time.Time `json:"reviewed_at"`
	// The intervention a call without a proof of a person recorded as an agent proposal while the version stayed as it was
	Proposal string `json:"proposal,omitempty"`
}

// Reaffirms under a key that proves a person and records an agent proposal of any other call
// A proposal names an approved version and answers it as it stands
func (s *Server) reaffirm(ctx context.Context, _ *sdk.CallToolRequest, in reaffirmInput) (*sdk.CallToolResult, reaffirmAnswer, error) {
	approver := s.approver(in.Approver)
	if err := knowledge.CheckApprover(approver); err != nil {
		return nil, reaffirmAnswer{}, err
	}
	judgment := trace.Intervention{Action: trace.ActionJudge, ID: in.ID, Version: in.Version, Choice: trace.ChoiceKeep, Proof: s.proof()}
	var k knowledge.Knowledge
	var err error
	var wrote knowledge.Set
	id := trace.NewID(s.now())
	if judgment.Proof == trace.ProofNone {
		k, err = s.ledger.Approved(ctx, in.ID, in.Version)
	} else {
		judgment.Person = approver
		k, err = s.ledger.Under(id).Reaffirm(ctx, in.ID, in.Version, approver)
		wrote = knowledge.Set{k}
	}
	if err != nil {
		return nil, reaffirmAnswer{}, err
	}
	id, err = s.intervened(ctx, id, judgment, wrote)
	if err != nil {
		return nil, reaffirmAnswer{}, err
	}
	s.afterVerdict(ctx)
	answer := reaffirmAnswer{ID: k.ID, Version: k.Version, Approver: k.Approver, ReviewedAt: k.ReviewedAt}
	if wrote == nil {
		answer.Proposal = id
	}
	return nil, answer, nil
}
