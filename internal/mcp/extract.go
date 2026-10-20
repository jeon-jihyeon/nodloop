package mcp

import (
	"cmp"
	"context"
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
)

type extractionInput struct {
	From string `json:"from" jsonschema:"the trace id of a run the user corrected with edit or reject"`
}

// What the conversation drafts a lesson from
func (s *Server) extraction(ctx context.Context, _ *sdk.CallToolRequest, in extractionInput) (*sdk.CallToolResult, any, error) {
	r, err := s.extractor.Reaction(ctx, in.From)
	if err != nil {
		return nil, nil, err
	}
	answer := map[string]any{
		"run": r.Run.ID, "producer": r.Run.Producer, "labels": r.Run.Labels, "output": r.Run.Output,
		"verdict": r.Verdict.Verdict, "reason_code": r.Verdict.ReasonCode, "reason": r.Verdict.Reason,
		"items":      newItemAnswers(r.Reached),
		"candidates": newItemAnswers(r.Waiting),
		"rules":      extract.Rules,
		"schema":     json.RawMessage(extract.Schema),
		"critic":     extract.CriticRules,
	}
	if len(r.Verdict.Edited) > 0 {
		answer["edited"] = r.Verdict.Edited
	}
	return nil, answer, nil
}

type proposeExtractionInput struct {
	From      string           `json:"from" jsonschema:"the trace id the extraction tool was called with"`
	Relation  extract.Relation `json:"relation" jsonschema:"add or update or duplicate or conflict"`
	RelatesTo string           `json:"relates_to,omitempty" jsonschema:"the id of the approved item or waiting candidate an update or a duplicate or a conflict names. Empty for add"`
	Kind      knowledge.Kind   `json:"kind" jsonschema:"meaning or judgment"`
	Content   string           `json:"content" jsonschema:"one sentence of what the correction taught. For an update the whole new text of the item"`
	Keys      []string         `json:"keys,omitempty" jsonschema:"label keys of the run the lesson needs. Empty reaches every run of the producer such as a way the user wants answers written"`
	Critique  extract.Critique `json:"critique" jsonschema:"your answers to the critic questions read as a second reader of the draft"`
	Author    string           `json:"author,omitempty" jsonschema:"who drafted. claude by default because the conversation drafts"`
}

// Checks the draft and the critique and proposes an add or an update or corroborates the candidate a duplicate names
func (s *Server) proposeExtraction(
	ctx context.Context, _ *sdk.CallToolRequest, in proposeExtractionInput,
) (*sdk.CallToolResult, any, error) {
	r, err := s.extractor.Reaction(ctx, in.From)
	if err != nil {
		return nil, nil, err
	}
	res, err := s.extractor.Propose(ctx, r, extract.Draft{
		Relation: in.Relation, RelatesTo: in.RelatesTo, Kind: in.Kind, Content: in.Content, Keys: in.Keys,
	}, in.Critique, cmp.Or(in.Author, defaultAuthor))
	if err != nil {
		return nil, nil, err
	}
	answer := map[string]any{"relation": res.Relation, "content": res.Content, "next": next[res.Relation]}
	if res.Related != nil {
		answer["related"] = newItemAnswers(knowledge.Set{*res.Related})[0]
	}
	if res.Candidate != nil {
		k := res.Candidate
		answer["candidate"] = map[string]any{"id": k.ID, "version": k.Version, "status": k.Status, "kind": k.Kind, "scope": k.Run}
		answer["overlaps"] = newItemAnswers(res.Overlaps)
	}
	if res.Corroborated() {
		answer["corroborated"], answer["next"] = true, nextCorroborated
	}
	return nil, answer, nil
}

// What happens after a duplicate of a waiting candidate
const nextCorroborated = "tell the user the waiting candidate already says this and the run joined its evidence, so nothing new was proposed. " +
	"It waits for the person as before"

// What happens after each relation
// No line names a tool or a command that decides
// A model that read one decided without asking
var next = map[extract.Relation]string{
	extract.RelationAdd: "show the candidate and its scope. It waits for the person, who is asked about it once unless their earlier decisions settle it. " +
		"Decide nothing yourself",
	extract.RelationUpdate: "show the related item and the candidate side by side. The candidate waits for the person, " +
		"who is asked about it once unless their earlier decisions settle it. Decide nothing yourself",
	extract.RelationDuplicate: "tell the user the related item already says this and nothing was proposed",
	extract.RelationConflict: "tell the user the related item or candidate says the opposite and nothing was proposed. " +
		"The person decides which stands, so change neither yourself",
}
