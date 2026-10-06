package mcp

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

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

type runInput struct {
	Producer string              `json:"producer" jsonschema:"who made the output such as session"`
	Labels   map[string][]string `json:"labels,omitempty" jsonschema:"the situation of the run as key to values such as repo, dir or task. Knowledge scoped to these labels reaches it"`
	Subject  string              `json:"subject,omitempty" jsonschema:"what the run was about in a few words"`
	Output   any                 `json:"output" jsonschema:"the output as it was given to the person, a JSON value or text"`
	Applied  []knowledge.Ref     `json:"applied,omitempty" jsonschema:"the knowledge items with their versions that the run applied, as knowledge_for answered them"`
}

// Records one output so a person can nod on it
func (s *Server) run(ctx context.Context, _ *sdk.CallToolRequest, in runInput) (*sdk.CallToolResult, any, error) {
	// Text stays a JSON string even when it reads as a number, and secrets go before anything is written
	b, err := json.Marshal(in.Output)
	if err != nil {
		return nil, nil, err
	}
	b = []byte(feedback.Redact(string(b)))
	input, err := json.Marshal(map[string][]knowledge.Ref{"applied": in.Applied})
	if err != nil {
		return nil, nil, err
	}
	if in.Producer == "" {
		return nil, nil, trace.ErrProducerRequired
	}
	tr, err := trace.NewRun(in.Producer, in.Subject, in.Labels, input, b, s.now())
	if err != nil {
		return nil, nil, err
	}
	tr.SessionID = s.session
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

// The approved items a run with these labels applies and whether they pass the run caps together
func (s *Server) knowledgeFor(ctx context.Context, _ *sdk.CallToolRequest, in knowledgeForInput) (*sdk.CallToolResult, any, error) {
	if in.Producer == "" {
		return nil, nil, trace.ErrProducerRequired
	}
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
		"items": out, "chars": chars, "over_budget": chars > knowledge.RunChars || len(items) > knowledge.RunItems,
		"context": items.Prompt(),
	}, nil
}

// A proposal scoped to runs
// 1. from fills the producer and the labels of a run a person corrected with edit or reject and cites it
// 2. labels given beside from replace the labels of the run
// 3. every label must be one a recorded run of the producer carries
func (s *Server) proposeRun(ctx context.Context, in proposeInput, draft knowledge.Knowledge, from *trace.Trace) (*sdk.CallToolResult, any, error) {
	draft.Run = &knowledge.RunScope{Producer: in.Producer, Labels: in.Labels, Except: in.Except}
	draft.NewLabels = in.NewLabels
	if from != nil {
		verdicts, err := s.verdicts.List(ctx, feedback.Filter{TraceID: from.ID})
		if err != nil {
			return nil, nil, err
		}
		if draft, err = draft.From(*from, verdicts); err != nil {
			return nil, nil, err
		}
	}
	if !in.NewLabels {
		runs, err := s.traces.List(ctx, trace.Filter{Name: trace.NameRun})
		if err != nil {
			return nil, nil, err
		}
		if err := draft.Run.Recorded(runs.Vocabulary(draft.Run.Producer)); err != nil {
			return nil, nil, err
		}
	}
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

type checkCallInput struct {
	Tool  string         `json:"tool" jsonschema:"the tool the agent is about to call such as Bash"`
	Input map[string]any `json:"input" jsonschema:"the arguments of the call. A shell command goes under command"`
}

// The veto of approved knowledge that matches the call, blocks winning over asks
func (s *Server) checkCall(ctx context.Context, _ *sdk.CallToolRequest, in checkCallInput) (*sdk.CallToolResult, any, error) {
	all, err := s.ledger.All(ctx)
	if err != nil {
		return nil, nil, err
	}
	vetoes, err := all.Guard()
	if err != nil {
		return nil, nil, err
	}
	matched := vetoes.Match(in.Tool, in.Input)
	if matched == nil {
		return nil, map[string]any{"action": "allow"}, nil
	}
	return nil, map[string]any{"action": matched.Action(), "veto": matched.ID(), "reason": matched.Reason()}, nil
}
