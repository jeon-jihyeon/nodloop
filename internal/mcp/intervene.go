package mcp

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The name a person who accepted a form decides under when config.json names no approver
const unnamedPerson = "person"

// The id of the one input request a confirm form sends
const formID = "confirm"

type interveneInput struct {
	Action trace.Action `json:"action" jsonschema:"judge, answer, dismiss, pause, resume, knob, reanchor, digest or reverse"`
	Target string       `json:"target,omitempty" jsonschema:"what it acts on: an item version as id:version, a compaction or the decide of a question for judge, the decide or expose of a question for answer and dismiss, the change kind add, update or compaction for pause, the pause for resume, the knob audit_tier0, audit_tier1, item_hold or cycle_weeks for knob, the producer of the place for reanchor and digest, an earlier intervention for reverse"`
	Choice trace.Choice `json:"choice,omitempty" jsonschema:"keep, undo or scope for judge, and one of the two choices of the question for answer"`
	// Any label keys so the schema names no key
	Labels map[string][]string `json:"labels,omitempty" jsonschema:"the labels of a scope choice, the scope a pause covers or the place of a reanchor or a digest"`
	Value  *float64            `json:"value,omitempty" jsonschema:"the value of a knob within its range: 0.05 to 0.5 for an audit share, 0 to 0.2 for item_hold, 1 to 4 for cycle_weeks"`
	// Lets the stop of the turn ask a proposal in the session that made it
	Session string `json:"session,omitempty" jsonschema:"the session id the prompt note names so a proposal is asked at the end of this turn"`
}

// What one intervention recorded as MCP intervene and POST /v1/interventions answer it
type InterveneAnswer struct {
	// The intervention record in its published schema and absent when the person declined the form
	Intervention json.RawMessage `json:"intervention,omitempty"`
	Authority    trace.Authority `json:"authority,omitempty"`
	Proof        trace.Proof     `json:"proof,omitempty"`
	// The knowledge records it wrote
	Records []trace.Written `json:"records"`
	// The answer event an answer wrote
	Answer string `json:"answer,omitempty"`
	// The cursor of a digest to pass as since to the records of the kinds answer, intervention, decide, expose and knowledge
	Since string `json:"since,omitempty"`
	// The decide that asks the person about a proposal at the stop of this turn
	Question string `json:"question,omitempty"`
	// The person declined or cancelled the form so nothing was recorded
	Declined bool `json:"declined,omitempty"`
	// Why approved.md or the veto file was not written while the intervention stands
	ExportError string `json:"export_error,omitempty"`
}

func (in interveneInput) request() curate.Request {
	return curate.Request{Action: in.Action, Target: in.Target, Choice: in.Choice, Labels: in.Labels, Value: in.Value, Session: in.Session}
}

// One intervention of the write side
// 1. under a key that proves a person it decides at once and a digest never needs a proof
// 2. over stdio a client that declared form elicitation is sent one confirm form, and an accepted form is the proof of the person
// 3. a declined or cancelled form records nothing
// 4. every other call records an agent proposal curate asks the person about
func (s *Server) intervene(ctx context.Context, req *sdk.CallToolRequest, in interveneInput) (*sdk.CallToolResult, InterveneAnswer, error) {
	r, by := in.request(), s.submitter()
	if by.Proof == trace.ProofNone && r.Action != trace.ActionDigest && s.formed(req) {
		form, accepted, err := s.confirm(ctx, req, r)
		switch {
		case err != nil:
			return nil, InterveneAnswer{}, err
		case form != nil:
			return form, InterveneAnswer{}, nil
		case !accepted:
			return nil, InterveneAnswer{Records: []trace.Written{}, Declined: true}, nil
		}
		by.Proof, by.Person = trace.ProofElicitation, cmp.Or(s.policy.Approver, unnamedPerson)
	}
	answer, err := s.submit(ctx, r, by)
	return nil, answer, err
}

// Who calls: a person under a key that proves one and else an agent whose call becomes a proposal
func (s *Server) submitter() curate.Submitter {
	return curate.Submitter{Proof: s.proof(), Person: s.caller.Person, Source: trace.SourceManual}
}

// Records the intervention of the submitter and answers it with the record in its published schema
// A failed export rides on the answer since the intervention is recorded
func (s *Server) submit(ctx context.Context, r curate.Request, by curate.Submitter) (InterveneAnswer, error) {
	res, err := s.intervener.Intervene(ctx, r, by)
	if err != nil && !errors.Is(err, knowledge.ErrExport) {
		return InterveneAnswer{}, err
	}
	s.afterVerdict(ctx)
	answer := InterveneAnswer{
		Intervention: published(res.Trace), Authority: by.Proof.Authority(), Proof: by.Proof, Records: res.Wrote.Written(),
		Answer: res.Answer, Since: res.Since, Question: res.Decide,
	}
	if err != nil {
		answer.ExportError = err.Error()
	}
	return answer, nil
}

// Whether the call may send the confirm form
// Only a stdio server whose client declared form elicitation sends one, since a server key proves a person or proves none
func (s *Server) formed(req *sdk.CallToolRequest) bool {
	if s.caller.Person != "" || s.forms == nil || req == nil || req.Session == nil {
		return false
	}
	p := req.Session.InitializeParams()
	if p == nil || p.Capabilities == nil || p.Capabilities.Elicitation == nil {
		return false
	}
	// A client that names neither mode asks forms as clients of earlier protocol versions did
	e := p.Capabilities.Elicitation
	return e.Form != nil || e.URL == nil
}

// The confirm form of the intervention as a round trip of the call
// 1. the first round answers the form as an input request with a nonce the host keeps for this payload
// 2. the round that returns with the response and the nonce of a form this host sent for the same payload carries the person's answer
// 3. a response without such a nonce fails, so a client cannot answer a form the person never saw
// The form names the action and its target in plain words and asks for nothing but confirm or cancel
func (s *Server) confirm(ctx context.Context, req *sdk.CallToolRequest, r curate.Request) (*sdk.CallToolResult, bool, error) {
	// A request of strings, numbers and labels always encodes
	payload, _ := json.Marshal(r)
	if req.Params.InputResponses == nil {
		text, err := s.intervener.Describe(ctx, r)
		if err != nil {
			return nil, false, err
		}
		params := &sdk.ElicitParams{
			Mode: "form", Message: text + ". Accept records it as your decision and nodloop acts on it at once. Decline records nothing",
			RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		}
		return &sdk.CallToolResult{InputRequests: sdk.InputRequestMap{formID: params}, RequestState: s.forms.open(string(payload))}, false, nil
	}
	if !s.forms.close(req.Params.RequestState, string(payload)) {
		return nil, false, fmt.Errorf("%w: call intervene without inputResponses and answer the form it sends", ErrFormUnknown)
	}
	res, ok := req.Params.InputResponses[formID].(*sdk.ElicitResult)
	return nil, ok && res.Action == "accept", nil
}

// The payload each form intervene sent asked about by the nonce of the form
// The host keeps them across the calls of one process and its call lock serializes every use
type forms map[string]string

// A new nonce for the payload
func (f forms) open(payload string) string {
	var b [16]byte
	// crypto rand Read never returns an error
	_, _ = rand.Read(b[:])
	nonce := hex.EncodeToString(b[:])
	f[nonce] = payload
	return nonce
}

// Whether the nonce names a form sent for the payload, used once
func (f forms) close(nonce, payload string) bool {
	sent, ok := f[nonce]
	delete(f, nonce)
	return ok && sent == payload
}
