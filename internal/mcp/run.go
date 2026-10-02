package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/diagnose"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// A server over the records alone for a client with no data directory
// Every tool of the data review answers missing and the tools of runs, nods and knowledge work
func NewRecords(
	traces TraceStore, verdicts FeedbackStore, outcomes OutcomeStore, ledger *knowledge.Ledger, compactor *compact.Compactor,
	now func() time.Time, session diagnose.Session, exe, dataArgs string, missing error,
) *Server {
	return &Server{
		traces: traces, verdicts: verdicts, outcomes: outcomes, ledger: ledger, compactor: compactor, now: now, session: session,
		exe: exe, dataArgs: dataArgs, missing: missing,
	}
}

type checkCompactionInput struct {
	Compaction string                   `json:"compaction" jsonschema:"the compaction id from propose_compaction"`
	Items      []compact.CoverageAnswer `json:"items" jsonschema:"one entry per old item: the new items that state it and every fact of it no new item states"`
}

// Records the coverage the conversation checked for a compaction of run items
// Approval reads the newest one, so a fixed draft is checked again before approve_compaction
func (s *Server) checkCompaction(ctx context.Context, _ *sdk.CallToolRequest, in checkCompactionInput) (*sdk.CallToolResult, any, error) {
	cov, err := s.compactor.Record(ctx, in.Compaction, in.Items)
	if err != nil {
		return nil, nil, err
	}
	c, err := s.ledger.Compaction(ctx, in.Compaction)
	if err != nil {
		return nil, nil, err
	}
	answer := map[string]any{"compaction": cov.Compaction, "items": cov.Items, "passed": true}
	if err := cov.Passes(c); err != nil {
		answer["passed"], answer["why"] = false, err.Error()
	}
	return nil, answer, nil
}

// A tool of the data review answers the missing data dir of a records server instead of reading nil stores
func needsData[In any](
	h func(*Server, context.Context, *sdk.CallToolRequest, In) (*sdk.CallToolResult, any, error),
) func(*Server, context.Context, *sdk.CallToolRequest, In) (*sdk.CallToolResult, any, error) {
	return func(s *Server, ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
		if s.missing != nil {
			return nil, nil, s.missing
		}
		return h(s, ctx, req, in)
	}
}

type runInput struct {
	Producer string              `json:"producer" jsonschema:"who made the output such as session"`
	Labels   map[string][]string `json:"labels,omitempty" jsonschema:"the situation of the run as key to values such as repo, path or task. Knowledge scoped to these labels reaches it"`
	Subject  string              `json:"subject,omitempty" jsonschema:"what the run was about in a few words"`
	Output   any                 `json:"output" jsonschema:"the output as it was given to the person, a JSON value or text"`
	Applied  []knowledge.Ref     `json:"applied,omitempty" jsonschema:"the knowledge items with their versions that the run applied, as knowledge_for answered them"`
}

// Records one output so a person can nod on it
func (s *Server) run(ctx context.Context, _ *sdk.CallToolRequest, in runInput) (*sdk.CallToolResult, any, error) {
	output, ok := in.Output.(string)
	b := []byte(output)
	if !ok {
		var err error
		if b, err = json.Marshal(in.Output); err != nil {
			return nil, nil, err
		}
	}
	input, err := json.Marshal(map[string][]knowledge.Ref{"applied": in.Applied})
	if err != nil {
		return nil, nil, err
	}
	tr, err := trace.NewRun(in.Producer, in.Subject, in.Labels, input, b, s.now())
	if err != nil {
		return nil, nil, err
	}
	tr.SessionID = s.session.ID
	if err := s.traces.Append(ctx, tr); err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"trace_id": tr.ID}, nil
}

type knowledgeForInput struct {
	Producer string              `json:"producer" jsonschema:"the producer of the run about to be made"`
	Labels   map[string][]string `json:"labels,omitempty" jsonschema:"the situation of that run as key to values"`
}

type runItem struct {
	ID      string             `json:"id"`
	Version int                `json:"version"`
	Kind    knowledge.Kind     `json:"kind"`
	Content string             `json:"content"`
	Scope   knowledge.RunScope `json:"scope"`
}

// The approved items a run with these labels applies and whether they pass the review caps together
func (s *Server) knowledgeFor(ctx context.Context, _ *sdk.CallToolRequest, in knowledgeForInput) (*sdk.CallToolResult, any, error) {
	all, err := s.ledger.All(ctx)
	if err != nil {
		return nil, nil, err
	}
	items := all.For(in.Producer, in.Labels)
	out := make([]runItem, 0, len(items))
	chars := 0
	for _, k := range items {
		out = append(out, runItem{ID: k.ID, Version: k.Version, Kind: k.Kind, Content: k.Content, Scope: *k.Run})
		chars += utf8.RuneCountInString(k.Text())
	}
	return nil, map[string]any{
		"items": out, "chars": chars, "over_budget": chars > knowledge.ReviewChars || len(items) > knowledge.ReviewItems,
	}, nil
}

// A proposal scoped to runs
// 1. from fills the producer and the labels of a run a person corrected with edit or reject and cites it
// 2. labels given beside from replace the labels of the run
// 3. every label must be one a recorded run of the producer carries
func (s *Server) proposeRun(ctx context.Context, in proposeInput, draft knowledge.Knowledge, from *trace.Trace) (*sdk.CallToolResult, any, error) {
	run := knowledge.RunScope{Producer: in.Producer, Labels: in.Labels, Except: in.Except}
	if from != nil {
		if err := s.corrected(ctx, from.ID); err != nil {
			return nil, nil, err
		}
		run.Producer = cmp.Or(run.Producer, from.Producer)
		if len(run.Labels) == 0 {
			run.Labels = from.Labels
		}
		if !slices.Contains(draft.Evidence.FeedbackTraceIDs, from.ID) {
			draft.Evidence.FeedbackTraceIDs = append(draft.Evidence.FeedbackTraceIDs, from.ID)
		}
	}
	runs, err := s.traces.List(ctx, trace.Filter{Name: trace.NameRun})
	if err != nil {
		return nil, nil, err
	}
	if err := run.Recorded(runs.Vocabulary(run.Producer)); err != nil {
		return nil, nil, err
	}
	draft.Scope, draft.Exceptions, draft.Run = knowledge.Scope{}, nil, &run
	k, overlaps, err := s.ledger.Propose(ctx, draft)
	if err != nil {
		return nil, nil, err
	}
	folder, err := s.ledger.Folder(ctx, k.ID, k.Version)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{
		"id": k.ID, "version": k.Version, "status": k.Status, "overlaps": overlaps, "folder": newFolderAnswer(folder, false),
		"veto": k.Veto, "scope": k.Run, "drafted": k.Drafted,
	}, nil
}

// The latest verdict a person gave on the trace is an edit or a reject
func (s *Server) corrected(ctx context.Context, traceID string) error {
	verdicts, err := s.verdicts.List(ctx, feedback.Filter{TraceID: traceID})
	if err != nil {
		return err
	}
	human := feedback.Records(verdicts).Human().Latest()
	if len(human) == 0 || !human[0].Corrects() {
		return fmt.Errorf("%w: %s", ErrRunNotCorrected, traceID)
	}
	return nil
}
