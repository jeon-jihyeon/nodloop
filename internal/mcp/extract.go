package mcp

import (
	"cmp"
	"context"
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/call"
	"github.com/jeon-jihyeon/nodloop/internal/extract"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type extractionInput struct {
	From string `json:"from" jsonschema:"the trace id of a run the user corrected with edit or reject"`
}

type extractionAnswer struct {
	Run        string              `json:"run"`
	Producer   string              `json:"producer"`
	Labels     trace.Labels        `json:"labels"`
	Output     json.RawMessage     `json:"output"`
	Verdict    feedback.Verdict    `json:"verdict"`
	ReasonCode feedback.ReasonCode `json:"reason_code"`
	Reason     string              `json:"reason"`
	Edited     json.RawMessage     `json:"edited,omitempty"`
	// The shapes of the tool calls of the turn a cue may name
	Calls      call.Shapes     `json:"calls"`
	Items      []itemAnswer    `json:"items"`
	Candidates []itemAnswer    `json:"candidates"`
	Retired    []retiredAnswer `json:"retired"`
	Rules      string          `json:"rules"`
	Schema     json.RawMessage `json:"schema"`
	Critic     string          `json:"critic"`
}

// A retired version with who retired it and why so a duplicate of it can name it
type retiredAnswer struct {
	itemAnswer
	Approver string           `json:"approver"`
	Reason   knowledge.Reason `json:"reason,omitempty"`
}

func newRetiredAnswers(set knowledge.Set) []retiredAnswer {
	out := make([]retiredAnswer, 0, len(set))
	for i, item := range newItemAnswers(set) {
		out = append(out, retiredAnswer{itemAnswer: item, Approver: set[i].Approver, Reason: set[i].Reason})
	}
	return out
}

// What the conversation drafts a lesson from
func (s *Server) extraction(ctx context.Context, _ *sdk.CallToolRequest, in extractionInput) (*sdk.CallToolResult, extractionAnswer, error) {
	r, err := s.extractor.Reaction(ctx, in.From)
	if err != nil {
		return nil, extractionAnswer{}, err
	}
	return nil, extractionAnswer{
		Run: r.Run.ID, Producer: r.Run.Producer, Labels: r.Run.Labels, Output: r.Run.Output,
		Verdict: r.Verdict.Verdict, ReasonCode: r.Verdict.ReasonCode, Reason: r.Verdict.Reason, Edited: r.Verdict.Edited,
		Calls: append(call.Shapes{}, r.Calls...), Items: newItemAnswers(r.Reached), Candidates: newItemAnswers(r.Waiting), Retired: newRetiredAnswers(r.Retired),
		Rules: extract.Rules, Schema: json.RawMessage(extract.Schema), Critic: extract.CriticRules,
	}, nil
}

type proposeExtractionInput struct {
	From      string           `json:"from" jsonschema:"the trace id the extraction tool was called with"`
	Relation  extract.Relation `json:"relation" jsonschema:"add or update or duplicate or conflict"`
	RelatesTo string           `json:"relates_to,omitempty" jsonschema:"the id of the approved item or waiting candidate an update or a conflict names or of the item, candidate or retired version a duplicate names. Empty for add"`
	Kind      knowledge.Kind   `json:"kind" jsonschema:"meaning or judgment"`
	Content   string           `json:"content" jsonschema:"one sentence of what the correction taught. For an update the whole new text of the item"`
	Keys      []string         `json:"keys,omitempty" jsonschema:"label keys of the run the lesson needs. Empty reaches every run of the producer such as a way the user wants answers written"`
	Excerpt   string           `json:"excerpt,omitempty" jsonschema:"the shortest part of the output the correction is about quoted word for word in at most 200 characters"`
	Cue       *knowledge.Cue   `json:"cue,omitempty" jsonschema:"the call of the turn a judgment is about, taken from calls, so it arrives right before that call. Left out when the lesson holds beyond that call"`
	Critique  extract.Critique `json:"critique" jsonschema:"your answers to the critic questions read as a second reader of the draft"`
	Author    string           `json:"author,omitempty" jsonschema:"who drafted. claude by default because the conversation drafts"`
}

// Checks the draft and the critique and proposes its candidate or corroborates the candidate a duplicate names
func (s *Server) proposeExtraction(
	ctx context.Context, _ *sdk.CallToolRequest, in proposeExtractionInput,
) (*sdk.CallToolResult, proposeExtractionAnswer, error) {
	r, err := s.extractor.Reaction(ctx, in.From)
	if err != nil {
		return nil, proposeExtractionAnswer{}, err
	}
	res, err := s.extractor.Propose(ctx, r, extract.Draft{
		Relation: in.Relation, RelatesTo: in.RelatesTo, Kind: in.Kind, Content: in.Content, Keys: in.Keys, Excerpt: in.Excerpt, Cue: in.Cue,
	}, in.Critique, cmp.Or(in.Author, defaultAuthor))
	if err != nil {
		return nil, proposeExtractionAnswer{}, err
	}
	answer := proposeExtractionAnswer{Relation: res.Relation, Content: res.Content, Next: next[res.Relation]}
	if res.Related != nil {
		answer.Related = &newItemAnswers(knowledge.Set{*res.Related})[0]
	}
	if res.Candidate != nil {
		k := res.Candidate
		answer.Candidate = &candidateAnswer{ID: k.ID, Version: k.Version, Status: k.Status, Kind: k.Kind, Scope: k.Run, Cue: k.Cue}
		answer.Overlaps = newItemAnswers(res.Overlaps)
	}
	switch {
	case res.Corroborated():
		answer.Corroborated, answer.Next = true, nextCorroborated
	case res.ProposedAgain():
		answer.Next = nextProposedAgain
	}
	return nil, answer, nil
}

type proposeExtractionAnswer struct {
	Relation extract.Relation `json:"relation"`
	Content  string           `json:"content"`
	Next     string           `json:"next"`
	Related  *itemAnswer      `json:"related,omitempty"`
	// The candidate the draft proposed or the one a duplicate corroborated
	Candidate *candidateAnswer `json:"candidate,omitempty"`
	// Present with a candidate only
	Overlaps     []itemAnswer `json:"overlaps,omitzero"`
	Corroborated bool         `json:"corroborated,omitempty"`
}

type candidateAnswer struct {
	ID      string              `json:"id"`
	Version int                 `json:"version"`
	Status  knowledge.Status    `json:"status"`
	Kind    knowledge.Kind      `json:"kind"`
	Scope   *knowledge.RunScope `json:"scope"`
	Cue     *knowledge.Cue      `json:"cue,omitempty"`
}

// What happens after a duplicate of a waiting candidate
const nextCorroborated = "tell the user the waiting candidate already says this and the run joined its evidence, so nothing new was proposed. " +
	"It waits for the person as before"

// What happens after a duplicate of a retired version
const nextProposedAgain = "tell the user the related lesson was retired earlier and is proposed again as its next version with this run in its evidence. " +
	"It waits for the person, who is asked about it once unless their earlier decisions settle it. Decide nothing yourself"

// What happens after each relation
// No line names a tool or a command that decides
// A model that read one decided without asking
var next = map[extract.Relation]string{
	extract.RelationAdd: "show the candidate and its scope. It waits for the person, who is asked about it once unless their earlier decisions settle it. " +
		"Decide nothing yourself",
	extract.RelationUpdate: "show the related item and the candidate side by side. The candidate waits for the person, " +
		"who is asked about it once unless their earlier decisions settle it. Decide nothing yourself",
	extract.RelationDuplicate: "tell the user the related item already says this and nothing was proposed",
	extract.RelationConflict: "show the related item or candidate and the candidate side by side. The candidate is its next version and says the opposite. " +
		"The person decides which stands and is asked once unless their earlier decisions settle it, so change neither yourself",
}
