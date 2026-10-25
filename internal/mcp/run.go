package mcp

import (
	"context"
	"encoding/json"
	"math"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/compact"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

type checkCompactionInput struct {
	Compaction string                   `json:"compaction" jsonschema:"the compaction id from propose_compaction"`
	Items      []compact.CoverageAnswer `json:"items" jsonschema:"one entry per old item: the new items that state it and every fact of it no new item states"`
}

type checkCompactionAnswer struct {
	Compaction string                   `json:"compaction"`
	Items      []knowledge.CoverageItem `json:"items"`
	Passed     bool                     `json:"passed"`
	Why        string                   `json:"why,omitempty"`
}

// Records the coverage the conversation checked for a compaction of run items
// Approval reads the newest one so a fixed draft is checked again before approve_compaction
func (s *Server) checkCompaction(
	ctx context.Context, _ *sdk.CallToolRequest, in checkCompactionInput,
) (*sdk.CallToolResult, checkCompactionAnswer, error) {
	cov, err := s.compactor.Record(ctx, in.Compaction, in.Items)
	if err != nil {
		return nil, checkCompactionAnswer{}, err
	}
	c, err := s.ledger.Compaction(ctx, in.Compaction)
	if err != nil {
		return nil, checkCompactionAnswer{}, err
	}
	answer := checkCompactionAnswer{Compaction: cov.Compaction, Items: cov.Items, Passed: true}
	if err := cov.Passes(c); err != nil {
		answer.Passed, answer.Why = false, err.Error()
	}
	return nil, answer, nil
}

type runInput struct {
	Producer string              `json:"producer" jsonschema:"who made the output such as session"`
	Labels   map[string][]string `json:"labels,omitempty" jsonschema:"the situation of the run with one value per key. correlation preserves an external conversation id but is excluded from inferred knowledge scope"`
	Subject  string              `json:"subject,omitempty" jsonschema:"what the run was about in a few words"`
	Model    string              `json:"model,omitempty" jsonschema:"the one model the producer runs. Leave it out when the producer may switch models"`
	Output   any                 `json:"output" jsonschema:"the output as it was given to the person, a JSON value or text"`
	Applied  []knowledge.Ref     `json:"applied,omitempty" jsonschema:"the knowledge items with their versions that the run applied, as knowledge_for answered them"`
	Withheld []knowledge.Ref     `json:"withheld,omitempty" jsonschema:"the withheld items knowledge_for answered when the holdout drew the run. Never together with applied"`
	// The run tool takes the draw again from the key so a producer passes back only what it holds
	HoldoutKey string `json:"holdout_key,omitempty" jsonschema:"the holdout key the run passed to knowledge_for. Report effect counts only runs recorded with it"`
	DurationMS int64  `json:"duration_ms,omitempty" jsonschema:"how many milliseconds the run took up to this call so its span has a length"`
}

type runAnswer struct {
	TraceID string `json:"trace_id"`
}

// Records one output so a person can nod on it
// AfterRun of the policy follows the record
func (s *Server) run(ctx context.Context, _ *sdk.CallToolRequest, in runInput) (*sdk.CallToolResult, runAnswer, error) {
	if in.DurationMS < 0 {
		return nil, runAnswer{}, trace.ErrDurationNegative
	}
	if in.DurationMS > math.MaxInt64/int64(time.Millisecond) {
		return nil, runAnswer{}, ErrDurationInvalid
	}
	refs, err := knowledge.NewRunInput(in.Applied, in.Withheld, "")
	if err != nil {
		return nil, runAnswer{}, err
	}
	// Text stays a JSON string even when it reads as a number
	// Secrets go before anything is written
	b, err := json.Marshal(in.Output)
	if err != nil {
		return nil, runAnswer{}, err
	}
	b = []byte(feedback.Redact(string(b)))
	if in.Producer == "" {
		return nil, runAnswer{}, trace.ErrProducerRequired
	}
	input, err := json.Marshal(s.policy.Holdout.Drawn(refs, in.Producer, in.HoldoutKey))
	if err != nil {
		return nil, runAnswer{}, err
	}
	tr, err := trace.NewRun(in.Producer, in.Subject, in.Labels, input, b, s.now())
	if err != nil {
		return nil, runAnswer{}, err
	}
	tr.SessionID, tr.Model = s.session, in.Model
	if tr, err = tr.Took(time.Duration(in.DurationMS) * time.Millisecond); err != nil {
		return nil, runAnswer{}, err
	}
	if err := s.traces.Append(ctx, tr.Record()); err != nil {
		return nil, runAnswer{}, err
	}
	if s.policy.AfterRun != nil {
		s.policy.AfterRun(ctx)
	}
	return nil, runAnswer{TraceID: tr.ID}, nil
}

type knowledgeForInput struct {
	Producer string              `json:"producer" jsonschema:"the producer of the run about to be made"`
	Labels   map[string][]string `json:"labels,omitempty" jsonschema:"the situation of that run as key to values"`
	// A producer opts in to the holdout by passing one
	HoldoutKey string `json:"holdout_key,omitempty" jsonschema:"a string kept for one run such as a window id. The holdout draws on it so a share of runs gets no item and report effect compares them"`
}

type runItem struct {
	ID      string             `json:"id"`
	Version int                `json:"version"`
	Kind    knowledge.Kind     `json:"kind"`
	Content string             `json:"content"`
	Scope   knowledge.RunScope `json:"scope"`
}

type knowledgeForAnswer struct {
	Items      []runItem `json:"items"`
	Chars      int       `json:"chars"`
	OverBudget bool      `json:"over_budget"`
	Context    string    `json:"context"`
	// Present only when the holdout drew the call and empty when the place has no approved item
	Withheld []knowledge.Ref `json:"withheld,omitzero"`
}

// The approved items a run with these labels applies and whether they pass RunChars together
// A run the holdout draws gets no item and withheld names every item it would have received
func (s *Server) knowledgeFor(
	ctx context.Context, _ *sdk.CallToolRequest, in knowledgeForInput,
) (*sdk.CallToolResult, knowledgeForAnswer, error) {
	if in.Producer == "" {
		return nil, knowledgeForAnswer{}, trace.ErrProducerRequired
	}
	labels, err := trace.Labels(in.Labels).ForRun()
	if err != nil {
		return nil, knowledgeForAnswer{}, err
	}
	all, err := s.ledger.All(ctx)
	if err != nil {
		return nil, knowledgeForAnswer{}, err
	}
	items := all.ForAt(in.Producer, labels, s.now())
	var withheld []knowledge.Ref
	if s.policy.Holdout.WithholdsRun(in.Producer, in.HoldoutKey) {
		items, withheld = nil, items.Refs()
	}
	out := make([]runItem, 0, len(items))
	chars := 0
	for _, k := range items {
		out = append(out, runItem{ID: k.ID, Version: k.Version, Kind: k.Kind, Content: k.Content, Scope: *k.Run})
		chars += utf8.RuneCountInString(k.Text())
	}
	return nil, knowledgeForAnswer{
		Items: out, Chars: chars, OverBudget: chars > knowledge.RunChars, Context: items.Prompt(), Withheld: withheld,
	}, nil
}

// A proposal scoped to runs
// 1. from fills the producer and the labels of a run a person corrected with edit or reject and cites it
// 2. labels given beside from replace the labels of the run
// 3. every label must be one a recorded run of the producer carries
func (s *Server) proposeRun(ctx context.Context, in proposeInput, draft knowledge.Knowledge, from *trace.Trace) (proposeAnswer, error) {
	draft.Run = &knowledge.RunScope{Producer: in.Producer, Labels: in.Labels, Except: in.Except,
		ValidFrom: in.ValidFrom, ValidUntil: in.ValidUntil}
	draft.NewLabels = in.NewLabels
	for _, id := range draft.Evidence.FeedbackTraceIDs {
		found, err := s.traces.Get(ctx, id)
		if err != nil {
			return proposeAnswer{}, err
		}
		draft.Evidence = draft.Evidence.Join(id, trace.Restore(found).EvidenceUnit())
	}
	if from != nil {
		verdicts, err := s.verdicts.List(ctx, record.FeedbackFilter{TraceID: from.ID})
		if err != nil {
			return proposeAnswer{}, err
		}
		if draft, err = draft.From(*from, feedback.RestoreAll(verdicts)); err != nil {
			return proposeAnswer{}, err
		}
	}
	if !in.NewLabels {
		runs, err := s.traces.List(ctx, record.TraceFilter{Name: string(trace.NameRun)})
		if err != nil {
			return proposeAnswer{}, err
		}
		if err := draft.Run.Recorded(trace.RestoreAll(runs).Vocabulary(draft.Run.Producer)); err != nil {
			return proposeAnswer{}, err
		}
	}
	k, overlaps, err := s.ledger.Propose(ctx, draft)
	if err != nil {
		return proposeAnswer{}, err
	}
	folder, err := s.ledger.Folder(ctx, k.ID, k.Version)
	if err != nil {
		return proposeAnswer{}, err
	}
	return proposeAnswer{
		ID: k.ID, Version: k.Version, Status: k.Status, Overlaps: overlaps, Folder: newFolderAnswer(folder),
		Veto: k.Veto, Scope: k.Run, Drafted: k.Drafted,
	}, nil
}

type proposeAnswer struct {
	ID       string              `json:"id"`
	Version  int                 `json:"version"`
	Status   knowledge.Status    `json:"status"`
	Overlaps knowledge.Set       `json:"overlaps"`
	Folder   folderAnswer        `json:"folder"`
	Veto     *knowledge.Veto     `json:"veto"`
	Scope    *knowledge.RunScope `json:"scope"`
	Drafted  bool                `json:"drafted"`
}

type checkCallInput struct {
	Producer string         `json:"producer,omitempty" jsonschema:"the producer about to make the call. Only the vetoes approved for it apply and every veto when empty"`
	Tool     string         `json:"tool" jsonschema:"the tool the agent is about to call such as Bash"`
	Input    map[string]any `json:"input" jsonschema:"the arguments of the call. A shell command goes under command"`
}

type checkCallAnswer struct {
	// allow or the action of the veto that matched
	Action string `json:"action"`
	Veto   string `json:"veto,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// The veto of approved knowledge that matches the call
// A block wins over an ask
func (s *Server) checkCall(ctx context.Context, _ *sdk.CallToolRequest, in checkCallInput) (*sdk.CallToolResult, checkCallAnswer, error) {
	all, err := s.ledger.All(ctx)
	if err != nil {
		return nil, checkCallAnswer{}, err
	}
	vetoes, err := all.Guard(in.Producer)
	if err != nil {
		return nil, checkCallAnswer{}, err
	}
	matched := vetoes.Match(in.Tool, in.Input)
	if matched == nil {
		return nil, checkCallAnswer{Action: "allow"}, nil
	}
	return nil, checkCallAnswer{Action: string(matched.Action()), Veto: matched.ID(), Reason: matched.Reason()}, nil
}
