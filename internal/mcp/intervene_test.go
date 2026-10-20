package mcp_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/mcp"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/testkit"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The forms a client was asked to confirm, answered with one action
type forms struct {
	mu       sync.Mutex
	messages []string
}

func (f *forms) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.messages...)
}

// Options of a client that declares form elicitation and answers each form with the action
// A client that declares URL elicitation alone shows no form
func (f *forms) client(action string, urlOnly bool) *sdk.ClientOptions {
	elicitation := &sdk.ElicitationCapabilities{Form: &sdk.FormElicitationCapabilities{}}
	if urlOnly {
		elicitation = &sdk.ElicitationCapabilities{URL: &sdk.URLElicitationCapabilities{}}
	}
	return &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{Elicitation: elicitation},
		ElicitationHandler: func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.messages = append(f.messages, req.Params.Message)
			return &sdk.ElicitResult{Action: action}, nil
		},
	}
}

// The status of the candidate short
func status(t *testing.T, st testkit.Stores) knowledge.Status {
	t.Helper()
	all, err := st.Ledger.All(t.Context())
	require.NoError(t, err)
	k, err := all.Version("short", 1)
	require.NoError(t, err)
	return k.Status
}

// Only an accepted form, a key that proves a person or the terminal turns an intervention into the person's
// A declined or cancelled form records nothing and any other call records a proposal that leaves the ledger as it was
func TestServerIntervene(t *testing.T) {
	keep := map[string]any{"action": "judge", "target": "short:1", "choice": "keep", "session": "s-1"}
	type args struct {
		// The action the client answers a form with and empty for a client without forms
		form string
		// Whether the client declares URL elicitation alone
		urlOnly  bool
		protocol string
		caller   mcp.Caller
		in       map[string]any
	}
	type want struct {
		status        knowledge.Status
		forms         []string
		declined      bool
		authority     trace.Authority
		interventions []string
		errText       string
	}
	asked := []string{`Keep the lesson "give only the conclusion". Accept records it as your decision and nodloop acts on it at once. Decline records nothing`}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a form the person accepts decides at once", args{form: "accept", in: keep},
			want{knowledge.StatusApproved, asked, false, trace.AuthorityPerson, []string{"person elicitation person"}, ""}},
		{"a form of an earlier protocol the person accepts decides at once", args{form: "accept", protocol: "2025-06-18", in: keep},
			want{knowledge.StatusApproved, asked, false, trace.AuthorityPerson, []string{"person elicitation person"}, ""}},
		{"a form the person declines records nothing", args{form: "decline", in: keep},
			want{knowledge.StatusCandidate, asked, true, "", []string{}, ""}},
		{"a form the person cancels records nothing", args{form: "cancel", in: keep},
			want{knowledge.StatusCandidate, asked, true, "", []string{}, ""}},
		{"a client without the form records a proposal and leaves the ledger as it was", args{in: keep},
			want{knowledge.StatusCandidate, []string{}, false, trace.AuthorityAgent, []string{"agent none "}, ""}},
		{"a client that shows only URL elicitation records a proposal with no form", args{form: "accept", urlOnly: true, in: keep},
			want{knowledge.StatusCandidate, []string{}, false, trace.AuthorityAgent, []string{"agent none "}, ""}},
		{"a key that proves a person decides with no form", args{form: "decline", caller: mcp.Caller{Person: "ann", Proves: true}, in: keep},
			want{knowledge.StatusApproved, []string{}, false, trace.AuthorityPerson, []string{"person key ann"}, ""}},
		{"a key without --person records a proposal with no form", args{form: "accept", caller: mcp.Caller{Person: "ci"}, in: keep},
			want{knowledge.StatusCandidate, []string{}, false, trace.AuthorityAgent, []string{"agent none "}, ""}},
		{"a digest sends no form and is no proposal", args{form: "accept", in: map[string]any{"action": "digest"}},
			want{knowledge.StatusCandidate, []string{}, false, trace.AuthorityAgent, []string{"agent none "}, ""}},
		{"a knob outside its range is refused before any form", args{form: "accept", in: map[string]any{"action": "knob", "target": "audit_tier1", "value": 0.9}},
			want{knowledge.StatusCandidate, []string{}, false, "", []string{}, curate.ErrTargetInvalid.Error()}},
		{"a knob outside its range from a client without the form is refused", args{in: map[string]any{"action": "knob", "target": "audit_tier1", "value": 0.9}},
			want{knowledge.StatusCandidate, []string{}, false, "", []string{}, curate.ErrTargetInvalid.Error()}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			var f forms
			options := &sdk.ClientOptions{}
			if tc.args.form != "" {
				options = f.client(tc.args.form, tc.args.urlOnly)
			}
			c := testkit.ConnectWith(t, serve(st, tc.args.caller), options, tc.args.protocol)
			candidate(t, c)
			var got mcp.InterveneAnswer

			err := c.Call(t, "intervene", tc.args.in, &got)

			assert.Contains(t, errText(err), tc.want.errText)
			assert.Equal(t, tc.want.status, status(t, st))
			assert.Equal(t, tc.want.forms, f.asked())
			assert.Equal(t, []any{tc.want.declined, tc.want.authority}, []any{got.Declined, got.Authority})
			assert.Equal(t, tc.want.interventions, interventions(t, st))
		})
	}
}

// The answer carries the intervention as its published record and names what it wrote
func TestServerInterveneAnswer(t *testing.T) {
	st := testkit.Open(t)
	c := testkit.Connect(t, serve(st, mcp.Caller{Person: "ann", Proves: true}))
	candidate(t, c)
	var kept mcp.InterveneAnswer
	require.NoError(t, c.Call(t, "intervene", map[string]any{"action": "judge", "target": "short:1", "choice": "keep"}, &kept))
	var id struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(kept.Intervention, &id))
	var back mcp.InterveneAnswer

	err := c.Call(t, "intervene", map[string]any{"action": "reverse", "target": id.ID}, &back)

	require.NoError(t, err)
	assert.NoError(t, schema.Validate(string(trace.NameIntervention), kept.Intervention))
	assert.Equal(t, []trace.Written{{ID: "short", Version: 1, Status: "approved"}}, kept.Records)
	assert.Equal(t, []trace.Written{{ID: "short", Version: 1, Status: "retired"}, {ID: "short", Version: 2, Status: "candidate"}}, back.Records)
	assert.Equal(t, []string{"person key ann", "person key ann"}, interventions(t, st))
}

// A call that answers a form this server never sent fails and records nothing
func TestServerInterveneForgedForm(t *testing.T) {
	st := testkit.Open(t)
	var f forms
	c := testkit.ConnectWith(t, serve(st, mcp.Caller{}), f.client("accept", false), "")
	candidate(t, c)

	_, err := c.CallParams(t, &sdk.CallToolParams{
		Name: "intervene", Arguments: map[string]any{"action": "judge", "target": "short:1", "choice": "keep"},
		InputResponses: sdk.InputResponseMap{"confirm": &sdk.ElicitResult{Action: "accept"}}, RequestState: "made-up",
	})

	assert.ErrorContains(t, err, mcp.ErrFormUnknown.Error())
	assert.Equal(t, knowledge.StatusCandidate, status(t, st))
	assert.Empty(t, interventions(t, st))
}

// The host records an intervention of the HTTP route under the key of its server with no form
func TestHostIntervene(t *testing.T) {
	type args struct {
		caller mcp.Caller
	}
	type want struct {
		status    knowledge.Status
		authority trace.Authority
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a key that proves a person decides", args{mcp.Caller{Person: "ann", Proves: true}}, want{knowledge.StatusApproved, trace.AuthorityPerson}},
		{"a key without --person proposes", args{mcp.Caller{Person: "ci"}}, want{knowledge.StatusCandidate, trace.AuthorityAgent}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := testkit.Open(t)
			host := mcp.NewHost(open(st, tc.args.caller), "test", "")
			candidate(t, testkit.Connect(t, host.ServeTransport))

			got, err := host.Intervene(t.Context(), curate.Request{Action: trace.ActionJudge, Target: "short:1", Choice: trace.ChoiceKeep})

			require.NoError(t, err)
			assert.Equal(t, []any{tc.want.status, tc.want.authority}, []any{status(t, st), got.Authority})
		})
	}
}
