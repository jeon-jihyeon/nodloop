package mcp

import (
	"context"
	"slices"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
)

// Opens the server over the config resolved at the moment of the call
// Owned by the composition root because only it reads the process environment and the config file
type Open func(ctx context.Context) (*Server, error)

// The tools of one server process
// 1. every call opens the server anew so a record dir saved through the CLI reaches the next call with no reconnect
// 2. one lock serializes the calls so two approvals of one candidate never both land
// 3. the tool list is the same whatever open answers so no list changed notification is ever due
type Host struct {
	open Open
	// Reported to the client in the MCP handshake
	version string
	// Sent in the handshake, which a client shows the model before any tool call
	// Empty sends none
	instructions string
	mu           sync.Mutex
	// The confirm forms of intervene sent and not yet answered, kept across calls since the server opens per call
	forms forms
}

func NewHost(open Open, version, instructions string) *Host {
	return &Host{open: open, version: version, instructions: instructions, forms: forms{}}
}

// Records one intervention under the call lock as POST /v1/interventions submits it
// The key proves a person or the call is a proposal, so no form is sent
func (h *Host) Intervene(ctx context.Context, r curate.Request) (InterveneAnswer, error) {
	var answer InterveneAnswer
	err := h.call(ctx, func(s *Server) error {
		var err error
		answer, err = s.Intervene(ctx, r, s.submitter())
		return err
	})
	return answer, err
}

func (h *Host) ServeTransport(ctx context.Context, t sdk.Transport) error {
	return h.Server(Tools()).Run(ctx, t)
}

// Serves the tools of the agent server so no tool deciding for a person is listed
func (h *Host) ServeAgent(ctx context.Context, t sdk.Transport) error {
	return h.Server(AgentTools()).Run(ctx, t)
}

// The MCP server with the named tools of the host
// A server key of a role names fewer tools so a call outside the role is no tool at all
func (h *Host) Server(names []string) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "nodloop", Version: h.version}, &sdk.ServerOptions{Instructions: h.instructions})
	for _, tl := range tools {
		if slices.Contains(names, tl.name) {
			tl.add(srv, h)
		}
	}
	return srv
}

// Runs one tool call on the server open answers under the call lock
// An open error is the answer of that call so the conversation can tell the user what to fix
func (h *Host) call(ctx context.Context, run func(*Server) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, err := h.open(ctx)
	if err != nil {
		return err
	}
	s.forms = h.forms
	return run(s)
}
