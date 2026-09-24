package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// An MCP client connected to a server over in-memory pipes
// The server and the session end with the test
type Client struct {
	session *sdk.ClientSession
}

func Connect(t *testing.T, serve func(context.Context, sdk.Transport) error) Client {
	t.Helper()
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	ctx := t.Context()
	// The server only stops when the test ends and cancels ctx so its error carries nothing to check
	go func() { _ = serve(ctx, serverTransport) }()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "testkit", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return Client{session: session}
}

// For a call whose answer the test does not read
func (c Client) Run(t *testing.T, name string, args any) error {
	t.Helper()
	_, err := c.call(t, name, args)
	return err
}

// Decodes the structured content into out
func (c Client) Call(t *testing.T, name string, args, out any) error {
	t.Helper()
	res, err := c.call(t, name, args)
	if err != nil {
		return err
	}
	b, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, out), "%s: %s", name, b)
	return nil
}

// A tool error result returns ErrTool with the text content as message
func (c Client) call(t *testing.T, name string, args any) (*sdk.CallToolResult, error) {
	t.Helper()
	res, err := c.session.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err, "%s: protocol error", name)
	if !res.IsError {
		return res, nil
	}
	var texts []string
	for _, content := range res.Content {
		if text, ok := content.(*sdk.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrTool, strings.Join(texts, " "))
}

func (c Client) Tools(t *testing.T) []string {
	t.Helper()
	res, err := c.session.ListTools(t.Context(), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}
