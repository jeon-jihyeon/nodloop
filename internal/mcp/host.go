package mcp

import (
	"context"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
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
}

func NewHost(open Open, version, instructions string) *Host {
	return &Host{open: open, version: version, instructions: instructions}
}

func (h *Host) ServeTransport(ctx context.Context, t sdk.Transport) error {
	srv := sdk.NewServer(&sdk.Implementation{Name: "nodloop", Version: h.version}, &sdk.ServerOptions{Instructions: h.instructions})
	for _, tl := range tools {
		tl.add(srv, h)
	}
	return srv.Run(ctx, t)
}

// Runs one tool call on the server open answers under the call lock
// An open error is the answer of that call so the conversation can tell the user what to fix
func (h *Host) call(ctx context.Context, run func(*Server) (*sdk.CallToolResult, any, error)) (*sdk.CallToolResult, any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, err := h.open(ctx)
	if err != nil {
		return nil, nil, err
	}
	return run(s)
}
